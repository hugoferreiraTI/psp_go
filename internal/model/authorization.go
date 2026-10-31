package model

// AuthorizationResponse é devolvida na própria requisição HTTP da Card Machine.
// Amount usa decimal com duas casas, calculado a partir dos centavos, sem float.
// Dados que não puderem ser determinados em uma reprovação ficam vazios.
type AuthorizationResponse struct {
	Status      SaleStatus      `json:"status"`
	PaymentType CardPaymentType `json:"payment_type"`
	CardNetwork CardNetwork     `json:"card_network"`
	IssuerBank  IssuerBank      `json:"issuer_bank"`
	Amount      string          `json:"amount"`
	TimeNow     string          `json:"time_now"` // Data e hora recebidas da Card Machine.
	Log         string          `json:"log"`      // Motivo seguro da reprovação; vazio quando aprovado.
}
