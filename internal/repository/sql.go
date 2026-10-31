package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"psp_go/internal/model"
)

var (
	ErrCardNotFound            = errors.New("cartão não encontrado")
	ErrCardAlreadyExists       = errors.New("já existe um cartão cadastrado com esse PAN")
	ErrInsufficientBalance     = errors.New("saldo insuficiente")
	ErrInsufficientCreditLimit = errors.New("limite de crédito insuficiente")
	ErrUnsupportedPaymentType  = errors.New("tipo de pagamento não suportado")
	ErrInvalidAmount           = errors.New("valor deve ser positivo")
	ErrSaleNotPending          = errors.New("tentativa de venda ausente, finalizada ou com dados divergentes")
)

type UserRepository struct {
	Db *sql.DB
}

func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{Db: db}
}

// CardPANExists verifica o PAN completo nos dois cadastros do MVP.
func (r *UserRepository) CardPANExists(pan string) (bool, error) {
	var exists bool
	query := "SELECT EXISTS (SELECT 1 FROM cards_debit WHERE pan = $1 UNION ALL SELECT 1 FROM cards_credit WHERE pan = $1)"
	err := r.Db.QueryRow(query, pan).Scan(&exists)
	return exists, err
}

// InsertCard gera bcrypt antes do SQL: o PIN em texto existe apenas em memória.
// dateExpiry recebe YYMMDD, o mesmo conteúdo reconstruído da tag 5F24.
// Mantém a assinatura usada pelos cadastros de teste existentes.
func (r *UserRepository) InsertCard(pan string, network model.CardNetwork, issuer model.IssuerBank, cardType model.CardPaymentType, dateExpiry, pin string) (int, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return 0, fmt.Errorf("erro ao gerar hash do PIN: %w", err)
	}
	return r.CreatCard(model.Card{PAN: pan, CardNetwork: network, IssuerBank: issuer, CardType: cardType, PINHash: string(hash), DateExpiry: dateExpiry, Balance: 0, Status: "ACTIVE"})
}

// CreatCard persiste somente os dados preparados pelo usecase e o hash do PIN.
// O ID é gerado pelo PostgreSQL; o PIN em texto não participa da consulta.
func (r *UserRepository) CreatCard(card model.Card) (int, error) {
	exists, err := r.CardPANExists(card.PAN)
	if err != nil {
		return 0, err
	}
	if exists {
		return 0, ErrCardAlreadyExists
	}

	var id int
	query := "INSERT INTO cards_debit (pan, card_network, issuer_bank, card_type, pin_hash, date_expiry, balance, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id"
	err = r.Db.QueryRow(query, card.PAN, card.CardNetwork, card.IssuerBank, card.CardType, card.PINHash, card.DateExpiry, card.Balance, card.Status).Scan(&id)
	var postgresErr *pq.Error
	if errors.As(err, &postgresErr) && postgresErr.Code == "23505" {
		return 0, ErrCardAlreadyExists
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// InsertCreditCard recebe o cartão com PINHash já preparado e valores em centavos.
// Tipo CREDIT, saldo zero e status ACTIVE vêm dos padrões de cards_credit.
// O CHECK do banco também impede amount > limit em INSERTs e UPDATEs diretos.
func (r *UserRepository) InsertCreditCard(card model.Card, limit, amount int64) (int, error) {
	exists, err := r.CardPANExists(card.PAN)
	if err != nil {
		return 0, err
	}
	if exists {
		return 0, ErrCardAlreadyExists
	}

	var id int
	query := `INSERT INTO cards_credit (pan, card_network, issuer_bank, pin_hash, date_expiry, "limit", amount) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`
	err = r.Db.QueryRow(query, card.PAN, card.CardNetwork, card.IssuerBank, card.PINHash, card.DateExpiry, limit, amount).Scan(&id)
	var postgresErr *pq.Error
	if errors.As(err, &postgresErr) && postgresErr.Code == "23505" {
		return 0, ErrCardAlreadyExists
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// AddValue recebe o ID do cartão já validado pelo usecase e centavos inteiros.
// Retorna o novo saldo em centavos; a soma no SQL preserva créditos concorrentes.
func (r *UserRepository) AddValue(cardID int, amount int64) (int64, error) {
	if amount <= 0 {
		return 0, ErrInvalidAmount
	}
	query := "UPDATE cards_debit SET balance = balance + $1 WHERE id = $2 RETURNING balance"
	var balance int64
	err := r.Db.QueryRow(query, amount, cardID).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrCardNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("erro ao adicionar valor ao cartão: %w", err)
	}
	return balance, nil
}

func (r *UserRepository) CreatePendingSale(saleID int) (int, error) {
	var id int
	query := "INSERT INTO card_transactions (sale_id, payment_type, status) VALUES ($1, NULL, $2) RETURNING id"
	if err := r.Db.QueryRow(query, saleID, model.StatusPending).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (r *UserRepository) UpdateSaleDetails(id int, pan string, amount int64, paymentType model.CardPaymentType, cardID *int) error {
	query := "UPDATE card_transactions SET pan = $1, amount = $2, payment_type = $3, card_id = $4 WHERE id = $5 AND status = $6"
	result, err := r.Db.Exec(query, pan, amount, paymentType, cardID, id, model.StatusPending)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrSaleNotPending
	}
	return nil
}

// UpdateSaleStatus finaliza somente a tentativa que ainda está PENDING.
func (r *UserRepository) UpdateSaleStatus(id int, status model.SaleStatus) error {
	query := "UPDATE card_transactions SET status = $1 WHERE id = $2 AND status = $3"
	result, err := r.Db.Exec(query, status, id, model.StatusPending)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrSaleNotPending
	}
	return nil
}

// ApproveSale debita e muda o histórico PENDING para APPROVED atomicamente.
// Uma falha desfaz o débito e mantém o histórico para registrar a reprovação.
func (r *UserRepository) ApproveSale(transactionID, cardID int, amount int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	tx, err := r.Db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec("UPDATE cards_debit SET balance = balance - $1 WHERE id = $2 AND balance >= $1", amount, cardID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrInsufficientBalance
	}
	query := "UPDATE card_transactions SET status = $1 WHERE id = $2 AND status = $3 AND card_id = $4 AND amount = $5 AND payment_type = $6"
	result, err = tx.Exec(query, model.StatusApproved, transactionID, model.StatusPending, cardID, amount, model.Debit)
	if err != nil {
		return err
	}
	rows, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrSaleNotPending
	}
	return tx.Commit()
}

// ApproveCreditSale soma a venda ao amount e aprova o histórico atomicamente.
// A soma em numeric na condição evita overflow e é reavaliada sob concorrência.
func (r *UserRepository) ApproveCreditSale(transactionID, cardID int, amount int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	tx, err := r.Db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`UPDATE cards_credit SET amount = amount + $1 WHERE id = $2 AND amount::numeric + $1 <= "limit"`, amount, cardID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrInsufficientCreditLimit
	}
	query := "UPDATE card_transactions SET status = $1 WHERE id = $2 AND status = $3 AND card_id = $4 AND amount = $5 AND payment_type = $6"
	result, err = tx.Exec(query, model.StatusApproved, transactionID, model.StatusPending, cardID, amount, model.Credit)
	if err != nil {
		return err
	}
	rows, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrSaleNotPending
	}
	return tx.Commit()
}

// GetCardByPAN consulta somente os dados necessários à validação do cartão.
func (r *UserRepository) GetCardByPAN(pan string) (model.Card, error) {
	var card model.Card
	var pinHash sql.NullString // Cartões anteriores à migração podem não ter hash.
	var dateExpiry sql.NullString
	query := "SELECT id, pan, card_network, issuer_bank, pin_hash, date_expiry, balance, card_type FROM cards_debit WHERE pan = $1"
	err := r.Db.QueryRow(query, pan).Scan(&card.ID, &card.PAN, &card.CardNetwork, &card.IssuerBank, &pinHash, &dateExpiry, &card.Balance, &card.CardType)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Card{}, ErrCardNotFound
	}
	if err != nil {
		return model.Card{}, fmt.Errorf("erro ao consultar cartão: %w", err)
	}
	card.PINHash = pinHash.String
	card.DateExpiry = dateExpiry.String
	return card, nil
}

// GetCreditCardByPAN recupera os mesmos dados de autenticação e o uso do limite.
func (r *UserRepository) GetCreditCardByPAN(pan string) (model.Card, error) {
	var card model.Card
	var pinHash sql.NullString // Cartões anteriores à migração podem não ter hash.
	var dateExpiry sql.NullString
	query := `SELECT id, pan, card_network, issuer_bank, pin_hash, date_expiry, card_type, "limit", amount FROM cards_credit WHERE pan = $1`
	err := r.Db.QueryRow(query, pan).Scan(&card.ID, &card.PAN, &card.CardNetwork, &card.IssuerBank, &pinHash, &dateExpiry, &card.CardType, &card.Limit, &card.Amount)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Card{}, ErrCardNotFound
	}
	if err != nil {
		return model.Card{}, fmt.Errorf("erro ao consultar cartão: %w", err)
	}
	card.PINHash = pinHash.String
	card.DateExpiry = dateExpiry.String
	return card, nil
}
