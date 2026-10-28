package model

// DecodedCardPayment contém os dados após a interpretação do protocolo TLV.
// AmountOriginal preserva a representação usada pelo terminal no criptograma;
// AmountCents representa o mesmo valor em centavos, sem ponto flutuante.
type DecodedCardPayment struct {
	PAN                 string
	DateExpiry          string // YYMMDD; o ano pertence ao intervalo 2000–2099.
	TransactionCountry  string // Quatro dígitos, preservando zeros à esquerda.
	UnpredictableNumber [4]byte
	AmountOriginal      string
	AmountCents         int64
	Status              SaleStatus
	PaymentType         CardPaymentType
}
