BEGIN;

ALTER TABLE card_transactions RENAME COLUMN credit_card_id TO card_id;

-- A renomeação atualiza os triggers e o índice; os corpos das funções são texto.
CREATE OR REPLACE FUNCTION validate_transaction_card() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    -- Tentativas ainda sem cartão identificado também fazem parte do histórico.
    IF NEW.card_id IS NULL THEN
        RETURN NEW;
    END IF;

    IF NEW.payment_type = 'DEBIT' THEN
        PERFORM id FROM cards_debit WHERE id = NEW.card_id FOR KEY SHARE;
    ELSIF NEW.payment_type = 'CREDIT' THEN
        PERFORM id FROM cards_credit WHERE id = NEW.card_id FOR KEY SHARE;
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

CREATE OR REPLACE FUNCTION restrict_transaction_card_change() RETURNS TRIGGER
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
        WHERE card_id = OLD.id AND payment_type = card_payment_type
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

COMMENT ON COLUMN card_transactions.card_id IS
    'ID em cards_debit ou cards_credit, determinado por payment_type; validado por triggers.';

COMMIT;
