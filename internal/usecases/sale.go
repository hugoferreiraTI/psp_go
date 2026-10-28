package usecases

import (
	"errors"
	"fmt"
	"time"

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
	repo *repository.UserRepository
}

func NewSaleCaseDependency(repo *repository.UserRepository) *SaleCase {
	return &SaleCase{
		repo: repo,
	}
}

// SaleCard retorna a decisão da PSP após validar a transação e autenticar o PIN.
// keys deve conter as chaves compartilhadas com a máquina daquele usuário.
// Cada tentativa tem seu histórico. A aprovação e o débito são atômicos;
// qualquer saída reprovada finaliza o mesmo registro sem movimentar saldo.
func (s *SaleCase) SaleCard(saleCard model.CardPayment, keys PaymentKeys) (result model.AuthorizationResponse) {
	result.Status = model.StatusReproved
	result.PaymentType = saleCard.PaymentType
	fmt.Printf("[SaleCard] sale_id=%d início\n", saleCard.SaleId)
	defer func() {
		fmt.Printf("[SaleCard] sale_id=%d fim status=%s\n", saleCard.SaleId, result.Status)
	}()
	fmt.Printf("[SaleCard] sale_id=%d etapa=CreatePendingSale\n", saleCard.SaleId)
	transactionID, err := s.repo.CreatePendingSale(saleCard.SaleId)
	if err != nil {
		fmt.Printf("[SaleCard] sale_id=%d CreatePendingSale falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	defer func() {
		if result.Status == model.StatusReproved {
			fmt.Printf("[SaleCard] sale_id=%d etapa=UpdateSaleStatus status=REPROVED\n", saleCard.SaleId)
			if err := s.repo.UpdateSaleStatus(transactionID, result.Status); err != nil {
				fmt.Printf("[SaleCard] sale_id=%d UpdateSaleStatus falhou: %v\n", saleCard.SaleId, err)
			} else {
				fmt.Printf("[SaleCard] sale_id=%d histórico REPROVED salvo\n", saleCard.SaleId)
			}
		}
	}()

	fmt.Printf("[SaleCard] sale_id=%d etapa=DecodeTLV\n", saleCard.SaleId)
	decoded, err := parser.DecodeTLV(saleCard)
	if err != nil {
		fmt.Printf("[SaleCard] sale_id=%d DecodeTLV falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=formatar valor decimal\n", saleCard.SaleId)
	whole, fraction := decoded.AmountCents/100, decoded.AmountCents%100
	sign := ""
	if decoded.AmountCents < 0 {
		sign, whole, fraction = "-", -whole, -fraction
	}
	result.Amount = fmt.Sprintf("%s%d.%02d", sign, whole, fraction)
	// Preenche os metadados disponíveis também para vendas reprovadas.
	// A decisão sobre routing permanece após as validações estruturais e CMAC.
	var routingErr error
	fmt.Printf("[SaleCard] sale_id=%d etapa=VerifyPANRouting\n", saleCard.SaleId)
	result.CardNetwork, result.IssuerBank, routingErr = VerifyPANRouting(decoded.PAN)
	if routingErr != nil {
		fmt.Printf("[SaleCard] sale_id=%d VerifyPANRouting falhou: %v\n", saleCard.SaleId, routingErr)
	} else {
		fmt.Printf("[SaleCard] sale_id=%d VerifyPANRouting OK bandeira=%s emissor=%s\n", saleCard.SaleId, result.CardNetwork, result.IssuerBank)
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=UpdateSaleDetails\n", saleCard.SaleId)
	if err := s.repo.UpdateSaleDetails(transactionID, decoded.PAN, decoded.AmountCents, decoded.PaymentType, nil); err != nil {
		fmt.Printf("[SaleCard] sale_id=%d UpdateSaleDetails falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=VerifyTransaction\n", saleCard.SaleId)
	if err := securitysale.VerifyTransaction(decoded); err != nil {
		fmt.Printf("[SaleCard] sale_id=%d VerifyTransaction falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=VerifyCryptogram\n", saleCard.SaleId)
	if err := VerifyCryptogram(saleCard, keys.CMACKeyHex); err != nil {
		fmt.Printf("[SaleCard] sale_id=%d VerifyCryptogram falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	if routingErr != nil {
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=GetCardByPAN\n", saleCard.SaleId)
	card, err := s.repo.GetCardByPAN(decoded.PAN)
	if err != nil {
		fmt.Printf("[SaleCard] sale_id=%d GetCardByPAN falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=vincular cartão ao histórico\n", saleCard.SaleId)
	if err := s.repo.UpdateSaleDetails(transactionID, decoded.PAN, decoded.AmountCents, decoded.PaymentType, &card.ID); err != nil {
		fmt.Printf("[SaleCard] sale_id=%d vínculo do cartão falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=VerifyCardIdentity\n", saleCard.SaleId)
	if err := VerifyCardIdentity(decoded, card, result.CardNetwork, result.IssuerBank); err != nil {
		fmt.Printf("[SaleCard] sale_id=%d VerifyCardIdentity falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=VerifyDateExpiry\n", saleCard.SaleId)
	if err := VerifyDateExpiry(decoded.DateExpiry, card.DateExpiry, time.Now()); err != nil {
		fmt.Printf("[SaleCard] sale_id=%d VerifyDateExpiry falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=VerifyAESGCM\n", saleCard.SaleId)
	pin, err := VerifyAESGCM(saleCard.Emv.Pin.PIN, keys.GCMKeyHex)
	if err != nil {
		fmt.Printf("[SaleCard] sale_id=%d VerifyAESGCM falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=VerifyPIN\n", saleCard.SaleId)
	if err := VerifyPIN(pin, card.PINHash); err != nil {
		fmt.Printf("[SaleCard] sale_id=%d VerifyPIN falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=VerifyDebitBalance\n", saleCard.SaleId)
	if err := VerifyDebitBalance(card.Balance, decoded.AmountCents, decoded.PaymentType); err != nil {
		fmt.Printf("[SaleCard] sale_id=%d VerifyDebitBalance falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	fmt.Printf("[SaleCard] sale_id=%d etapa=ApproveSale\n", saleCard.SaleId)
	if err := s.repo.ApproveSale(transactionID, card.ID, decoded.AmountCents); err != nil {
		fmt.Printf("[SaleCard] sale_id=%d ApproveSale falhou: %v\n", saleCard.SaleId, err)
		return result
	}
	result.Status = model.StatusApproved
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
