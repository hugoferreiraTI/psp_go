BEGIN;

-- Impede escritas enquanto os vínculos existentes são transferidos.
LOCK TABLE cards_debit, cards_credit, card_transactions IN ACCESS EXCLUSIVE MODE;

ALTER TABLE card_transactions
    DROP CONSTRAINT card_transactions_one_card_check,
    DROP CONSTRAINT card_transactions_card_id_fkey,
    DROP CONSTRAINT card_transactions_credit_card_id_fkey;

UPDATE card_transactions
SET credit_card_id = card_id
WHERE card_id IS NOT NULL;

ALTER TABLE card_transactions DROP COLUMN card_id;

-- Uma FK nativa não pode apontar para duas tabelas. O tipo determina o cadastro.
CREATE FUNCTION validate_transaction_card() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    -- Tentativas ainda sem cartão identificado também fazem parte do histórico.
    IF NEW.credit_card_id IS NULL THEN
        RETURN NEW;
    END IF;

    IF NEW.payment_type = 'DEBIT' THEN
        PERFORM id FROM cards_debit WHERE id = NEW.credit_card_id FOR KEY SHARE;
    ELSIF NEW.payment_type = 'CREDIT' THEN
        PERFORM id FROM cards_credit WHERE id = NEW.credit_card_id FOR KEY SHARE;
    ELSE
        RAISE EXCEPTION 'Tipo de pagamento inválido para vincular o cartão'
            USING ERRCODE = '23514';
    END IF;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'Cartão inexistente para o tipo de pagamento'
            USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER card_transactions_validate_card
BEFORE INSERT OR UPDATE OF credit_card_id, payment_type ON card_transactions
FOR EACH ROW EXECUTE FUNCTION validate_transaction_card();

-- Protege também o lado do cadastro, como ON DELETE/UPDATE RESTRICT de uma FK.
CREATE FUNCTION restrict_transaction_card_change() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
DECLARE
    card_payment_type TEXT;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.id = OLD.id THEN
        RETURN NEW;
    END IF;

    IF TG_TABLE_NAME = 'cards_debit' THEN
        card_payment_type := 'DEBIT';
    ELSE
        card_payment_type := 'CREDIT';
    END IF;

    IF EXISTS (
        SELECT 1 FROM card_transactions
        WHERE credit_card_id = OLD.id AND payment_type = card_payment_type
    ) THEN
        RAISE EXCEPTION 'Cartão vinculado ao histórico de transações'
            USING ERRCODE = '23503';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER cards_debit_restrict_transactions
BEFORE DELETE OR UPDATE OF id ON cards_debit
FOR EACH ROW EXECUTE FUNCTION restrict_transaction_card_change();

CREATE TRIGGER cards_credit_restrict_transactions
BEFORE DELETE OR UPDATE OF id ON cards_credit
FOR EACH ROW EXECUTE FUNCTION restrict_transaction_card_change();

CREATE INDEX card_transactions_card_type_idx
ON card_transactions (credit_card_id, payment_type)
WHERE credit_card_id IS NOT NULL;

-- Executa a validação também para os vínculos migrados; falhas desfazem tudo.
UPDATE card_transactions SET credit_card_id = credit_card_id
WHERE credit_card_id IS NOT NULL;

COMMENT ON COLUMN card_transactions.credit_card_id IS
    'ID do cartão em cards_debit ou cards_credit, determinado por payment_type; validado por triggers.';

COMMIT;
