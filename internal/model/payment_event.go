package model

// PaymentKafka contém somente os metadados públicos da tentativa de autorização.
// Value mantém o valor decimal recebido; não contém centavos em ponto flutuante.
type PaymentKafka struct {
	SaleId      int             `json:"sale_id"`
	Value       string          `json:"amount"`
	PaymentType CardPaymentType `json:"payment_type"`
	Status      SaleStatus      `json:"status"`
}
