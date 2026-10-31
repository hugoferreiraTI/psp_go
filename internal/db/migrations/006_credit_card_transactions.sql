BEGIN;

-- IDs das duas tabelas podem coincidir; cada cartão mantém sua própria FK.
ALTER TABLE card_transactions
    ADD COLUMN credit_card_id BIGINT REFERENCES cards_credit(id),
    ADD CONSTRAINT card_transactions_one_card_check
        CHECK (card_id IS NULL OR credit_card_id IS NULL);

COMMENT ON COLUMN card_transactions.card_id IS 'Cartão de débito: cards_debit.id.';
COMMENT ON COLUMN card_transactions.credit_card_id IS 'Cartão de crédito: cards_credit.id.';

COMMIT;
