package model

// TLV contém a tag, o comprimento do valor em bytes e o valor binário.
// Ao usar encoding/json, Tag e Value são transportados como strings Base64.
type TLV struct {
	Tag    []byte
	Length int
	Value  []byte
}

// tags EMV in TLV default.
type EmvTlv struct {
	PAN                 TLV //Tag EMV: 5A
	DateExpiry          TLV //Tag EMV: 5F24
	TransactionCountry  TLV //Tag EMV: 9F1A
	Pin                 PIN
	NumberUnpredictable TLV //Tag EMV: 9F37
	Cryptogram          TLV //Tag EMV: 9f26
}

// tags EMV minimal IN json
type EmvTags struct {
	PAN                string `json:"PAN" binding:"required, max=19"`          //Tag EMV: 5A
	DateExpiry         string `json:"dateExpirity" binding:"required, max=6"`  //Tag EMV: 5F24
	TransactionCountry string `json:"Country"  binding:"required,min=3,max=3"` //Tag EMV: 9F1A
	Pin                PIN    `json:"Pin" binding:"required,min=4,max=6"`      //Tratamento proprio
}

type PIN struct {
	PIN string
	CVM CVM
}

type CVM string

const (
	CVMOnlinePIN CVM = "ONLINE_PIN"
)

type CardPaymentType string

const (
	Debit  CardPaymentType = "DEBIT"
	Credit CardPaymentType = "CREDIT"
)

// the cryptograma use all data of cardpay, for crypotografe and in bank compare the values.
type Cryptogram struct {
	StatusCard               SaleStatus //Status the sale
	AmountCrypt              string     //Amount value
	TimeNowCrypt             string     //Time of sale
	NumberUnpredictableCrypt [4]byte    //hash generated in terminal
}

type CardPayment struct {
	SaleId      int
	TimeNow     string
	Amount      string
	Status      SaleStatus
	PaymentType CardPaymentType
	Emv         EmvTlv
}

// CardAddAmount recebe o PIN em memória, validade YYMMDD e valor em centavos.
type CardAddAmount struct {
	PAN        string          `json:"PAN" binding:"required"`
	PIN        string          `json:"PIN" binding:"required"`
	DateExpiry string          `json:"DateExpiry" binding:"required"`
	TypeCard   CardPaymentType `json:"TypeCard" binding:"required"`
	Amount     int64           `json:"Amount" binding:"required"`
}

// initialize the sale
type CardSale struct {
	PaymentType CardPaymentType
	Amount      string
	SaleId      int
	TimeNow     string
	Status      SaleStatus
}

type Card struct {
	ID          int
	PAN         string
	CardNetwork CardNetwork
	IssuerBank  IssuerBank
	PIN         string `json:"PIN,omitempty"` // Somente entrada do cadastro; nunca persistido ou retornado.
	PINHash     string `json:"-"`             // Somente hash bcrypt; nunca o PIN em texto.
	DateExpiry  string // YYMMDD, mesmo formato da tag EMV 5F24.
	Balance     int64
	CardType    CardPaymentType
	Status      string
}

type CardNetwork string
type IssuerBank string

const (
	NetworkVisa       CardNetwork = "VISA"
	NetworkMastercard CardNetwork = "MASTERCARD"

	IssuerNubank   IssuerBank = "NUBANK"
	IssuerItau     IssuerBank = "ITAU"
	IssuerBradesco IssuerBank = "BRADESCO"
)
