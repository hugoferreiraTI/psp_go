package test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"

	"psp_go/internal/db"
	"psp_go/internal/handler"
	"psp_go/internal/model"
	"psp_go/internal/parser"
	"psp_go/internal/repository"
	"psp_go/internal/usecases"
)

func TestAuthorizeCardInvalidJSON(t *testing.T) {
	router := gin.New()
	cardHandler := handler.NewCardHandler(nil, usecases.PaymentKeys{})
	router.POST("/authorize/card", cardHandler.AuthorizeCard)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/authorize/card", bytes.NewBufferString("{")))
	var body map[string]string
	if json.Unmarshal(recorder.Body.Bytes(), &body) != nil || recorder.Code != http.StatusBadRequest || body["error"] != "invalid payload" || body["message"] == "" {
		t.Fatal("esperado HTTP 400 com error e message")
	}
	t.Logf("Teste aprovado, valor: HTTP %d, error=%s", recorder.Code, body["error"])
}

func TestAddValueHTTP(t *testing.T) {
	for _, tt := range []struct {
		name      string
		payload   string
		query     bool
		update    bool
		updateErr bool
		wantCode  int
	}{
		{name: "sucesso", payload: `{"PAN":"5200500000000006","PIN":"2468","DateExpiry":"311231","TypeCard":"DEBIT","Amount":2533}`, query: true, update: true, wantCode: http.StatusOK},
		{name: "JSON inválido", payload: `{`, wantCode: http.StatusBadRequest},
		{name: "campos ausentes", payload: `{}`, wantCode: http.StatusBadRequest},
		{name: "valor decimal", payload: `{"PAN":"5200500000000006","PIN":"2468","DateExpiry":"311231","TypeCard":"DEBIT","Amount":25.33}`, wantCode: http.StatusBadRequest},
		{name: "PIN incorreto", payload: `{"PAN":"5200500000000006","PIN":"9999","DateExpiry":"311231","TypeCard":"DEBIT","Amount":2533}`, query: true, wantCode: http.StatusBadRequest},
		{name: "erro na atualização", payload: `{"PAN":"5200500000000006","PIN":"2468","DateExpiry":"311231","TypeCard":"DEBIT","Amount":2533}`, query: true, update: true, updateErr: true, wantCode: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			if tt.query {
				mock.ExpectQuery(selectCardQuery).WithArgs(manualCardPAN).WillReturnRows(cardRows(testCard(t)))
			}
			if tt.update {
				query := mock.ExpectQuery(addValueQuery).WithArgs(int64(2533), 1)
				if tt.updateErr {
					query.WillReturnError(errors.New("falha no UPDATE"))
				} else {
					query.WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(int64(2533)))
				}
			}
			router := gin.New()
			cardHandler := handler.NewCardHandler(usecases.NewSaleCaseDependency(repo), usecases.PaymentKeys{})
			router.POST("/card/add-value", cardHandler.AddValueInCard)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/card/add-value", bytes.NewBufferString(tt.payload)))
			var body struct {
				Error   string `json:"error"`
				Message string `json:"message"`
				Balance int64  `json:"balance"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal("resposta deve ser JSON")
			}
			if recorder.Code != tt.wantCode || body.Message == "" {
				t.Fatalf("HTTP %d; esperado %d e message", recorder.Code, tt.wantCode)
			}
			if tt.wantCode == http.StatusOK {
				if body.Error != "" || body.Balance != 2533 {
					t.Fatal("esperado saldo atualizado em centavos")
				}
			} else if body.Error == "" {
				t.Fatal("erro deve conter error e message")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			t.Logf("Teste aprovado, valor: HTTP %d, saldo=%d centavos", recorder.Code, body.Balance)
		})
	}
}

func TestCreatCardHTTP(t *testing.T) {
	for _, tt := range []struct {
		name      string
		payload   string
		insert    bool
		duplicate bool
		wantCode  int
	}{
		{name: "cadastro", payload: `{"PAN":"5200500000000006","PIN":"2468","DateExpiry":"311231"}`, insert: true, wantCode: http.StatusCreated},
		{name: "JSON inválido", payload: `{`, wantCode: http.StatusBadRequest},
		{name: "PIN numérico em vez de string", payload: `{"PAN":"5200500000000006","PIN":2468,"DateExpiry":"311231"}`, wantCode: http.StatusBadRequest},
		{name: "PIN ausente", payload: `{"PAN":"5200500000000006","PINHash":"não aceitar hash externo","DateExpiry":"311231"}`, wantCode: http.StatusBadRequest},
		{name: "PAN duplicado", payload: `{"PAN":"5200500000000006","PIN":"2468","DateExpiry":"311231"}`, insert: true, duplicate: true, wantCode: http.StatusConflict},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			if tt.insert {
				mock.ExpectQuery(existsPANQuery).WithArgs(manualCardPAN).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tt.duplicate))
			}
			if tt.insert && !tt.duplicate {
				query := mock.ExpectQuery(insertCardQuery).WithArgs(manualCardPAN, "MASTERCARD", "NUBANK", "DEBIT", bcryptHashArgument{}, manualCardExpiry, int64(0), "ACTIVE")
				query.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
			}
			router := gin.New()
			cardHandler := handler.NewCardHandler(usecases.NewSaleCaseDependency(repo), usecases.PaymentKeys{})
			router.POST("/card/create", cardHandler.CreatCard)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/card/create", bytes.NewBufferString(tt.payload)))
			var body map[string]json.RawMessage
			if json.Unmarshal(recorder.Body.Bytes(), &body) != nil || recorder.Code != tt.wantCode || len(body["message"]) == 0 {
				t.Fatalf("esperado HTTP %d e message; recebido HTTP %d", tt.wantCode, recorder.Code)
			}
			if _, ok := body["PIN"]; ok {
				t.Fatal("a resposta não deve incluir PIN")
			}
			if _, ok := body["PINHash"]; ok {
				t.Fatal("a resposta não deve incluir o hash")
			}
			if tt.wantCode == http.StatusCreated {
				if string(body["card_id"]) != "2" || len(body["error"]) != 0 {
					t.Fatal("esperado somente confirmação do cartão criado")
				}
			} else if len(body["error"]) == 0 {
				t.Fatal("falha deve conter error e message")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			t.Logf("Teste aprovado, valor: HTTP %d, card_id=%s", recorder.Code, body["card_id"])
		})
	}
}

// A resposta da própria requisição contém a decisão e os dados da venda.
func TestAuthorizeCard(t *testing.T) {
	rowsForType := func(card *model.Card, paymentType model.CardPaymentType) *sqlmock.Rows {
		if paymentType == model.Credit {
			return creditCardRows(card)
		}
		return cardRows(card)
	}
	keys := newPaymentTestKeys(t)
	for _, paymentType := range []model.CardPaymentType{model.Debit, model.Credit} {
		t.Run(string(paymentType), func(t *testing.T) {
			selectQuery, detailsQuery, movementQuery, approveQuery := selectCardQuery, updateSaleDetailsQuery, debitCardQuery, approveSaleQuery
			if paymentType == model.Credit {
				selectQuery, detailsQuery, movementQuery, approveQuery = selectCreditCardQuery, updateCreditSaleDetailsQuery, chargeCreditCardQuery, approveCreditSaleQuery
			}

			for _, tt := range []struct {
				name       string
				pin        string
				want       model.SaleStatus
				saveErr    error
				balance    int64
				changed    bool
				invalidTLV bool
				invalidMAC bool
				missing    bool
				invalidGCM bool
				expired    bool
				cardChange func(*model.Card)
				wantLog    string
			}{
				{name: "aprovado", pin: manualCardPIN, want: model.StatusApproved, balance: 10000},
				{name: "TLV inválido também tem histórico", wantLog: "Dados EMV inválidos.", pin: manualCardPIN, want: model.StatusReproved, invalidTLV: true},
				{name: "CMAC inválido também tem histórico", wantLog: "Criptograma inválido.", pin: manualCardPIN, want: model.StatusReproved, invalidMAC: true},
				{name: "cartão não encontrado também tem histórico", wantLog: "Cartão inexistente para este tipo de pagamento.", pin: manualCardPIN, want: model.StatusReproved, missing: true},
				{name: "PIN errado", wantLog: "PIN incorreto.", pin: "9999", want: model.StatusReproved, balance: 10000},
				{name: "hash ausente", wantLog: "Cadastro de autenticação do cartão inválido.", pin: manualCardPIN, want: model.StatusReproved, balance: 10000, cardChange: func(c *model.Card) { c.PINHash = "" }},
				{name: "PAN divergente", wantLog: "Dados do cartão divergentes do cadastro.", pin: manualCardPIN, want: model.StatusReproved, balance: 10000, cardChange: func(c *model.Card) { c.PAN = "4000000000000002" }},
				{name: "bandeira divergente", wantLog: "Dados do cartão divergentes do cadastro.", pin: manualCardPIN, want: model.StatusReproved, balance: 10000, cardChange: func(c *model.Card) { c.CardNetwork = model.NetworkVisa }},
				{name: "emissor divergente", wantLog: "Dados do cartão divergentes do cadastro.", pin: manualCardPIN, want: model.StatusReproved, balance: 10000, cardChange: func(c *model.Card) { c.IssuerBank = model.IssuerItau }},
				{name: "validade divergente", wantLog: "Validade divergente do cadastro.", pin: manualCardPIN, want: model.StatusReproved, balance: 10000, cardChange: func(c *model.Card) { c.DateExpiry = "321231" }},
				{name: "cartão expirado", wantLog: "Cartão expirado.", pin: manualCardPIN, want: model.StatusReproved, balance: 10000, expired: true, cardChange: func(c *model.Card) { c.DateExpiry = "010101" }},
				{name: "tipo cadastrado divergente", wantLog: "Tipo de cartão divergente do pagamento.", pin: manualCardPIN, want: model.StatusReproved, balance: 10000, cardChange: func(c *model.Card) {
					if c.CardType == model.Debit {
						c.CardType = model.Credit
					} else {
						c.CardType = model.Debit
					}
				}},
				{name: "AES-GCM inválido", wantLog: "Não foi possível autenticar o PIN recebido.", pin: manualCardPIN, want: model.StatusReproved, balance: 10000, invalidGCM: true},
				{name: "saldo zero", wantLog: "Saldo insuficiente.", pin: manualCardPIN, want: model.StatusReproved, balance: 0},
				{name: "saldo insuficiente", wantLog: "Saldo insuficiente.", pin: manualCardPIN, want: model.StatusReproved, balance: 2532},
				{name: "saldo exato", pin: manualCardPIN, want: model.StatusApproved, balance: 2533},
				{name: "saldo mudou após consulta", wantLog: "Saldo insuficiente.", pin: manualCardPIN, want: model.StatusReproved, balance: 10000, changed: true},
				{name: "falha ao gravar", wantLog: "Não foi possível processar o pagamento.", pin: manualCardPIN, want: model.StatusReproved, saveErr: errors.New("erro privado PAN=5200500000000006 PIN=2468 chave=segredo"), balance: 10000},
			} {
				t.Run(tt.name, func(t *testing.T) {
					repo, mock := newRepositoryMock(t)
					mock.ExpectQuery(createPendingSaleQuery).WithArgs(900001, "PENDING").
						WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(99))
					if !tt.invalidTLV {
						mock.ExpectExec(detailsQuery).WithArgs(manualCardPAN, int64(2533), string(paymentType), nil, 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
						if !tt.invalidMAC {
							card := testCard(t)
							card.Balance = tt.balance
							card.CardType = paymentType
							card.Limit, card.Amount = 36800+tt.balance, 36800
							if tt.cardChange != nil {
								tt.cardChange(card)
							}
							if tt.missing {
								mock.ExpectQuery(selectQuery).WithArgs(manualCardPAN).WillReturnRows(rowsForType(nil, paymentType))
							} else {
								mock.ExpectQuery(selectQuery).WithArgs(manualCardPAN).WillReturnRows(rowsForType(card, paymentType))
								mock.ExpectExec(detailsQuery).WithArgs(manualCardPAN, int64(2533), string(paymentType), 1, 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
								if tt.want == model.StatusApproved || tt.changed || tt.saveErr != nil {
									mock.ExpectBegin()
									rows := int64(1)
									if tt.changed {
										rows = 0
									}
									mock.ExpectExec(movementQuery).WithArgs(int64(2533), 1).WillReturnResult(sqlmock.NewResult(0, rows))
									if rows > 0 {
										update := mock.ExpectExec(approveQuery).WithArgs("APPROVED", 99, "PENDING", 1, int64(2533), string(paymentType))
										if tt.saveErr != nil {
											update.WillReturnError(tt.saveErr)
										} else {
											update.WillReturnResult(sqlmock.NewResult(0, 1))
										}
									}
									if rows == 0 || tt.saveErr != nil {
										mock.ExpectRollback()
									} else {
										mock.ExpectCommit()
									}
								}
							}
						}
					}
					if tt.want == model.StatusReproved {
						mock.ExpectExec(updateSaleStatusQuery).WithArgs("REPROVED", 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
					}
					router := gin.New()
					cardHandler := handler.NewCardHandler(usecases.NewSaleCaseDependency(repo), keys)
					router.POST("/authorize/card", cardHandler.AuthorizeCard)
					payment := newSecuredPaymentWithPIN(t, keys, tt.pin)
					payment.PaymentType = paymentType
					if tt.invalidGCM {
						payment.Emv.Pin.PIN = "00"
					}
					if tt.expired {
						payment.Emv.DateExpiry.Value = []byte{0x01, 0x01, 0x01}
					}
					if tt.invalidTLV {
						payment.Emv.PAN.Length--
					}
					if tt.invalidMAC {
						payment.Emv.Cryptogram.Value[0] ^= 1
					}
					payload, err := json.Marshal(payment)
					if err != nil {
						t.Fatal("falha ao preparar JSON de teste")
					}
					// Simula a Card Machine lendo resp.Body do POST original à PSP.
					server := httptest.NewServer(router)
					defer server.Close()
					httpResponse, err := http.Post(server.URL+"/authorize/card", "application/json", bytes.NewReader(payload))
					if err != nil {
						t.Fatal(err)
					}
					defer httpResponse.Body.Close()
					body, err := io.ReadAll(httpResponse.Body)
					if err != nil {
						t.Fatal(err)
					}
					var response model.AuthorizationResponse
					want := model.AuthorizationResponse{Status: tt.want, PaymentType: paymentType, TimeNow: payment.TimeNow, Log: tt.wantLog}
					if paymentType == model.Credit && want.Log == "Saldo insuficiente." {
						want.Log = "Limite de crédito insuficiente."
					}
					if !tt.invalidTLV {
						want.CardNetwork, want.IssuerBank, want.Amount = model.NetworkMastercard, model.IssuerNubank, "25.33"
					}
					var fields map[string]json.RawMessage
					if json.Unmarshal(body, &response) != nil || json.Unmarshal(body, &fields) != nil || len(fields) != 7 || httpResponse.StatusCode != http.StatusOK || response != want {
						t.Fatal("resposta HTTP deve conter os sete campos esperados, incluindo log com motivo seguro")
					}
					for _, secret := range []string{manualCardPAN, tt.pin, keys.CMACKeyHex, keys.GCMKeyHex, "segredo"} {
						if secret != "" && strings.Contains(string(body), secret) {
							t.Fatal("resposta expôs dados sensíveis")
						}
					}
					if err := mock.ExpectationsWereMet(); err != nil {
						t.Fatal("o histórico e o débito devem estar finalizados antes da resposta HTTP")
					}
					t.Logf("Teste aprovado, valor: HTTP %d, %+v", httpResponse.StatusCode, response)
				})
			}
		})
	}
}

// Edite este JSON e o status esperado. Usa o saldo REAL, sem recarga automática.
// Toda tentativa fica no histórico; saldo zero => REPROVED, sem débito.
// Amount/Status/TimeNow/9F37 alterados exigem novo CMAC.
// Na pasta test: go test -v -run '^TestSaleCard$' -count=1
func TestSaleCard(t *testing.T) {
	wantStatus := model.StatusReproved // Cartão de teste cadastrado com saldo zero.
	payload := `{
  "SaleId": 900001,
  "TimeNow": "2026-10-26T12:00:00-03:00",
  "Amount": "254.33",
  "Status": "PENDING",
  "PaymentType": "DEBIT",
  "Emv": {
    "PAN": {"Tag": "Wg==", "Length": 8, "Value": "UgBQAAAAAAY="},
    "DateExpiry": {"Tag": "XyQ=", "Length": 3, "Value": "MRIx"},
    "TransactionCountry": {"Tag": "nxo=", "Length": 2, "Value": "AHY="},
    "Pin": {"PIN": "96b18d685dfad6be5d42eced6156329c509db489f4e0234d9120ca36380c2f38", "CVM": "ONLINE_PIN"},
    "NumberUnpredictable": {"Tag": "nzc=", "Length": 4, "Value": "AHyA/w=="},
    "Cryptogram": {"Tag": "nyY=", "Length": 16, "Value": "TBPpxnuG1a6jmwlXm5qPzQ=="}
  }
}`
	var payment model.CardPayment
	if json.Unmarshal([]byte(payload), &payment) != nil {
		t.Fatal("JSON do teste inválido")
	}
	keys := manualPaymentKeys(t)
	connection, err := db.ConnectDB()
	if err != nil {
		t.Fatal("falha na conexão ao PostgreSQL")
	}
	defer connection.Close()
	repo := repository.NewUserRepository(connection)
	decoded, decodeErr := parser.DecodeTLV(payment)
	var beforeCard model.Card
	hasCard := false
	if decodeErr == nil {
		beforeCard, err = repo.GetCardByPAN(decoded.PAN)
		if err != nil && !errors.Is(err, repository.ErrCardNotFound) {
			t.Fatal("falha ao consultar saldo antes da autorização")
		}
		hasCard = err == nil
	}
	var lastID int
	if err := connection.QueryRow("SELECT COALESCE(MAX(id), 0) FROM card_transactions").Scan(&lastID); err != nil {
		t.Fatal("falha ao consultar transações antes da autorização")
	}
	router := gin.New()
	cardHandler := handler.NewCardHandler(usecases.NewSaleCaseDependency(repo), keys)
	router.POST("/authorize/card", cardHandler.AuthorizeCard)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/authorize/card", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)
	var response model.AuthorizationResponse
	if json.Unmarshal(recorder.Body.Bytes(), &response) != nil || recorder.Code != http.StatusOK || response.Status != wantStatus {
		t.Fatalf("HTTP %d, status=%s; esperado=%s", recorder.Code, response.Status, wantStatus)
	}
	var count int
	if err := connection.QueryRow("SELECT count(*) FROM card_transactions WHERE sale_id = $1 AND id > $2", payment.SaleId, lastID).Scan(&count); err != nil || count != 1 {
		t.Fatal("cada tentativa deve produzir exatamente um registro de histórico")
	}
	var id int
	var cardID, amount sql.NullInt64
	var pan sql.NullString
	var status model.SaleStatus
	err = connection.QueryRow("SELECT id, card_id, pan, amount, status FROM card_transactions WHERE sale_id = $1 AND id > $2 ORDER BY id DESC LIMIT 1", payment.SaleId, lastID).
		Scan(&id, &cardID, &pan, &amount, &status)
	if err != nil || status != response.Status {
		t.Fatal("status no banco deve ser igual ao status retornado pelo HTTP")
	}
	if decodeErr == nil && (!pan.Valid || pan.String != decoded.PAN || !amount.Valid || amount.Int64 != decoded.AmountCents) {
		t.Fatal("dados decodificados não correspondem ao histórico")
	}
	if response.Status == model.StatusReproved {
		if hasCard {
			afterCard, err := repo.GetCardByPAN(decoded.PAN)
			if err != nil || afterCard.Balance != beforeCard.Balance {
				t.Fatal("autorização reprovada não deve alterar o saldo")
			}
		}
		t.Logf("Teste aprovado, valor: %s, transaction_id=%d, histórico salvo", response.Status, id)
		return
	}
	if decodeErr != nil || !hasCard {
		t.Fatal("a autorização aprovou um payload inválido")
	}
	card, err := repo.GetCardByPAN(decoded.PAN)
	if err != nil {
		t.Fatal("cartão da autorização não encontrado")
	}
	if card.Balance != beforeCard.Balance-decoded.AmountCents {
		t.Fatal("o saldo não foi debitado pelo valor exato em centavos")
	}
	if !cardID.Valid || cardID.Int64 != int64(card.ID) {
		t.Fatal("cartão da autorização não corresponde ao histórico")
	}
	cents := amount.Int64
	t.Logf("Teste concluido, valor: %s, transaction_id=%d, card_id=%d, amount=%d.%02d (%d centavos), saldo=%d centavos", response.Status, id, cardID.Int64, cents/100, cents%100, cents, card.Balance)
}
