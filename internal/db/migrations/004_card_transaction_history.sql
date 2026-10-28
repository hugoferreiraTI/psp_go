-- O histórico existe antes de identificar cartão e interpretar o pagamento.
-- Dados indisponíveis ficam NULL; a FK continua exigindo um cards.id válido.
ALTER TABLE card_transactions
    ALTER COLUMN card_id DROP NOT NULL,
    ALTER COLUMN pan DROP NOT NULL,
    ALTER COLUMN amount DROP NOT NULL,
    ALTER COLUMN payment_type DROP NOT NULL;
