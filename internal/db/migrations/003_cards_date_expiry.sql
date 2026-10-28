-- YYMMDD é preservado como texto, inclusive zeros iniciais, igual à tag 5F24.
-- Cartões anteriores sem validade cadastrada são bloqueados pelo usecase.
ALTER TABLE cards ADD COLUMN date_expiry VARCHAR(6)
    CHECK (date_expiry IS NULL OR date_expiry ~ '^[0-9]{6}$');

COMMENT ON COLUMN cards.date_expiry IS
    'Data de expiracao YYMMDD, correspondente aos tres bytes BCD da tag EMV 5F24.';
