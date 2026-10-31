package usecases

import (
	"errors"
	"fmt"
	"log"
	"time"

	kafkaconfig "psp_go/internal/kafka"
	"psp_go/internal/model"
	"psp_go/internal/parser"
	"psp_go/internal/repository"
	"psp_go/internal/securitysale"
)

var (
	ErrInvalidPAN             = errors.New("PAN inválido")
	ErrUnsupportedCardNetwork = errors.New("bandeira do cartão não suportada")
	ErrUnknownIssuerBank      = errors.New("banco emissor não encontrado para o BIN informado")
	ErrCardDataMismatch       = errors.New("PAN, bandeira ou emissor divergem do cartão cadastrado")
)

// Intervalos inclusivos e simulados do MVP. Os limites têm exatamente
// 6 dígitos e são comparados com pan[:6], após a validação dos dígitos.
type binRange struct {
	start   string
	end     string
	network model.CardNetwork
	issuer  model.IssuerBank
}

var binRanges = []binRange{
	{start: "520000", end: "520099", network: model.NetworkMastercard, issuer: model.IssuerNubank},
	{start: "520100", end: "520199", network: model.NetworkMastercard, issuer: model.IssuerItau},
	{start: "400000", end: "400099", network: model.NetworkVisa, issuer: model.IssuerNubank},
	{start: "400100", end: "400199", network: model.NetworkVisa, issuer: model.IssuerBradesco},
}

type SaleCase struct {
	repo   *repository.UserRepository
	events *kafkaconfig.NewProducerCase
}

func NewSaleCaseDependency(repo *repository.UserRepository, producer ...*kafkaconfig.NewProducerCase) *SaleCase {
	s := &SaleCase{repo: repo}
	if len(producer) > 0 {
		s.events = producer[0]
	}
	return s
}

// SaleCard retorna a decisão da PSP após validar a transação e autenticar o PIN.
// keys deve conter as chaves compartilhadas com a máquina daquele usuário.
// Cada tentativa tem seu histórico. A aprovação e a movimentação de saldo/limite são atômicas;
// qualquer saída reprovada finaliza o mesmo registro sem movimentar saldo.
func (s *SaleCase) SaleCard(saleCard model.CardPayment, keys PaymentKeys) (result model.AuthorizationResponse) {
	result.Status = model.StatusReproved
	result.Log = "Pagamento reprovado."
	result.PaymentType = saleCard.PaymentType
	result.TimeNow = saleCard.TimeNow
	if s.events != nil {
		event := model.PaymentKafka{SaleId: saleCard.SaleId, Value: saleCard.Amount, PaymentType: saleCard.PaymentType, Status: model.StatusPending}
		if err := s.events.ProducerGo(event); err != nil {
			log.Printf("falha ao enfileirar evento PENDING sale_id=%d", saleCard.SaleId)
		}
		// Registrado antes do defer do histórico, para publicar a decisão após finalizá-lo.
		defer func() {
			event.Status = result.Status
			if result.Amount != "" {
				event.Value = result.Amount
			}
			if err := s.events.ProducerGo(event); err != nil {
				log.Printf("falha ao enfileirar evento final sale_id=%d", saleCard.SaleId)
			}
		}()
	}
	transactionID, err := s.repo.CreatePendingSale(saleCard.SaleId)
	if err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	defer func() {
		if result.Status == model.StatusReproved {
			if err := s.repo.UpdateSaleStatus(transactionID, result.Status); err != nil {
				log.Printf("falha ao registrar reprovação transaction_id=%d", transactionID)
				result.Log += " Não foi possível salvar o histórico."
			}
		}
	}()

	// O histórico é comum; cada tipo segue sua própria função de autorização.
	switch saleCard.PaymentType {
	case model.Debit:
		return s.debit(saleCard, keys, transactionID, result)
	case model.Credit:
		return s.credit(saleCard, keys, transactionID, result)
	default:
		result.Log = authorizationLog(repository.ErrUnsupportedPaymentType)
		return result
	}
}

// debit concentra as validações do cartão e a aprovação com débito atômico.
func (s *SaleCase) debit(saleCard model.CardPayment, keys PaymentKeys, transactionID int, result model.AuthorizationResponse) model.AuthorizationResponse {
	decoded, err := parser.DecodeTLV(saleCard)
	if err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	whole, fraction := decoded.AmountCents/100, decoded.AmountCents%100
	sign := ""
	if decoded.AmountCents < 0 {
		sign, whole, fraction = "-", -whole, -fraction
	}
	result.Amount = fmt.Sprintf("%s%d.%02d", sign, whole, fraction)
	// Preenche os metadados disponíveis também para vendas reprovadas.
	// A decisão sobre routing permanece após as validações estruturais e CMAC.
	var routingErr error
	result.CardNetwork, result.IssuerBank, routingErr = VerifyPANRouting(decoded.PAN)
	if err := s.repo.UpdateSaleDetails(transactionID, decoded.PAN, decoded.AmountCents, decoded.PaymentType, nil); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := securitysale.VerifyTransaction(decoded); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := VerifyCryptogram(saleCard, keys.CMACKeyHex); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if routingErr != nil {
		result.Log = authorizationLog(routingErr)
		return result
	}
	card, err := s.repo.GetCardByPAN(decoded.PAN)
	if err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := s.repo.UpdateSaleDetails(transactionID, decoded.PAN, decoded.AmountCents, decoded.PaymentType, &card.ID); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if card.CardType != decoded.PaymentType {
		result.Log = "Tipo de cartão divergente do pagamento."
		return result
	}
	if err := VerifyCardIdentity(decoded, card, result.CardNetwork, result.IssuerBank); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := VerifyDateExpiry(decoded.DateExpiry, card.DateExpiry, time.Now()); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	pin, err := VerifyAESGCM(saleCard.Emv.Pin.PIN, keys.GCMKeyHex)
	if err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := VerifyPIN(pin, card.PINHash); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := VerifyDebitBalance(card.Balance, decoded.AmountCents, decoded.PaymentType); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := s.repo.ApproveSale(transactionID, card.ID, decoded.AmountCents); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	result.Status = model.StatusApproved
	result.Log = ""
	return result
}

// credit autentica como débito e soma a venda ao valor utilizado do limite.
func (s *SaleCase) credit(saleCard model.CardPayment, keys PaymentKeys, transactionID int, result model.AuthorizationResponse) model.AuthorizationResponse {
	decoded, err := parser.DecodeTLV(saleCard)
	if err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	whole, fraction := decoded.AmountCents/100, decoded.AmountCents%100
	sign := ""
	if decoded.AmountCents < 0 {
		sign, whole, fraction = "-", -whole, -fraction
	}
	result.Amount = fmt.Sprintf("%s%d.%02d", sign, whole, fraction)
	// Preenche os metadados disponíveis também para vendas reprovadas.
	// A decisão sobre routing permanece após as validações estruturais e CMAC.
	var routingErr error
	result.CardNetwork, result.IssuerBank, routingErr = VerifyPANRouting(decoded.PAN)
	if err := s.repo.UpdateSaleDetails(transactionID, decoded.PAN, decoded.AmountCents, decoded.PaymentType, nil); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := securitysale.VerifyTransaction(decoded); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := VerifyCryptogram(saleCard, keys.CMACKeyHex); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if routingErr != nil {
		result.Log = authorizationLog(routingErr)
		return result
	}
	card, err := s.repo.GetCreditCardByPAN(decoded.PAN)
	if err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := s.repo.UpdateSaleDetails(transactionID, decoded.PAN, decoded.AmountCents, decoded.PaymentType, &card.ID); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if card.CardType != decoded.PaymentType {
		result.Log = "Tipo de cartão divergente do pagamento."
		return result
	}
	if err := VerifyCardIdentity(decoded, card, result.CardNetwork, result.IssuerBank); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := VerifyDateExpiry(decoded.DateExpiry, card.DateExpiry, time.Now()); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	pin, err := VerifyAESGCM(saleCard.Emv.Pin.PIN, keys.GCMKeyHex)
	if err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := VerifyPIN(pin, card.PINHash); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := VerifyCreditLimit(card.Limit, card.Amount, decoded.AmountCents, decoded.PaymentType); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	if err := s.repo.ApproveCreditSale(transactionID, card.ID, decoded.AmountCents); err != nil {
		result.Log = authorizationLog(err)
		return result
	}
	result.Status = model.StatusApproved
	result.Log = ""
	return result
}

// VerifyPANRouting recebe um PAN apenas com dígitos e retorna bandeira e emissor.
// Valida formato, comprimento e Luhn; isso não confirma a existência do cartão.
// Se o BIN estiver fora dos ranges simulados, retorna a bandeira, emissor vazio
// e erro. Dentro de um range, retorna a bandeira e o emissor definidos nele.
func VerifyPANRouting(pan string) (model.CardNetwork, model.IssuerBank, error) {
	if len(pan) < 12 || len(pan) > 19 {
		return "", "", ErrInvalidPAN
	}

	// Luhn: dobre os dígitos alternados, começando pelo penúltimo.
	sum := 0
	double := false
	for i := len(pan) - 1; i >= 0; i-- {
		if pan[i] < '0' || pan[i] > '9' {
			return "", "", ErrInvalidPAN
		}

		digit := int(pan[i] - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	if sum == 0 || sum%10 != 0 {
		return "", "", ErrInvalidPAN
	}

	var network model.CardNetwork
	bin := pan[:6] // No MVP, o BIN é sempre formado pelos 6 primeiros dígitos.
	switch {
	case pan[0] == '4':
		if len(pan) != 13 && len(pan) != 16 && len(pan) != 19 {
			return "", "", ErrInvalidPAN
		}
		network = model.NetworkVisa
	case (pan[:2] >= "51" && pan[:2] <= "55") || (bin >= "222100" && bin <= "272099"):
		if len(pan) != 16 {
			return "", "", ErrInvalidPAN
		}
		network = model.NetworkMastercard
	default:
		return "", "", ErrUnsupportedCardNetwork
	}

	for _, interval := range binRanges {
		if bin >= interval.start && bin <= interval.end {
			return interval.network, interval.issuer, nil
		}
	}
	return network, "", ErrUnknownIssuerBank
}

// authorizationLog usa somente mensagens fixas, nunca o conteúdo de erros externos.
// Erros de SQL, payload ou configuração podem carregar dados que não devem sair na resposta.
func authorizationLog(err error) string {
	switch {
	case errors.Is(err, repository.ErrCardNotFound):
		return "Cartão inexistente para este tipo de pagamento."
	case errors.Is(err, repository.ErrInsufficientBalance):
		return "Saldo insuficiente."
	case errors.Is(err, repository.ErrInsufficientCreditLimit):
		return "Limite de crédito insuficiente."
	case errors.Is(err, repository.ErrInvalidAmount), errors.Is(err, parser.ErrInvalidAmount):
		return "Valor do pagamento inválido; informe um valor maior que zero."
	case errors.Is(err, repository.ErrUnsupportedPaymentType), errors.Is(err, securitysale.ErrUnsupportedPaymentType):
		return "Tipo de pagamento não suportado."
	case errors.Is(err, repository.ErrSaleNotPending):
		return "Transação já finalizada ou com dados divergentes."
	case errors.Is(err, parser.ErrInvalidTLV):
		return "Dados EMV inválidos."
	case errors.Is(err, ErrInvalidPAN), errors.Is(err, securitysale.ErrInvalidPANFormat):
		return "Número do cartão inválido."
	case errors.Is(err, ErrUnsupportedCardNetwork):
		return "Bandeira do cartão não suportada."
	case errors.Is(err, ErrUnknownIssuerBank):
		return "Emissor do cartão não identificado."
	case errors.Is(err, ErrCardDataMismatch):
		return "Dados do cartão divergentes do cadastro."
	case errors.Is(err, securitysale.ErrInvalidDateExpiryFormat):
		return "Formato ou data de validade inválidos."
	case errors.Is(err, ErrDateExpiryMismatch):
		return "Validade divergente do cadastro."
	case errors.Is(err, ErrCardExpired):
		return "Cartão expirado."
	case errors.Is(err, securitysale.ErrInvalidTransactionCountry):
		return "País da transação não suportado."
	case errors.Is(err, securitysale.ErrInvalidStatus):
		return "Status inicial do pagamento inválido; esperado PENDING."
	case errors.Is(err, ErrInvalidCryptogram):
		return "Criptograma inválido."
	case errors.Is(err, ErrInvalidAESGCM):
		return "Não foi possível autenticar o PIN recebido."
	case errors.Is(err, ErrInvalidPIN):
		return "PIN incorreto."
	case errors.Is(err, ErrInvalidPINHash):
		return "Cadastro de autenticação do cartão inválido."
	case errors.Is(err, ErrInvalidPaymentKey):
		return "Falha na configuração de segurança do pagamento."
	default:
		return "Não foi possível processar o pagamento."
	}
}
