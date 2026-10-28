-- Preserva os cartões existentes. Sem um PIN fornecido pelo titular, não é
-- possível gerar seu hash: NULL permanece bloqueado pela validação da PSP.
ALTER TABLE cards ADD COLUMN pin_hash TEXT;

-- Impede armazenar PIN em texto nesta coluna. Novos cartões criados pela PSP
-- recebem bcrypt.GenerateFromPassword; o formato bcrypt completo tem 60 bytes.
ALTER TABLE cards ADD CONSTRAINT cards_pin_hash_bcrypt_check
    CHECK (pin_hash IS NULL OR
           pin_hash ~ '^[$]2[aby][$](0[4-9]|[12][0-9]|3[01])[$][./A-Za-z0-9]{53}$');

COMMENT ON COLUMN cards.pin_hash IS
    'Hash bcrypt do PIN. Nunca armazena o PIN em texto; NULL bloqueia a transacao.';
