package usecases

import (
	"errors"
	"log"
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
func (s *SaleCase) SaleCard(saleCard model.CardPayment, keys PaymentKeys) (status model.SaleStatus) {
	status = model.StatusReproved
	transactionID, err := s.repo.CreatePendingSale(saleCard.SaleId)
	if err != nil {
		log.Printf("falha ao criar histórico sale_id=%d: %v", saleCard.SaleId, err)
		return model.StatusReproved
	}
	defer func() {
		if status == model.StatusReproved {
			if err := s.repo.UpdateSaleStatus(transactionID, status); err != nil {
				log.Printf("falha ao registrar reprovação transaction_id=%d: %v", transactionID, err)
			}
		}
	}()

	decoded, err := parser.DecodeTLV(saleCard)
	if err != nil {
		return model.StatusReproved
	}
	if err := s.repo.UpdateSaleDetails(transactionID, decoded.PAN, decoded.AmountCents, decoded.PaymentType, nil); err != nil {
		log.Printf("falha ao registrar dados transaction_id=%d: %v", transactionID, err)
		return model.StatusReproved
	}
	if err := securitysale.VerifyTransaction(decoded); err != nil {
		return model.StatusReproved
	}
	if err := VerifyCryptogram(saleCard, keys.CMACKeyHex); err != nil {
		return model.StatusReproved
	}
	network, issuer, err := VerifyPANRouting(decoded.PAN)
	if err != nil {
		return model.StatusReproved
	}
	card, err := s.repo.GetCardByPAN(decoded.PAN)
	if err != nil {
		return model.StatusReproved
	}
	if err := s.repo.UpdateSaleDetails(transactionID, decoded.PAN, decoded.AmountCents, decoded.PaymentType, &card.ID); err != nil {
		log.Printf("falha ao vincular cartão transaction_id=%d: %v", transactionID, err)
		return model.StatusReproved
	}
	if err := VerifyCardIdentity(decoded, card, network, issuer); err != nil {
		return model.StatusReproved
	}
	if err := VerifyDateExpiry(decoded.DateExpiry, card.DateExpiry, time.Now()); err != nil {
		return model.StatusReproved
	}
	pin, err := VerifyAESGCM(saleCard.Emv.Pin.PIN, keys.GCMKeyHex)
	if err != nil {
		return model.StatusReproved
	}
	if err := VerifyPIN(pin, card.PINHash); err != nil {
		return model.StatusReproved
	}
	if err := VerifyDebitBalance(card.Balance, decoded.AmountCents, decoded.PaymentType); err != nil {
		log.Printf("débito reprovado sale_id=%d: %v", saleCard.SaleId, err)
		return model.StatusReproved
	}
	if err := s.repo.ApproveSale(transactionID, card.ID, decoded.AmountCents); err != nil {
		log.Printf("falha ao debitar e gravar venda sale_id=%d: %v", saleCard.SaleId, err)
		return model.StatusReproved
	}
	return model.StatusApproved
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
