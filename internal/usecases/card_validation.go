package usecases

import (
	"errors"
	"time"

	"psp_go/internal/model"
	"psp_go/internal/repository"
)

var (
	ErrDateExpiryMismatch = errors.New("data de expiração diverge do cartão cadastrado")
	ErrCardExpired        = errors.New("cartão expirado")
)

// VerifyCardIdentity compara apenas PAN, bandeira e emissor com o cadastro.
func VerifyCardIdentity(decoded model.DecodedCardPayment, card model.Card, network model.CardNetwork, issuer model.IssuerBank) error {
	if decoded.PAN != card.PAN || network != card.CardNetwork || issuer != card.IssuerBank {
		return ErrCardDataMismatch
	}
	return nil
}

// VerifyDateExpiry compara com o cadastro e aceita o cartão até o fim do dia
// de expiração no Brasil. value já passou por DecodeTLV e VerifyTransaction.
// now permite testar os limites sem alterar o relógio.
func VerifyDateExpiry(value, stored string, now time.Time) error {
	if value != stored {
		return ErrDateExpiryMismatch
	}
	location, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		return err
	}
	if "20"+value < now.In(location).Format("20060102") {
		return ErrCardExpired
	}
	return nil
}

// VerifyDebitBalance valida a fotografia do saldo. O repository repete a
// condição atomicamente no UPDATE, pois o saldo pode mudar após esta leitura.
func VerifyDebitBalance(balance, amount int64, paymentType model.CardPaymentType) error {
	if paymentType != model.Debit {
		return repository.ErrUnsupportedPaymentType
	}
	if amount <= 0 {
		return repository.ErrInvalidAmount
	}
	if balance < amount {
		return repository.ErrInsufficientBalance
	}
	return nil
}
