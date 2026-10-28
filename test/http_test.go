package test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/lib/pq"

	"psp_go/internal/client"
	"psp_go/internal/db"
	"psp_go/internal/handler"
	"psp_go/internal/model"
	"psp_go/internal/parser"
	"psp_go/internal/repository"
	"psp_go/internal/usecases"
)

func TestAuthorizeCardInvalidJSON(t *testing.T) {
	router := gin.New()
	cardHandler := handler.NewCardHandler(nil, usecases.PaymentKeys{}, nil)
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
			cardHandler := handler.NewCardHandler(usecases.NewSaleCaseDependency(repo), usecases.PaymentKeys{}, nil)
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
				query := mock.ExpectQuery(insertCardQuery).WithArgs(manualCardPAN, "MASTERCARD", "NUBANK", "DEBIT", bcryptHashArgument{}, manualCardExpiry, int64(0), "ACTIVE")
				if tt.duplicate {
					query.WillReturnError(&pq.Error{Code: "23505"})
				} else {
					query.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
				}
			}
			router := gin.New()
			cardHandler := handler.NewCardHandler(usecases.NewSaleCaseDependency(repo), usecases.PaymentKeys{}, nil)
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

// Valida a resposta e o callback; falha no callback não muda a autorização.
func TestAuthorizeCard(t *testing.T) {
	keys := newPaymentTestKeys(t)
	for _, tt := range []struct {
		name       string
		pin        string
		callback   int
		want       model.SaleStatus
		saveErr    error
		balance    int64
		changed    bool
		invalidTLV bool
		invalidMAC bool
		missing    bool
	}{
		{name: "aprovado", pin: manualCardPIN, callback: http.StatusNoContent, want: model.StatusApproved, balance: 10000},
		{name: "TLV inválido também tem histórico", pin: manualCardPIN, callback: http.StatusNoContent, want: model.StatusReproved, invalidTLV: true},
		{name: "CMAC inválido também tem histórico", pin: manualCardPIN, callback: http.StatusNoContent, want: model.StatusReproved, invalidMAC: true},
		{name: "cartão não encontrado também tem histórico", pin: manualCardPIN, callback: http.StatusNoContent, want: model.StatusReproved, missing: true},
		{name: "PIN errado", pin: "9999", callback: http.StatusNoContent, want: model.StatusReproved, balance: 10000},
		{name: "saldo zero", pin: manualCardPIN, callback: http.StatusNoContent, want: model.StatusReproved, balance: 0},
		{name: "saldo insuficiente", pin: manualCardPIN, callback: http.StatusNoContent, want: model.StatusReproved, balance: 2532},
		{name: "saldo exato", pin: manualCardPIN, callback: http.StatusNoContent, want: model.StatusApproved, balance: 2533},
		{name: "saldo mudou após consulta", pin: manualCardPIN, callback: http.StatusNoContent, want: model.StatusReproved, balance: 10000, changed: true},
		{name: "callback indisponível", pin: manualCardPIN, callback: http.StatusServiceUnavailable, want: model.StatusApproved, balance: 10000},
		{name: "falha ao gravar", pin: manualCardPIN, callback: http.StatusNoContent, want: model.StatusReproved, saveErr: errors.New("falha ao atualizar aprovação"), balance: 10000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			mock.ExpectQuery(createPendingSaleQuery).WithArgs(900001, "PENDING").
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(99))
			if !tt.invalidTLV {
				mock.ExpectExec(updateSaleDetailsQuery).WithArgs(manualCardPAN, int64(2533), "DEBIT", nil, 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
				if !tt.invalidMAC {
					card := testCard(t)
					card.Balance = tt.balance
					if tt.missing {
						mock.ExpectQuery(selectCardQuery).WithArgs(manualCardPAN).WillReturnRows(cardRows(nil))
					} else {
						mock.ExpectQuery(selectCardQuery).WithArgs(manualCardPAN).WillReturnRows(cardRows(card))
						mock.ExpectExec(updateSaleDetailsQuery).WithArgs(manualCardPAN, int64(2533), "DEBIT", 1, 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, 1))
						if tt.pin == manualCardPIN && tt.balance >= 2533 {
							mock.ExpectBegin()
							rows := int64(1)
							if tt.changed {
								rows = 0
							}
							mock.ExpectExec(debitCardQuery).WithArgs(int64(2533), 1).WillReturnResult(sqlmock.NewResult(0, rows))
							if rows > 0 {
								update := mock.ExpectExec(approveSaleQuery).WithArgs("APPROVED", 99, "PENDING", 1, int64(2533), "DEBIT")
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
			callbacks := make(chan model.AuthorizationResult, 2)
			machine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var result model.AuthorizationResult
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&result) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				callbacks <- result
				w.WriteHeader(tt.callback)
			}))
			defer machine.Close()
			router := gin.New()
			cardHandler := handler.NewCardHandler(usecases.NewSaleCaseDependency(repo), keys, client.NewCardMachineClient(machine.URL, nil))
			router.POST("/authorize/card", cardHandler.AuthorizeCard)
			payment := newSecuredPaymentWithPIN(t, keys, tt.pin)
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
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/authorize/card", bytes.NewReader(payload)))
			var response model.AuthorizationResponse
			message := "payment reproved"
			if tt.want == model.StatusApproved {
				message = "payment approved"
			}
			if json.Unmarshal(recorder.Body.Bytes(), &response) != nil || recorder.Code != http.StatusOK || response.Status != tt.want || response.Message != message {
				t.Fatal("resposta HTTP diferente da decisão esperada")
			}
			if len(callbacks) != 1 {
				t.Fatal("esperado um único callback")
			}
			if result := <-callbacks; result.SaleId != 900001 || result.Status != tt.want {
				t.Fatal("callback não corresponde à venda e ao status retornados")
			}
			t.Logf("Teste aprovado, valor: HTTP %d, %+v", recorder.Code, response)
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
	callbacks := make(chan model.AuthorizationResult, 2)
	machine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result model.AuthorizationResult
		if json.NewDecoder(r.Body).Decode(&result) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		callbacks <- result
		w.WriteHeader(http.StatusNoContent)
	}))
	defer machine.Close()
	router := gin.New()
	cardHandler := handler.NewCardHandler(usecases.NewSaleCaseDependency(repo), keys, client.NewCardMachineClient(machine.URL, nil))
	router.POST("/authorize/card", cardHandler.AuthorizeCard)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/authorize/card", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)
	var response model.AuthorizationResponse
	if json.Unmarshal(recorder.Body.Bytes(), &response) != nil || recorder.Code != http.StatusOK || response.Status != wantStatus {
		t.Fatalf("HTTP %d, status=%s; esperado=%s", recorder.Code, response.Status, wantStatus)
	}
	if len(callbacks) != 1 {
		t.Fatal("esperado um callback para a Card Machine simulada")
	}
	if result := <-callbacks; result.SaleId != payment.SaleId || result.Status != response.Status {
		t.Fatal("callback diferente da decisão retornada")
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
