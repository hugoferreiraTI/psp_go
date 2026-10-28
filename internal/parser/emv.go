package parser

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"psp_go/internal/model"
)

var (
	ErrInvalidTLV    = errors.New("TLV inválido")
	ErrInvalidAmount = errors.New("valor da venda deve ser decimal com duas casas e caber em int64 centavos")
)

// DecodeTLV interpreta os campos EMV de um CardPayment já desserializado.
// Confere o protocolo (tag, comprimento e BCD), sem consultar cadastro nem
// decidir Luhn, validade do cartão, país aceito, status ou tipo de pagamento.
// PIN e criptograma permanecem no payload original para suas verificações.
func DecodeTLV(payment model.CardPayment) (model.DecodedCardPayment, error) {
	pan, err := decodeBCD(payment.Emv.PAN, []byte{0x5A}, 6, 10, true)
	if err != nil {
		return model.DecodedCardPayment{}, err
	}
	expiry, err := decodeBCD(payment.Emv.DateExpiry, []byte{0x5F, 0x24}, 3, 3, false)
	if err != nil {
		return model.DecodedCardPayment{}, err
	}
	country, err := decodeBCD(payment.Emv.TransactionCountry, []byte{0x9F, 0x1A}, 2, 2, false)
	if err != nil {
		return model.DecodedCardPayment{}, err
	}
	number, err := tlvValue(payment.Emv.NumberUnpredictable, []byte{0x9F, 0x37}, 4, 4)
	if err != nil {
		return model.DecodedCardPayment{}, err
	}
	amount, err := amountToCents(payment.Amount)
	if err != nil {
		return model.DecodedCardPayment{}, err
	}

	decoded := model.DecodedCardPayment{
		PAN: pan, DateExpiry: expiry, TransactionCountry: country,
		AmountOriginal: payment.Amount, AmountCents: amount,
		Status: payment.Status, PaymentType: payment.PaymentType,
	}
	// Copia os bytes binários sem interpretá-los como BCD ou texto.
	copy(decoded.UnpredictableNumber[:], number)
	return decoded, nil
}

func tlvValue(tlv model.TLV, tag []byte, minLength, maxLength int) ([]byte, error) {
	if !bytes.Equal(tlv.Tag, tag) || tlv.Length != len(tlv.Value) ||
		tlv.Length < minLength || tlv.Length > maxLength {
		return nil, fmt.Errorf("%w: tag %X ausente ou com comprimento incompatível", ErrInvalidTLV, tag)
	}
	return tlv.Value, nil
}

func decodeBCD(tlv model.TLV, tag []byte, minLength, maxLength int, allowPadding bool) (string, error) {
	value, err := tlvValue(tlv, tag, minLength, maxLength)
	if err != nil {
		return "", err
	}
	text := hex.EncodeToString(value)
	if allowPadding {
		// Apenas o PAN pode ter um único nibble F de preenchimento no fim.
		text = strings.TrimSuffix(text, "f")
	}
	for _, digit := range text {
		if digit < '0' || digit > '9' {
			return "", fmt.Errorf("%w: tag %X contém BCD inválido", ErrInvalidTLV, tag)
		}
	}
	return text, nil
}

// Mantém o formato monetário já usado pelo terminal. Valores não positivos
// são convertidos normalmente; a autorização financeira decide sua aceitação.
func amountToCents(amount string) (int64, error) {
	whole, fraction, ok := strings.Cut(amount, ".")
	if !ok || len(fraction) != 2 {
		return 0, ErrInvalidAmount
	}
	if _, err := strconv.ParseInt(whole, 10, 64); err != nil {
		return 0, ErrInvalidAmount
	}
	cents, err := strconv.ParseInt(whole+fraction, 10, 64)
	if err != nil {
		return 0, ErrInvalidAmount
	}
	return cents, nil
}
