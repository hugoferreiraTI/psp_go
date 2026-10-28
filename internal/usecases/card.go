package usecases

import (
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"

	"psp_go/internal/model"
	"psp_go/internal/repository"
	"psp_go/internal/securitysale"
)

var ErrInvalidPINFormat = errors.New("PIN deve conter de 4 a 6 dígitos ASCII")

// CreatCard recebe apenas PAN, PIN e validade como dados obrigatórios.
// Bandeira e emissor vêm do BIN; saldo, status e tipo são definidos pelo MVP.
func (s *SaleCase) CreatCard(card model.Card) (int, error) {
	if card.CardType != "" && card.CardType != model.Debit {
		return 0, repository.ErrUnsupportedPaymentType
	}
	network, issuer, err := VerifyPANRouting(card.PAN)
	if err != nil {
		return 0, err
	}
	if err := securitysale.VerifyDateExpiryFormat(card.DateExpiry); err != nil {
		return 0, err
	}
	if err := VerifyDateExpiry(card.DateExpiry, card.DateExpiry, time.Now()); err != nil {
		return 0, err
	}
	if len(card.PIN) < 4 || len(card.PIN) > 6 {
		return 0, ErrInvalidPINFormat
	}
	for _, digit := range card.PIN {
		if digit < '0' || digit > '9' {
			return 0, ErrInvalidPINFormat
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(card.PIN), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	card.ID = 0
	card.CardNetwork, card.IssuerBank = network, issuer
	card.PINHash, card.PIN = string(hash), ""
	card.CardType, card.Balance, card.Status = model.Debit, 0, "ACTIVE"
	return s.repo.CreatCard(card)
}

// AddValueInCard valida o cadastro antes de somar centavos ao saldo do cartão.
func (s *SaleCase) AddValueInCard(payment model.CardAddAmount) (int64, error) {
	if payment.TypeCard != model.Debit {
		return 0, repository.ErrUnsupportedPaymentType
	}
	if payment.Amount <= 0 {
		return 0, repository.ErrInvalidAmount
	}
	if _, _, err := VerifyPANRouting(payment.PAN); err != nil {
		return 0, err
	}
	if err := securitysale.VerifyDateExpiryFormat(payment.DateExpiry); err != nil {
		return 0, err
	}
	card, err := s.repo.GetCardByPAN(payment.PAN)
	if err != nil {
		return 0, err
	}
	if payment.PAN != card.PAN {
		return 0, ErrCardDataMismatch
	}
	if card.CardType != model.Debit {
		return 0, repository.ErrUnsupportedPaymentType
	}
	if err := VerifyDateExpiry(payment.DateExpiry, card.DateExpiry, time.Now()); err != nil {
		return 0, err
	}
	if err := VerifyPIN(payment.PIN, card.PINHash); err != nil {
		return 0, err
	}
	return s.repo.AddValue(card.ID, payment.Amount)
}
