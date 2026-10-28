-- A coluna card_id já existe. Vincula a transação ao cartão validado em cards.
ALTER TABLE card_transactions
    ADD CONSTRAINT card_transactions_card_id_fkey
    FOREIGN KEY (card_id) REFERENCES cards(id);
