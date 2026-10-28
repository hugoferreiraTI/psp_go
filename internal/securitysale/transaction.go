package securitysale

import (
	"errors"
	"time"

	"psp_go/internal/model"
)

var (
	ErrInvalidPANFormat          = errors.New("PAN deve conter de 12 a 19 dígitos ASCII")
	ErrInvalidDateExpiryFormat   = errors.New("validade deve ser uma data existente em YYMMDD")
	ErrInvalidTransactionCountry = errors.New("país da transação deve ser Brasil (0076)")
	ErrInvalidStatus             = errors.New("nova autorização deve receber status PENDING")
	ErrUnsupportedPaymentType    = errors.New("tipo de pagamento deve ser DEBIT")
)

// VerifyTransaction orquestra validações estruturais dos dados decodificados.
// Luhn/routing, comparação com o cadastro, expiração e saldo ficam nas etapas
// próprias. O tamanho do número imprevisível já foi conferido por DecodeTLV
// e é garantido aqui pelo tipo [4]byte; não há regra adicional sobre seu valor.
func VerifyTransaction(decoded model.DecodedCardPayment) error {
	if err := verifyPANFormat(decoded.PAN); err != nil {
		return err
	}
	if err := VerifyDateExpiryFormat(decoded.DateExpiry); err != nil {
		return err
	}
	if err := verifyTransactionCountry(decoded.TransactionCountry); err != nil {
		return err
	}
	if err := verifyStatus(decoded.Status); err != nil {
		return err
	}
	return verifyPaymentType(decoded.PaymentType)
}

func verifyPANFormat(pan string) error {
	if len(pan) < 12 || len(pan) > 19 || !isASCIIDigits(pan) {
		return ErrInvalidPANFormat
	}
	return nil
}

// VerifyDateExpiryFormat valida uma data existente no formato YYMMDD.
func VerifyDateExpiryFormat(expiry string) error {
	if len(expiry) != 6 || !isASCIIDigits(expiry) {
		return ErrInvalidDateExpiryFormat
	}
	// Mantém a convenção do MVP: YY corresponde a um ano de 2000 a 2099.
	if _, err := time.Parse("20060102", "20"+expiry); err != nil {
		return ErrInvalidDateExpiryFormat
	}
	return nil
}

func verifyTransactionCountry(country string) error {
	if country != "0076" {
		return ErrInvalidTransactionCountry
	}
	return nil
}

func verifyStatus(status model.SaleStatus) error {
	if status != model.StatusPending {
		return ErrInvalidStatus
	}
	return nil
}

func verifyPaymentType(paymentType model.CardPaymentType) error {
	if paymentType != model.Debit {
		return ErrUnsupportedPaymentType
	}
	return nil
}

func isASCIIDigits(value string) bool {
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
