BEGIN;

-- Preserva os cartões, seus IDs e a FK de card_transactions.
ALTER TABLE cards RENAME TO cards_debit;

CREATE TABLE cards_credit (
    id BIGSERIAL PRIMARY KEY,
    pan VARCHAR(19) NOT NULL UNIQUE,
    card_network VARCHAR(30) NOT NULL,
    issuer_bank VARCHAR(100) NOT NULL,
    balance BIGINT NOT NULL DEFAULT 0,
    card_type VARCHAR(20) NOT NULL DEFAULT 'CREDIT',
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
    pin_hash TEXT CHECK (
        pin_hash IS NULL OR
        pin_hash ~ '^[$]2[aby][$](0[4-9]|[12][0-9]|3[01])[$][./A-Za-z0-9]{53}$'
    ),
    date_expiry VARCHAR(6) CHECK (
        date_expiry IS NULL OR date_expiry ~ '^[0-9]{6}$'
    ),
    "limit" BIGINT NOT NULL DEFAULT 0,
    amount BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT cards_credit_amount_limit_check CHECK (amount <= "limit")
);

COMMENT ON COLUMN cards_credit."limit" IS 'Limite do cartão de crédito em centavos.';
COMMENT ON COLUMN cards_credit.amount IS 'Valor em centavos; não pode ultrapassar o limite.';
COMMENT ON COLUMN cards_credit.pin_hash IS 'Somente hash bcrypt do PIN, nunca texto puro.';

COMMIT;
