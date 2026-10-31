-- Somente para um banco vazio. Os scripts seguintes evoluem este schema histórico.
BEGIN;

CREATE TABLE cards (
    id BIGSERIAL PRIMARY KEY,
    pan VARCHAR(19) NOT NULL UNIQUE,
    card_network VARCHAR(30) NOT NULL,
    issuer_bank VARCHAR(100) NOT NULL,
    balance BIGINT NOT NULL DEFAULT 0,
    card_type VARCHAR(20) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE'
);

CREATE TABLE card_transactions (
    id BIGSERIAL PRIMARY KEY,
    sale_id BIGINT NOT NULL,
    pan VARCHAR(19) NOT NULL,
    card_id BIGINT NOT NULL,
    amount BIGINT NOT NULL,
    payment_type VARCHAR(20) NOT NULL DEFAULT 'DEBIT',
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    time_transaction TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMIT;
