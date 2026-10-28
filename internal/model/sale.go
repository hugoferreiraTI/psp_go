package model

type SaleStatus string

const (
	StatusPending  SaleStatus = "PENDING"
	StatusApproved SaleStatus = "APPROVED"
	StatusExpired  SaleStatus = "EXPIRED"
	StatusReproved SaleStatus = "REPROVED"
)

