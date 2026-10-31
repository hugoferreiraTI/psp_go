package test

import (
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"psp_go/internal/model"
	"psp_go/internal/repository"
)

const existsPANQuery = "SELECT EXISTS (SELECT 1 FROM cards_debit WHERE pan = $1 UNION ALL SELECT 1 FROM cards_credit WHERE pan = $1)"

const selectCardQuery = "SELECT id, pan, card_network, issuer_bank, pin_hash, date_expiry, balance, card_type FROM cards_debit WHERE pan = $1"
const insertCardQuery = "INSERT INTO cards_debit (pan, card_network, issuer_bank, card_type, pin_hash, date_expiry, balance, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id"
const insertCreditCardQuery = `INSERT INTO cards_credit (pan, card_network, issuer_bank, pin_hash, date_expiry, "limit", amount) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`
const createPendingSaleQuery = "INSERT INTO card_transactions (sale_id, payment_type, status) VALUES ($1, NULL, $2) RETURNING id"
const updateSaleDetailsQuery = "UPDATE card_transactions SET pan = $1, amount = $2, payment_type = $3, card_id = $4 WHERE id = $5 AND status = $6"
const updateSaleStatusQuery = "UPDATE card_transactions SET status = $1 WHERE id = $2 AND status = $3"
const approveSaleQuery = "UPDATE card_transactions SET status = $1 WHERE id = $2 AND status = $3 AND card_id = $4 AND amount = $5 AND payment_type = $6"
const debitCardQuery = "UPDATE cards_debit SET balance = balance - $1 WHERE id = $2 AND balance >= $1"
const addValueQuery = "UPDATE cards_debit SET balance = balance + $1 WHERE id = $2 RETURNING balance"

const selectCreditCardQuery = `SELECT id, pan, card_network, issuer_bank, pin_hash, date_expiry, card_type, "limit", amount FROM cards_credit WHERE pan = $1`
const updateCreditSaleDetailsQuery = "UPDATE card_transactions SET pan = $1, amount = $2, payment_type = $3, card_id = $4 WHERE id = $5 AND status = $6"
const approveCreditSaleQuery = "UPDATE card_transactions SET status = $1 WHERE id = $2 AND status = $3 AND card_id = $4 AND amount = $5 AND payment_type = $6"
const chargeCreditCardQuery = `UPDATE cards_credit SET amount = amount + $1 WHERE id = $2 AND amount::numeric + $1 <= "limit"`

// Dados diretos, sem JSON: saldo R$ 100,00 + R$ 25,33 = R$ 125,33.
func TestAddValue(t *testing.T) {
	updateErr := errors.New("falha no UPDATE")
	for _, tt := range []struct {
		name        string
		amount      int64
		missing     bool
		updateErr   error
		wantBalance int64
		wantErr     error
	}{
		{name: "soma pelo ID", amount: 2533, wantBalance: 12533},
		{name: "valor zero", wantErr: repository.ErrInvalidAmount},
		{name: "valor negativo", amount: -1, wantErr: repository.ErrInvalidAmount},
		{name: "cartão removido", amount: 2533, missing: true, wantErr: repository.ErrCardNotFound},
		{name: "erro SQL", amount: 2533, updateErr: updateErr, wantErr: updateErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			if tt.amount > 0 {
				query := mock.ExpectQuery(addValueQuery).WithArgs(tt.amount, 1)
				if tt.updateErr != nil {
					query.WillReturnError(tt.updateErr)
				} else {
					rows := sqlmock.NewRows([]string{"balance"})
					if !tt.missing {
						rows.AddRow(int64(10000) + tt.amount)
					}
					query.WillReturnRows(rows)
				}
			}
			balance, err := repo.AddValue(1, tt.amount)
			if balance != tt.wantBalance || !errors.Is(err, tt.wantErr) {
				t.Fatalf("saldo=%d, erro=%v; esperado saldo=%d, erro=%v", balance, err, tt.wantBalance, tt.wantErr)
			}
			t.Logf("Teste aprovado, valor: saldo=%d centavos, erro=%v", balance, err)
		})
	}
}

func TestGetCardByPAN(t *testing.T) {
	card := testCard(t)
	queryErr := errors.New("falha SQL")
	for _, tt := range []struct {
		name string
		card *model.Card
		err  error
	}{
		{"encontrado", card, nil},
		{"ausente", nil, repository.ErrCardNotFound},
		{"erro SQL", nil, queryErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			query := mock.ExpectQuery(selectCardQuery).WithArgs(manualCardPAN)
			if tt.err == queryErr {
				query.WillReturnError(queryErr)
			} else {
				query.WillReturnRows(cardRows(tt.card))
			}
			got, err := repo.GetCardByPAN(manualCardPAN)
			if !errors.Is(err, tt.err) || (tt.card != nil && got != *tt.card) {
				t.Fatal("dados ou erro da consulta divergentes")
			}
			t.Logf("Teste aprovado, valor: id=%d, pan=%s, bandeira=%s, emissor=%s, erro=%v", got.ID, got.PAN, got.CardNetwork, got.IssuerBank, err)
		})
	}
}

// Confere bcrypt no argumento SQL e salts diferentes para o mesmo PIN.
func TestInsertCardHashesPIN(t *testing.T) {
	repo, mock := newRepositoryMock(t)
	var hashes [2]string
	var ids [2]int
	for i, pan := range []string{manualCardPAN, "5200000000000007"} {
		mock.ExpectQuery(existsPANQuery).WithArgs(pan).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectQuery(insertCardQuery).
			WithArgs(pan, "MASTERCARD", "NUBANK", "DEBIT", bcryptHashArgument{hash: &hashes[i]}, manualCardExpiry, int64(0), "ACTIVE").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(i + 1))
		id, err := repo.InsertCard(pan, model.NetworkMastercard, model.IssuerNubank, model.Debit, manualCardExpiry, manualCardPIN)
		if err != nil || id != i+1 {
			t.Fatal("cartão deve ser inserido com hash bcrypt, nunca PIN em texto")
		}
		ids[i] = id
	}
	if hashes[0] == hashes[1] {
		t.Fatal("o mesmo PIN deve gerar hashes com salts diferentes")
	}
	t.Logf("Teste aprovado, valor: IDs dos cartões=%v", ids)
}

func TestCreatCardSQL(t *testing.T) {
	queryErr := errors.New("falha SQL")
	for _, tt := range []struct {
		name    string
		err     error
		wantErr error
	}{
		{name: "sucesso"},
		{name: "PAN duplicado", err: &pq.Error{Code: "23505"}, wantErr: repository.ErrCardAlreadyExists},
		{name: "erro SQL", err: queryErr, wantErr: queryErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			card := testCard(t)
			card.Status = "ACTIVE"
			mock.ExpectQuery(existsPANQuery).WithArgs(card.PAN).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			query := mock.ExpectQuery(insertCardQuery).WithArgs(card.PAN, card.CardNetwork, card.IssuerBank, card.CardType, card.PINHash, card.DateExpiry, int64(0), "ACTIVE")
			if tt.err != nil {
				query.WillReturnError(tt.err)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
			}
			id, err := repo.CreatCard(*card)
			wantID := 0
			if tt.wantErr == nil {
				wantID = 2
			}
			if id != wantID || !errors.Is(err, tt.wantErr) {
				t.Fatal("ID ou erro do cadastro divergente")
			}
			t.Logf("Teste aprovado, valor: card_id=%d, erro=%v", id, err)
		})
	}
}

func TestInsertCreditCard(t *testing.T) {
	checkErr := &pq.Error{Code: "23514", Constraint: "cards_credit_amount_limit_check"}
	for _, tt := range []struct {
		name     string
		amount   int64
		queryErr error
		wantErr  error
	}{
		{name: "abaixo do limite", amount: 2533},
		{name: "igual ao limite", amount: 10000},
		{name: "acima do limite bloqueado pelo banco", amount: 10001, queryErr: checkErr, wantErr: checkErr},
		{name: "PAN duplicado", queryErr: &pq.Error{Code: "23505"}, wantErr: repository.ErrCardAlreadyExists},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			card := testCard(t)
			mock.ExpectQuery(existsPANQuery).WithArgs(card.PAN).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			query := mock.ExpectQuery(insertCreditCardQuery).WithArgs(card.PAN, card.CardNetwork, card.IssuerBank, card.PINHash, card.DateExpiry, int64(10000), tt.amount)
			if tt.queryErr != nil {
				query.WillReturnError(tt.queryErr)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
			}
			id, err := repo.InsertCreditCard(*card, 10000, tt.amount)
			wantID := 0
			if tt.wantErr == nil {
				wantID = 1
			}
			if id != wantID || !errors.Is(err, tt.wantErr) {
				t.Fatal("ID ou erro do INSERT de crédito divergente")
			}
			t.Logf("Teste aprovado, valor: card_id=%d, erro=%v", id, err)
		})
	}
}

func TestCreatePendingSale(t *testing.T) {
	repo, mock := newRepositoryMock(t)
	mock.ExpectQuery(createPendingSaleQuery).WithArgs(900001, "PENDING").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(99))
	id, err := repo.CreatePendingSale(900001)
	if err != nil || id != 99 {
		t.Fatal("a tentativa deve começar com histórico PENDING")
	}
	t.Logf("Teste aprovado, valor: transaction_id=%d, PENDING", id)
}

func TestUpdateSaleStatus(t *testing.T) {
	for _, tt := range []struct {
		name string
		rows int64
		err  error
	}{
		{"reprovação salva", 1, nil},
		{"não sobrescreve registro finalizado", 0, repository.ErrSaleNotPending},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			mock.ExpectExec(updateSaleStatusQuery).WithArgs("REPROVED", 99, "PENDING").WillReturnResult(sqlmock.NewResult(0, tt.rows))
			err := repo.UpdateSaleStatus(99, model.StatusReproved)
			if !errors.Is(err, tt.err) {
				t.Fatalf("erro=%v; esperado=%v", err, tt.err)
			}
			t.Logf("Teste aprovado, valor: %v", err)
		})
	}
}

// O débito e APPROVED são atômicos; rollback preserva o histórico PENDING.
func TestApproveSale(t *testing.T) {
	for _, paymentType := range []model.CardPaymentType{model.Debit, model.Credit} {
		t.Run(string(paymentType), func(t *testing.T) {
			movementQuery, approveQuery := debitCardQuery, approveSaleQuery
			insufficientErr := repository.ErrInsufficientBalance
			if paymentType == model.Credit {
				movementQuery, approveQuery = chargeCreditCardQuery, approveCreditSaleQuery
				insufficientErr = repository.ErrInsufficientCreditLimit
			}
			updateErr := errors.New("falha na atualização")
			for _, tt := range []struct {
				name        string
				debitRows   int64
				approveRows int64
				updateErr   error
				wantErr     error
			}{
				{name: "sucesso", debitRows: 1, approveRows: 1},
				{name: "saldo insuficiente", wantErr: insufficientErr},
				{name: "rollback após erro no histórico", debitRows: 1, updateErr: updateErr, wantErr: updateErr},
				{name: "registro já finalizado", debitRows: 1, wantErr: repository.ErrSaleNotPending},
			} {
				t.Run(tt.name, func(t *testing.T) {
					repo, mock := newRepositoryMock(t)
					mock.ExpectBegin()
					mock.ExpectExec(movementQuery).WithArgs(int64(2533), 1).WillReturnResult(sqlmock.NewResult(0, tt.debitRows))
					if tt.debitRows > 0 {
						update := mock.ExpectExec(approveQuery).WithArgs("APPROVED", 99, "PENDING", 1, int64(2533), string(paymentType))
						if tt.updateErr != nil {
							update.WillReturnError(tt.updateErr)
						} else {
							update.WillReturnResult(sqlmock.NewResult(0, tt.approveRows))
						}
					}
					if tt.wantErr != nil {
						mock.ExpectRollback()
					} else {
						mock.ExpectCommit()
					}
					var err error
					if paymentType == model.Credit {
						err = repo.ApproveCreditSale(99, 1, 2533)
					} else {
						err = repo.ApproveSale(99, 1, 2533)
					}
					if !errors.Is(err, tt.wantErr) {
						t.Fatalf("erro=%v; esperado=%v", err, tt.wantErr)
					}
					t.Logf("Teste aprovado, valor: %v", err)
				})
			}
		})
	}
}

func newRepositoryMock(t *testing.T) (*repository.UserRepository, sqlmock.Sqlmock) {
	t.Helper()
	connection, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		connection.Close()
	})
	return repository.NewUserRepository(connection), mock
}

func cardRows(card *model.Card) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "pan", "card_network", "issuer_bank", "pin_hash", "date_expiry", "balance", "card_type"})
	if card != nil {
		rows.AddRow(card.ID, card.PAN, string(card.CardNetwork), string(card.IssuerBank), card.PINHash, card.DateExpiry, card.Balance, string(card.CardType))
	}
	return rows
}

type bcryptHashArgument struct {
	hash *string
	pin  string
}

func (a bcryptHashArgument) Match(value driver.Value) bool {
	hash, ok := value.(string)
	pin := a.pin
	if pin == "" {
		pin = manualCardPIN
	}
	if !ok || bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin)) != nil {
		return false
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil || cost != bcrypt.DefaultCost {
		return false
	}
	if a.hash != nil {
		*a.hash = hash
	}
	return true
}

func creditCardRows(card *model.Card) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "pan", "card_network", "issuer_bank", "pin_hash", "date_expiry", "card_type", "limit", "amount"})
	if card != nil {
		rows.AddRow(card.ID, card.PAN, string(card.CardNetwork), string(card.IssuerBank), card.PINHash, card.DateExpiry, string(card.CardType), card.Limit, card.Amount)
	}
	return rows
}

// PAN cadastrado em qualquer uma das duas tabelas impede um novo INSERT.
func TestCardPANExists(t *testing.T) {
	queryErr := errors.New("consulta indisponível")
	for _, tt := range []struct {
		name     string
		exists   bool
		queryErr error
	}{
		{name: "PAN novo"}, {name: "PAN cadastrado", exists: true}, {name: "falha SQL", queryErr: queryErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			query := mock.ExpectQuery(existsPANQuery).WithArgs(manualCardPAN)
			if tt.queryErr != nil {
				query.WillReturnError(tt.queryErr)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tt.exists))
			}
			exists, err := repo.CardPANExists(manualCardPAN)
			if exists != tt.exists || !errors.Is(err, tt.queryErr) {
				t.Fatal("resultado da consulta de PAN divergente")
			}
			t.Logf("Teste aprovado, valor: cadastrado=%t, erro=%v", exists, err)
		})
	}
	for _, kind := range []model.CardPaymentType{model.Debit, model.Credit} {
		t.Run("bloqueia novo cadastro "+string(kind), func(t *testing.T) {
			repo, mock := newRepositoryMock(t)
			card := testCard(t)
			mock.ExpectQuery(existsPANQuery).WithArgs(card.PAN).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			var id int
			var err error
			if kind == model.Debit {
				id, err = repo.CreatCard(*card)
			} else {
				id, err = repo.InsertCreditCard(*card, 10000, 0)
			}
			if id != 0 || !errors.Is(err, repository.ErrCardAlreadyExists) {
				t.Fatal("PAN existente deve bloquear o INSERT")
			}
			t.Logf("Teste aprovado, valor: %v", err)
		})
	}
}
