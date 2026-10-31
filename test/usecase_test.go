package test

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	confluent "github.com/confluentinc/confluent-kafka-go/kafka"
	"math"
	"os"
	"path/filepath"
	kafkaconfig "psp_go/internal/kafka"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/hugoferreiraTI/AES_CMAC_Go/cmac"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"

	"psp_go/internal/db"
	"psp_go/internal/model"
	"psp_go/internal/parser"
	"psp_go/internal/repository"
	"psp_go/internal/securitysale"
	"psp_go/internal/usecases"
)

// Cartão sintético no PostgreSQL: id=1, MASTERCARD/NUBANK, DEBIT, ACTIVE,
// validade 31/12/2031, saldo 0. O banco guarda somente o bcrypt do PIN.
const (
	manualCardPAN    = "5200500000000006"
	manualCardPIN    = "2468"
	manualCardExpiry = "311231"
)

func TestDecodeTLV(t *testing.T) {
	payment := newSecuredPaymentWithPIN(t, newPaymentTestKeys(t), manualCardPIN)
	decoded, err := parser.DecodeTLV(payment)
	if err != nil || decoded.PAN != manualCardPAN || decoded.DateExpiry != manualCardExpiry || decoded.TransactionCountry != "0076" || decoded.AmountOriginal != "25.33" || decoded.AmountCents != 2533 || decoded.UnpredictableNumber != [4]byte{0, 0x7C, 0x80, 0xFF} {
		t.Fatal("decode deve preservar PAN, validade, país, valor original e número imprevisível")
	}
	payment.Emv.PAN.Length--
	_, invalidErr := parser.DecodeTLV(payment)
	if !errors.Is(invalidErr, parser.ErrInvalidTLV) {
		t.Fatal("TLV com comprimento divergente deve falhar")
	}
	t.Logf("Teste aprovado, valor: %+v; erro para TLV inválido=%v", decoded, invalidErr)
}

func TestVerifyTransaction(t *testing.T) {
	valid := model.DecodedCardPayment{PAN: manualCardPAN, DateExpiry: manualCardExpiry, TransactionCountry: "0076", Status: model.StatusPending, PaymentType: model.Debit}
	for _, tt := range []struct {
		name   string
		change func(*model.DecodedCardPayment)
		err    error
	}{
		{"válida", func(p *model.DecodedCardPayment) {}, nil},
		{"país incorreto", func(p *model.DecodedCardPayment) { p.TransactionCountry = "0840" }, securitysale.ErrInvalidTransactionCountry},
		{"status recebido aprovado", func(p *model.DecodedCardPayment) { p.Status = model.StatusApproved }, securitysale.ErrInvalidStatus},
		{"crédito", func(p *model.DecodedCardPayment) { p.PaymentType = model.Credit }, nil},
		{"tipo inválido", func(p *model.DecodedCardPayment) { p.PaymentType = "PIX" }, securitysale.ErrUnsupportedPaymentType},
		{"data inexistente", func(p *model.DecodedCardPayment) { p.DateExpiry = "310230" }, securitysale.ErrInvalidDateExpiryFormat},
	} {
		t.Run(tt.name, func(t *testing.T) {
			decoded := valid
			tt.change(&decoded)
			err := securitysale.VerifyTransaction(decoded)
			if !errors.Is(err, tt.err) {
				t.Fatalf("erro=%v; esperado=%v", err, tt.err)
			}
			t.Logf("Teste aprovado, valor: %v", err)
		})
	}
}

func TestVerifyPANRouting(t *testing.T) {
	for _, tt := range []struct {
		pan     string
		network model.CardNetwork
		issuer  model.IssuerBank
		err     error
	}{
		{manualCardPAN, model.NetworkMastercard, model.IssuerNubank, nil},
		{"5201000000000006", model.NetworkMastercard, model.IssuerItau, nil},
		{"4000000000000002", model.NetworkVisa, model.IssuerNubank, nil},
		{"4001000000000001", model.NetworkVisa, model.IssuerBradesco, nil},
		{"5200500000000007", "", "", usecases.ErrInvalidPAN},
		{"4111111111111111", model.NetworkVisa, "", usecases.ErrUnknownIssuerBank},
	} {
		t.Run(tt.pan, func(t *testing.T) {
			network, issuer, err := usecases.VerifyPANRouting(tt.pan)
			if network != tt.network || issuer != tt.issuer || !errors.Is(err, tt.err) {
				t.Fatal("bandeira, emissor ou erro de routing inesperado")
			}
			t.Logf("Teste aprovado, valor: bandeira=%s, emissor=%s, erro=%v", network, issuer, err)
		})
	}
}

func TestVerifyDebitBalance(t *testing.T) {
	for _, tt := range []struct {
		name    string
		balance int64
		amount  int64
		kind    model.CardPaymentType
		err     error
	}{
		{"saldo suficiente", 10000, 2533, model.Debit, nil},
		{"saldo exato", 2533, 2533, model.Debit, nil},
		{"saldo zero", 0, 2533, model.Debit, repository.ErrInsufficientBalance},
		{"saldo insuficiente", 2532, 2533, model.Debit, repository.ErrInsufficientBalance},
		{"valor zero", 10000, 0, model.Debit, repository.ErrInvalidAmount},
		{"valor negativo", 10000, -1, model.Debit, repository.ErrInvalidAmount},
		{"crédito", 10000, 2533, model.Credit, repository.ErrUnsupportedPaymentType},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := usecases.VerifyDebitBalance(tt.balance, tt.amount, tt.kind)
			if !errors.Is(err, tt.err) {
				t.Fatalf("erro=%v; esperado=%v", err, tt.err)
			}
			t.Logf("Teste aprovado, valor: %v", err)
		})
	}
}

func TestVerifyCreditLimit(t *testing.T) {
	for _, tt := range []struct {
		name                string
		limit, used, amount int64
		paymentType         model.CardPaymentType
		wantErr             error
	}{
		{name: "368 mais 100 cabe em 1000", limit: 100000, used: 36800, amount: 10000, paymentType: model.Credit},
		{name: "total igual ao limite", limit: 46800, used: 36800, amount: 10000, paymentType: model.Credit},
		{name: "total acima do limite", limit: 46799, used: 36800, amount: 10000, paymentType: model.Credit, wantErr: repository.ErrInsufficientCreditLimit},
		{name: "limite esgotado", limit: 36800, used: 36800, amount: 1, paymentType: model.Credit, wantErr: repository.ErrInsufficientCreditLimit},
		{name: "valor zero", limit: 100000, amount: 0, paymentType: model.Credit, wantErr: repository.ErrInvalidAmount},
		{name: "valor negativo", limit: 100000, amount: -1, paymentType: model.Credit, wantErr: repository.ErrInvalidAmount},
		{name: "overflow bloqueado", limit: math.MaxInt64, used: math.MaxInt64 - 1, amount: 2, paymentType: model.Credit, wantErr: repository.ErrInsufficientCreditLimit},
		{name: "tipo incorreto", limit: 100000, amount: 1, paymentType: model.Debit, wantErr: repository.ErrUnsupportedPaymentType},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := usecases.VerifyCreditLimit(tt.limit, tt.used, tt.amount, tt.paymentType)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("erro=%v; esperado=%v", err, tt.wantErr)
			}
			t.Logf("Teste aprovado, valor: %v", err)
		})
	}
}

// A validação completa deve impedir qualquer UPDATE quando um dado divergir.
func TestAddValueInCard(t *testing.T) {
	valid := model.CardAddAmount{PAN: manualCardPAN, PIN: manualCardPIN, DateExpiry: manualCardExpiry, TypeCard: model.Debit, Amount: 2533}
	queryErr := errors.New("falha na consulta")
	updateErr := errors.New("falha na soma")
	for _, tt := range []struct {
		name      string
		change    func(*model.CardAddAmount, *model.Card)
		query     bool
		missing   bool
		queryErr  error
		updateErr error
		wantErr   error
	}{
		{name: "sucesso", query: true},
		{name: "PAN inválido", change: func(p *model.CardAddAmount, c *model.Card) { p.PAN = "123" }, wantErr: usecases.ErrInvalidPAN},
		{name: "data inexistente", change: func(p *model.CardAddAmount, c *model.Card) { p.DateExpiry = "310230" }, wantErr: securitysale.ErrInvalidDateExpiryFormat},
		{name: "crédito no JSON", change: func(p *model.CardAddAmount, c *model.Card) { p.TypeCard = model.Credit }, wantErr: repository.ErrUnsupportedPaymentType},
		{name: "valor zero", change: func(p *model.CardAddAmount, c *model.Card) { p.Amount = 0 }, wantErr: repository.ErrInvalidAmount},
		{name: "valor negativo", change: func(p *model.CardAddAmount, c *model.Card) { p.Amount = -1 }, wantErr: repository.ErrInvalidAmount},
		{name: "cartão ausente", query: true, missing: true, wantErr: repository.ErrCardNotFound},
		{name: "PAN divergente", query: true, change: func(p *model.CardAddAmount, c *model.Card) { c.PAN = "4000000000000002" }, wantErr: usecases.ErrCardDataMismatch},
		{name: "crédito no cadastro", query: true, change: func(p *model.CardAddAmount, c *model.Card) { c.CardType = model.Credit }, wantErr: repository.ErrUnsupportedPaymentType},
		{name: "validade divergente", query: true, change: func(p *model.CardAddAmount, c *model.Card) { p.DateExpiry = "321231" }, wantErr: usecases.ErrDateExpiryMismatch},
		{name: "cartão expirado", query: true, change: func(p *model.CardAddAmount, c *model.Card) { p.DateExpiry = "010101"; c.DateExpiry = p.DateExpiry }, wantErr: usecases.ErrCardExpired},
		{name: "PIN incorreto", query: true, change: func(p *model.CardAddAmount, c *model.Card) { p.PIN = "9999" }, wantErr: usecases.ErrInvalidPIN},
		{name: "hash ausente", query: true, change: func(p *model.CardAddAmount, c *model.Card) { c.PINHash = "" }, wantErr: usecases.ErrInvalidPINHash},
		{name: "erro na consulta", query: true, queryErr: queryErr, wantErr: queryErr},
		{name: "erro na atualização", query: true, updateErr: updateErr, wantErr: updateErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payment, card := valid, testCard(t)
			card.Balance = 10000
			if tt.change != nil {
				tt.change(&payment, card)
			}
			repo, mock := newRepositoryMock(t)
			if tt.query {
				query := mock.ExpectQuery(selectCardQuery).WithArgs(payment.PAN)
				if tt.queryErr != nil {
					query.WillReturnError(tt.queryErr)
				} else if tt.missing {
					query.WillReturnRows(cardRows(nil))
				} else {
					query.WillReturnRows(cardRows(card))
				}
			}
			if tt.wantErr == nil || tt.updateErr != nil {
				query := mock.ExpectQuery(addValueQuery).WithArgs(payment.Amount, card.ID)
				if tt.updateErr != nil {
					query.WillReturnError(tt.updateErr)
				} else {
					query.WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(int64(12533)))
				}
			}
			balance, err := usecases.NewSaleCaseDependency(repo).AddValueInCard(payment)
			wantBalance := int64(0)
			if tt.wantErr == nil {
				wantBalance = 12533
			}
			if balance != wantBalance || !errors.Is(err, tt.wantErr) {
				t.Fatalf("saldo=%d, erro=%v; esperado saldo=%d, erro=%v", balance, err, wantBalance, tt.wantErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			t.Logf("Teste aprovado, valor: saldo=%d centavos, erro=%v", balance, err)
		})
	}
}

func TestCreatCardUsecase(t *testing.T) {
	for _, tt := range []struct {
		name    string
		change  func(*model.Card)
		wantErr error
	}{
		{name: "cadastro com padrões do MVP"},
		{name: "ignora ID saldo status bandeira e emissor recebidos", change: func(c *model.Card) {
			c.ID = 99
			c.Balance = 99999
			c.Status = "BLOCKED"
			c.CardNetwork = model.NetworkVisa
			c.IssuerBank = model.IssuerItau
			c.PINHash = "hash fornecido pelo cliente"
		}},
		{name: "PIN de 5 dígitos", change: func(c *model.Card) { c.PIN = "02468" }},
		{name: "PIN de 6 dígitos", change: func(c *model.Card) { c.PIN = "012468" }},
		{name: "PAN inválido", change: func(c *model.Card) { c.PAN = "5200500000000007" }, wantErr: usecases.ErrInvalidPAN},
		{name: "BIN fora do MVP", change: func(c *model.Card) { c.PAN = "4111111111111111" }, wantErr: usecases.ErrUnknownIssuerBank},
		{name: "validade inválida", change: func(c *model.Card) { c.DateExpiry = "310230" }, wantErr: securitysale.ErrInvalidDateExpiryFormat},
		{name: "cartão expirado", change: func(c *model.Card) { c.DateExpiry = "010101" }, wantErr: usecases.ErrCardExpired},
		{name: "crédito bloqueado", change: func(c *model.Card) { c.CardType = model.Credit }, wantErr: repository.ErrUnsupportedPaymentType},
		{name: "PIN ausente mesmo com hash recebido", change: func(c *model.Card) { c.PIN = ""; c.PINHash = "hash fornecido pelo cliente" }, wantErr: usecases.ErrInvalidPINFormat},
		{name: "PIN curto", change: func(c *model.Card) { c.PIN = "123" }, wantErr: usecases.ErrInvalidPINFormat},
		{name: "PIN longo", change: func(c *model.Card) { c.PIN = "1234567" }, wantErr: usecases.ErrInvalidPINFormat},
		{name: "PIN com letras", change: func(c *model.Card) { c.PIN = "12a4" }, wantErr: usecases.ErrInvalidPINFormat},
		{name: "PIN com dígitos não ASCII", change: func(c *model.Card) { c.PIN = "１２" }, wantErr: usecases.ErrInvalidPINFormat},
	} {
		t.Run(tt.name, func(t *testing.T) {
			card := model.Card{PAN: manualCardPAN, PIN: manualCardPIN, DateExpiry: manualCardExpiry}
			if tt.change != nil {
				tt.change(&card)
			}
			repo, mock := newRepositoryMock(t)
			if tt.wantErr == nil {
				// O matcher comprova bcrypt com o PIN original e custo padrão.
				mock.ExpectQuery(existsPANQuery).WithArgs(card.PAN).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
				mock.ExpectQuery(insertCardQuery).WithArgs(card.PAN, "MASTERCARD", "NUBANK", "DEBIT", bcryptHashArgument{pin: card.PIN}, card.DateExpiry, int64(0), "ACTIVE").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
			}
			id, err := usecases.NewSaleCaseDependency(repo).CreatCard(card)
			wantID := 0
			if tt.wantErr == nil {
				wantID = 2
			}
			if id != wantID || !errors.Is(err, tt.wantErr) {
				t.Fatalf("card_id=%d, erro=%v; esperado card_id=%d, erro=%v", id, err, wantID, tt.wantErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			t.Logf("Teste aprovado, valor: card_id=%d, erro=%v", id, err)
		})
	}
}

// SaleCard retorna os dados que o handler serializa na resposta HTTP.
// O CMAC deixa de corresponder ao alterar Amount; o retorno mantém o valor sem float.
func TestSaleCardResponse(t *testing.T) {
	for _, tt := range []struct {
		amount    string
		cents     int64
		formatted string
	}{
		{amount: "00025.33", cents: 2533, formatted: "25.33"},
		{amount: "-0.01", cents: -1, formatted: "-0.01"},
		{amount: "92233720368547758.07", cents: 9223372036854775807, formatted: "92233720368547758.07"},
	} {
		t.Run(tt.amount, func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			mock.ExpectQuery(createPendingSaleQuery).WithArgs(900001, "PENDING").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(99))
			mock.ExpectExec(updateSaleDetailsQuery).WithArgs(manualCardPAN, tt.cents, "DEBIT", nil, 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(updateSaleStatusQuery).WithArgs("REPROVED", 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
			keys := newPaymentTestKeys(t)
			payment := newSecuredPaymentWithPIN(t, keys, manualCardPIN)
			payment.Amount = tt.amount
			result := usecases.NewSaleCaseDependency(repo).SaleCard(payment, keys)
			want := model.AuthorizationResponse{Status: model.StatusReproved, PaymentType: model.Debit, CardNetwork: model.NetworkMastercard, IssuerBank: model.IssuerNubank, Amount: tt.formatted, TimeNow: payment.TimeNow, Log: "Criptograma inválido."}
			if result != want {
				t.Fatal("SaleCard deve retornar a decisão com o valor decimal exato")
			}
			t.Logf("Teste aprovado, valor: %+v", result)
		})
	}
}

// Tipos desconhecidos não acessam cartões nem movimentam valores, mas têm histórico.
func TestSaleCardPaymentType(t *testing.T) {
	for _, paymentType := range []model.CardPaymentType{"PIX", ""} {
		t.Run(string(paymentType), func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			mock.ExpectQuery(createPendingSaleQuery).WithArgs(900001, "PENDING").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(99))
			mock.ExpectExec(updateSaleStatusQuery).WithArgs("REPROVED", 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
			payment := model.CardPayment{SaleId: 900001, PaymentType: paymentType, TimeNow: "2026-10-29T12:00:00-03:00"}
			result := usecases.NewSaleCaseDependency(repo).SaleCard(payment, usecases.PaymentKeys{})
			want := model.AuthorizationResponse{Status: model.StatusReproved, PaymentType: paymentType, TimeNow: payment.TimeNow, Log: "Tipo de pagamento não suportado."}
			if result != want {
				t.Fatal("tipo inválido deve ser reprovado antes de validar o cartão")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			t.Logf("Teste aprovado, valor: %+v", result)
		})
	}
}

func TestSaleCardInternalErrorLog(t *testing.T) {
	repo, mock := newRepositoryMock(t)
	// Erros externos podem conter dados privados; o retorno deve usar texto fixo.
	mock.ExpectQuery(createPendingSaleQuery).WithArgs(900001, "PENDING").WillReturnError(errors.New("PAN=5201000000000006 PIN=2468 chave=segredo"))
	result := usecases.NewSaleCaseDependency(repo).SaleCard(model.CardPayment{SaleId: 900001, PaymentType: model.Debit}, usecases.PaymentKeys{})
	if result.Status != model.StatusReproved || result.Log != "Não foi possível processar o pagamento." {
		t.Fatal("erro interno deve retornar motivo genérico sem dados privados")
	}
	t.Logf("Teste aprovado, valor: %s", result.Log)
}

// Restaura os JSONs e cadastra o cartão somente se ausente; não altera existentes.
// PSP_PREPARE_PAYMENT=1 go test ./test -run '^TestPrepareManualCardPayment$' -v -count=1
func TestPrepareManualCardPayment(t *testing.T) {
	if os.Getenv("PSP_PREPARE_PAYMENT") != "1" {
		t.Skip("preparação manual: PSP_PREPARE_PAYMENT=1")
	}
	keys := manualPaymentKeys(t)
	connection, err := db.ConnectDB()
	if err != nil {
		t.Fatal("falha na conexão ao PostgreSQL")
	}
	defer connection.Close()
	repo := repository.NewUserRepository(connection)
	card, err := repo.GetCardByPAN(manualCardPAN)
	if errors.Is(err, repository.ErrCardNotFound) {
		if _, err := repo.InsertCard(manualCardPAN, model.NetworkMastercard, model.IssuerNubank, model.Debit, manualCardExpiry, manualCardPIN); err != nil {
			t.Fatal("falha ao cadastrar cartão de teste")
		}
	} else if err != nil {
		t.Fatal("falha ao consultar cartão de teste")
	} else if card.CardNetwork != model.NetworkMastercard || card.IssuerBank != model.IssuerNubank || card.DateExpiry != manualCardExpiry || usecases.VerifyPIN(manualCardPIN, card.PINHash) != nil {
		t.Fatal("cartão existente diverge dos dados de teste; nenhum registro alterado")
	}
	if err := os.MkdirAll("testdata", 0755); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ file, pin string }{
		{"card_payment.json", manualCardPIN},
		{"card_payment_wrong_pin.json", "9999"},
	} {
		payload, err := json.MarshalIndent(newSecuredPaymentWithPIN(t, keys, fixture.pin), "", "  ")
		if err != nil {
			t.Fatal("falha ao gerar JSON")
		}
		if err := os.WriteFile(filepath.Join("testdata", fixture.file), append(payload, '\n'), 0644); err != nil {
			t.Fatal("falha ao salvar JSON")
		}
	}
	t.Log("Teste aprovado, valor: testdata/card_payment.json e testdata/card_payment_wrong_pin.json")
}

func manualPaymentKeys(t *testing.T) usecases.PaymentKeys {
	t.Helper()
	env, err := godotenv.Read("../.env")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal("falha ao ler .env")
	}
	cmacKey, gcmKey := os.Getenv("AES_CMAC_KEY"), os.Getenv("AES_GCM")
	if cmacKey == "" {
		cmacKey = env["AES_CMAC_KEY"]
	}
	if gcmKey == "" {
		gcmKey = env["AES_GCM"]
	}
	if cmacKey == "" || gcmKey == "" {
		t.Fatal("configure AES_CMAC_KEY e AES_GCM")
	}
	return usecases.PaymentKeys{CMACKeyHex: cmacKey, GCMKeyHex: gcmKey}
}

func newPaymentTestKeys(t *testing.T) usecases.PaymentKeys {
	t.Helper()
	var keys [32]byte
	if _, err := rand.Read(keys[:]); err != nil {
		t.Fatal("falha ao gerar chaves de teste")
	}
	return usecases.PaymentKeys{CMACKeyHex: hex.EncodeToString(keys[:16]), GCMKeyHex: hex.EncodeToString(keys[16:])}
}

func testCard(t *testing.T) *model.Card {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(manualCardPIN), bcrypt.MinCost)
	if err != nil {
		t.Fatal("falha ao criar hash de teste")
	}
	return &model.Card{ID: 1, PAN: manualCardPAN, CardNetwork: model.NetworkMastercard, IssuerBank: model.IssuerNubank, PINHash: string(hash), DateExpiry: manualCardExpiry, CardType: model.Debit}
}

// Reproduz apenas a montagem do payload: BCD, CMAC e AES-GCM do terminal.
func newSecuredPaymentWithPIN(t *testing.T, keys usecases.PaymentKeys, pin string) model.CardPayment {
	t.Helper()
	pan, err := hex.DecodeString(manualCardPAN)
	if err != nil {
		t.Fatal("PAN de teste inválido")
	}
	number := [4]byte{0, 0x7C, 0x80, 0xFF}
	payment := model.CardPayment{
		SaleId: 900001, Amount: "25.33", TimeNow: "2026-10-26T12:00:00-03:00", Status: model.StatusPending, PaymentType: model.Debit,
		Emv: model.EmvTlv{
			PAN:                 model.TLV{Tag: []byte{0x5A}, Length: len(pan), Value: pan},
			DateExpiry:          model.TLV{Tag: []byte{0x5F, 0x24}, Length: 3, Value: []byte{0x31, 0x12, 0x31}},
			TransactionCountry:  model.TLV{Tag: []byte{0x9F, 0x1A}, Length: 2, Value: []byte{0x00, 0x76}},
			NumberUnpredictable: model.TLV{Tag: []byte{0x9F, 0x37}, Length: 4, Value: number[:]},
		},
	}
	macHex, err := cmac.Generate(keys.CMACKeyHex, fmt.Sprintf("%s|%s|%s|%s", payment.Status, payment.Amount, payment.TimeNow, number))
	if err != nil {
		t.Fatal("falha ao gerar CMAC de teste")
	}
	mac, err := hex.DecodeString(macHex)
	if err != nil {
		t.Fatal("CMAC de teste inválido")
	}
	payment.Emv.Cryptogram = model.TLV{Tag: []byte{0x9F, 0x26}, Length: len(mac), Value: mac}
	key, err := hex.DecodeString(keys.GCMKeyHex)
	if err != nil {
		t.Fatal("chave GCM inválida")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal("chave GCM inválida")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal("falha ao configurar GCM")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal("falha ao gerar nonce")
	}
	payment.Emv.Pin = model.PIN{PIN: hex.EncodeToString(gcm.Seal(nonce, nonce, []byte(pin), nil)), CVM: model.CVMOnlinePIN}
	return payment
}

// Broker Kafka em memória: verifica eventos reais sem acessar Kafka/PostgreSQL locais.
func TestKafkaAuthorizationEvents(t *testing.T) {
	cluster, err := confluent.NewMockCluster(1)
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	t.Setenv("KAFKA_BOOTSTRAP_SERVERS", cluster.BootstrapServers())
	producer, err := kafkaconfig.NewKafkaProducer()
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	publisher := kafkaconfig.NewProducerCaseDependency(producer)
	keys := newPaymentTestKeys(t)
	expected := map[int][]model.PaymentKafka{}
	cases := []struct {
		kind                             model.CardPaymentType
		approved, invalidTLV, createFail bool
	}{
		{kind: model.Debit, approved: true}, {kind: model.Debit}, {kind: model.Credit, approved: true}, {kind: model.Credit},
		{kind: model.Debit, invalidTLV: true}, {kind: model.Credit, createFail: true},
	}
	for index, tt := range cases {
		repo, mock := newRepositoryMock(t)
		payment := newSecuredPaymentWithPIN(t, keys, manualCardPIN)
		payment.SaleId = 900100 + index
		payment.PaymentType = tt.kind
		if !tt.approved && !tt.invalidTLV && !tt.createFail {
			payment = newSecuredPaymentWithPIN(t, keys, "9999")
			payment.SaleId = 900100 + index
			payment.PaymentType = tt.kind
		}
		pending := mock.ExpectQuery(createPendingSaleQuery).WithArgs(payment.SaleId, "PENDING")
		if tt.createFail {
			pending.WillReturnError(errors.New("banco indisponível"))
		} else {
			pending.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(99))
		}
		if tt.invalidTLV {
			payment.Emv.PAN.Length--
		}
		if !tt.invalidTLV && !tt.createFail {
			mock.ExpectExec(updateSaleDetailsQuery).WithArgs(manualCardPAN, int64(2533), string(tt.kind), nil, 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
			card := testCard(t)
			card.CardType = tt.kind
			card.Balance = 10000
			card.Limit = 10000
			selectQuery, rows := selectCardQuery, cardRows(card)
			if tt.kind == model.Credit {
				selectQuery, rows = selectCreditCardQuery, creditCardRows(card)
			}
			mock.ExpectQuery(selectQuery).WithArgs(manualCardPAN).WillReturnRows(rows)
			mock.ExpectExec(updateSaleDetailsQuery).WithArgs(manualCardPAN, int64(2533), string(tt.kind), 1, 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
			if tt.approved {
				movement := debitCardQuery
				if tt.kind == model.Credit {
					movement = chargeCreditCardQuery
				}
				mock.ExpectBegin()
				mock.ExpectExec(movement).WithArgs(int64(2533), 1).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(approveSaleQuery).WithArgs("APPROVED", 99, "PENDING", 1, int64(2533), string(tt.kind)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
		}
		if !tt.approved && !tt.createFail {
			mock.ExpectExec(updateSaleStatusQuery).WithArgs("REPROVED", 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
		}
		result := usecases.NewSaleCaseDependency(repo, publisher).SaleCard(payment, keys)
		wantStatus := model.StatusReproved
		if tt.approved {
			wantStatus = model.StatusApproved
		}
		if result.Status != wantStatus {
			t.Fatalf("decisão inesperada: %s, %s", result.Status, result.Log)
		}
		expected[payment.SaleId] = []model.PaymentKafka{
			{SaleId: payment.SaleId, Value: "25.33", PaymentType: tt.kind, Status: model.StatusPending},
			{SaleId: payment.SaleId, Value: "25.33", PaymentType: tt.kind, Status: wantStatus},
		}
	}
	if remaining := producer.Flush(5000); remaining != 0 {
		t.Fatalf("eventos sem entrega: %d", remaining)
	}
	consumer, err := confluent.NewConsumer(&confluent.ConfigMap{"bootstrap.servers": cluster.BootstrapServers(), "group.id": "psp-event-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	topic := kafkaconfig.Topic
	metadata, err := consumer.GetMetadata(&topic, false, 5000)
	if err != nil {
		t.Fatal(err)
	}
	var partitions []confluent.TopicPartition
	for _, part := range metadata.Topics[topic].Partitions {
		partitions = append(partitions, confluent.TopicPartition{Topic: &topic, Partition: part.ID, Offset: confluent.OffsetBeginning})
	}
	if err := consumer.Assign(partitions); err != nil {
		t.Fatal(err)
	}
	received := map[int]int{}
	for total, deadline := 0, time.Now().Add(10*time.Second); total < len(cases)*2 && time.Now().Before(deadline); {
		message, err := consumer.ReadMessage(100 * time.Millisecond)
		if err != nil {
			continue
		}
		var event model.PaymentKafka
		if err := json.Unmarshal(message.Value, &event); err != nil {
			t.Fatal(err)
		}
		sequence := received[event.SaleId]
		pair, ok := expected[event.SaleId]
		if !ok || sequence >= len(pair) || event != pair[sequence] || string(message.Key) != fmt.Sprint(event.SaleId) {
			t.Fatalf("evento fora de ordem ou divergente: %+v", event)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(message.Value, &fields) != nil || len(fields) != 4 {
			t.Fatal("evento deve conter apenas os quatro metadados")
		}
		for _, secret := range []string{manualCardPAN, manualCardPIN, keys.CMACKeyHex, keys.GCMKeyHex} {
			if strings.Contains(string(message.Value), secret) {
				t.Fatal("evento contém informação sensível")
			}
		}
		received[event.SaleId]++
		total++
	}
	for id := range expected {
		if received[id] != 2 {
			t.Fatalf("sale_id=%d recebeu %d eventos; esperado 2", id, received[id])
		}
	}
	t.Log("Teste aprovado, valor: PENDING seguido de APPROVED/REPROVED, débito e crédito, inclusive saídas antecipadas")
}
