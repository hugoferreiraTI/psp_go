package model

// AuthorizationResponse é a decisão devolvida ao request de autorização.
type AuthorizationResponse struct {
	Status  SaleStatus `json:"status"`
	Message string     `json:"message"`
}

// AuthorizationResult transporta somente o ID da venda e a decisão já tomada.
// Mantém SaleId e Status com os nomes usados pelo protocolo da Card Machine.
type AuthorizationResult struct {
	SaleId int        `json:"SaleId"`
	Status SaleStatus `json:"Status"`
}
