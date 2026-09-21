-- 000001_init.up.sql
-- Carteira, transações de aposta, ledger, inbox e outbox.
--
-- Convenções
--   * Dinheiro: BIGINT em unidades mínimas (centavos) + currency CHAR(3). Nunca float.
--   * IDs nossos: UUID v7 gerado na aplicação. IDs do provedor: TEXT opaco.
--   * Enumerações: TEXT + CHECK (mais fácil de evoluir e reverter que CREATE TYPE).
--   * Timestamps: TIMESTAMPTZ vindos do domínio, sempre UTC.
--   * Papéis: o dono das tabelas roda as migrations; a aplicação usa wallet_app,
--     que não tem DELETE em nada e não tem UPDATE no ledger.

BEGIN;

-- ---------------------------------------------------------------------------
-- wallets: raiz do agregado. O lock por carteira é tomado nesta linha com
--
--     SELECT ... FROM wallets WHERE id = $1 FOR NO KEY UPDATE
--
-- e NÃO com FOR UPDATE. Motivo (reproduzido em teste): o INSERT em
-- wager_transactions acontece antes do lock e a FK dele pega FOR KEY SHARE na
-- linha da carteira. Duas transações concorrentes ficam cada uma com KEY SHARE
-- e, ao pedir FOR UPDATE, uma espera a outra: deadlock. FOR NO KEY UPDATE não
-- conflita com KEY SHARE, continua serializando os escritores, e é o mesmo
-- nível que o UPDATE de balance/version usa (nenhuma coluna-chave é alterada).
-- ---------------------------------------------------------------------------
CREATE TABLE wallets (
    id            UUID        PRIMARY KEY,
    player_id     UUID        NOT NULL,
    currency      CHAR(3)     NOT NULL,
    balance_minor BIGINT      NOT NULL,
    version       BIGINT      NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallets_player_currency_uq UNIQUE (player_id, currency),
    -- alvo da FK composta do ledger: garante no banco que a moeda do lançamento é a da carteira
    CONSTRAINT wallets_id_currency_uq     UNIQUE (id, currency),
    CONSTRAINT wallets_currency_iso_ck    CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT wallets_balance_nonneg_ck  CHECK (balance_minor >= 0),
    CONSTRAINT wallets_version_ck         CHECK (version >= 1)
);

-- Rede de proteção contra lost update e contra escrita fora do agregado:
-- saldo só muda junto com version + 1, e version só muda junto com saldo.
CREATE FUNCTION wallets_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wallets: delete is not allowed'
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'wallets: initial version must be 1'
                USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;

    IF (NEW.id, NEW.player_id, NEW.currency, NEW.created_at)
       IS DISTINCT FROM (OLD.id, OLD.player_id, OLD.currency, OLD.created_at) THEN
        RAISE EXCEPTION 'wallets: identity columns are immutable'
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF NEW.balance_minor <> OLD.balance_minor THEN
        IF NEW.version <> OLD.version + 1 THEN
            RAISE EXCEPTION 'wallets: balance change requires version = % (got %)',
                OLD.version + 1, NEW.version
                USING ERRCODE = 'check_violation';
        END IF;
    ELSIF NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'wallets: version only changes together with balance'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER wallets_guard_trg
    BEFORE INSERT OR UPDATE OR DELETE ON wallets
    FOR EACH ROW EXECUTE FUNCTION wallets_guard();

-- ---------------------------------------------------------------------------
-- wager_transactions: a operação em si + idempotência + máquina de estados.
-- origin = INTERNAL só para OPENING; HTTP/SQS para operações de provedor.
-- ---------------------------------------------------------------------------
CREATE TABLE wager_transactions (
    id                                UUID        PRIMARY KEY,
    origin                            TEXT        NOT NULL,
    kind                              TEXT        NOT NULL,
    status                            TEXT        NOT NULL,
    wallet_id                         UUID        NOT NULL REFERENCES wallets (id),
    player_id                         UUID        NOT NULL,
    amount_minor                      BIGINT      NOT NULL,
    currency                          CHAR(3)     NOT NULL,

    -- só para operações externas
    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      TEXT,       -- sha-256 hex do JSON canônico
    round_id                          TEXT,
    game_id                           TEXT,
    reference_external_transaction_id TEXT,
    reference_transaction_id          UUID        REFERENCES wager_transactions (id),

    -- resultado (é isso que o replay devolve)
    failure_code                      TEXT,
    result_balance_minor              BIGINT,

    -- retentativa de referência pendente
    reference_attempts                INT         NOT NULL DEFAULT 0,
    next_attempt_at                   TIMESTAMPTZ,
    reference_deadline_at             TIMESTAMPTZ,

    correlation_id                    TEXT        NOT NULL,
    created_at                        TIMESTAMPTZ NOT NULL,
    updated_at                        TIMESTAMPTZ NOT NULL,
    completed_at                      TIMESTAMPTZ,

    CONSTRAINT wtx_origin_ck CHECK (origin IN ('INTERNAL', 'HTTP', 'SQS')),
    CONSTRAINT wtx_kind_ck   CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    CONSTRAINT wtx_status_ck CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    CONSTRAINT wtx_currency_iso_ck CHECK (currency ~ '^[A-Z]{3}$'),

    -- interno x externo
    CONSTRAINT wtx_internal_is_opening_ck CHECK ((origin = 'INTERNAL') = (kind = 'OPENING')),
    CONSTRAINT wtx_internal_fields_ck CHECK (
        origin <> 'INTERNAL' OR (
            provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
            AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL)),
    CONSTRAINT wtx_external_fields_ck CHECK (
        origin = 'INTERNAL' OR (
            provider_id IS NOT NULL AND external_transaction_id IS NOT NULL AND idempotency_key IS NOT NULL
            AND payload_hash IS NOT NULL AND round_id IS NOT NULL AND game_id IS NOT NULL)),
    CONSTRAINT wtx_payload_hash_ck CHECK (payload_hash IS NULL OR payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT wtx_opening_processed_ck CHECK (kind <> 'OPENING' OR status = 'PROCESSED'),

    -- política de valor: LOSS é zero, o resto é positivo
    CONSTRAINT wtx_amount_ck CHECK (CASE WHEN kind = 'LOSS' THEN amount_minor = 0 ELSE amount_minor > 0 END),

    -- referência
    CONSTRAINT wtx_reference_required_ck  CHECK (kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NOT NULL),
    CONSTRAINT wtx_reference_forbidden_ck CHECK (kind <> 'BET' OR reference_external_transaction_id IS NULL),
    CONSTRAINT wtx_no_self_reference_ck   CHECK (reference_transaction_id IS NULL OR reference_transaction_id <> id),

    -- coerência da máquina de estados
    CONSTRAINT wtx_completed_ck CHECK ((completed_at IS NOT NULL) = (status IN ('PROCESSED', 'REJECTED', 'FAILED'))),
    CONSTRAINT wtx_failure_code_ck CHECK ((failure_code IS NOT NULL) = (status IN ('REJECTED', 'FAILED'))),
    CONSTRAINT wtx_processed_result_ck CHECK (status <> 'PROCESSED' OR result_balance_minor IS NOT NULL),
    CONSTRAINT wtx_processed_reversal_resolved_ck CHECK (
        status <> 'PROCESSED' OR kind NOT IN ('REFUND', 'ROLLBACK') OR reference_transaction_id IS NOT NULL),
    CONSTRAINT wtx_pending_reference_ck CHECK (
        status <> 'PENDING_REFERENCE' OR (next_attempt_at IS NOT NULL AND reference_deadline_at IS NOT NULL)),
    CONSTRAINT wtx_result_balance_nonneg_ck CHECK (result_balance_minor IS NULL OR result_balance_minor >= 0),
    CONSTRAINT wtx_reference_attempts_ck CHECK (reference_attempts >= 0),

    -- idempotência: os dois índices também servem o replay e a resolução de referência
    CONSTRAINT wtx_provider_idempotency_key_uq UNIQUE (provider_id, idempotency_key),
    CONSTRAINT wtx_provider_external_id_uq     UNIQUE (provider_id, external_transaction_id)
);

-- um único crédito inicial por carteira
CREATE UNIQUE INDEX wtx_opening_per_wallet_uq
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

-- no máximo uma reversão bem-sucedida (REFUND ou ROLLBACK) por transação referenciada
CREATE UNIQUE INDEX wtx_single_successful_reversal_uq
    ON wager_transactions (reference_transaction_id)
    WHERE kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED';

-- fila do worker de referências: só o que está pendente mora no índice
CREATE INDEX wtx_pending_reference_due_idx
    ON wager_transactions (next_attempt_at, id)
    WHERE status = 'PENDING_REFERENCE';

-- "acordar" quem espera por uma transação que acabou de ser processada.
-- Usar com FOR UPDATE SKIP LOCKED para não cruzar lock com o worker.
CREATE INDEX wtx_waiting_for_reference_idx
    ON wager_transactions (provider_id, reference_external_transaction_id)
    WHERE status = 'PENDING_REFERENCE';

-- consultas por carteira (auditoria, reconciliação); id v7 já ordena por tempo
CREATE INDEX wtx_wallet_idx ON wager_transactions (wallet_id, id);

-- estado terminal não transita, identidade não muda, nada é apagado
CREATE FUNCTION wager_transactions_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wager_transactions: delete is not allowed'
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'wager_transactions: % is terminal (id %)', OLD.status, OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF (NEW.id, NEW.origin, NEW.kind, NEW.wallet_id, NEW.player_id, NEW.amount_minor, NEW.currency,
        NEW.provider_id, NEW.external_transaction_id, NEW.idempotency_key, NEW.payload_hash,
        NEW.round_id, NEW.game_id, NEW.reference_external_transaction_id, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.origin, OLD.kind, OLD.wallet_id, OLD.player_id, OLD.amount_minor, OLD.currency,
        OLD.provider_id, OLD.external_transaction_id, OLD.idempotency_key, OLD.payload_hash,
        OLD.round_id, OLD.game_id, OLD.reference_external_transaction_id, OLD.created_at) THEN
        RAISE EXCEPTION 'wager_transactions: identity columns are immutable (id %)', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER wager_transactions_guard_trg
    BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_transactions_guard();

-- ---------------------------------------------------------------------------
-- wallet_ledger_entries: append-only. Um lançamento por mudança de saldo.
-- wallet_version é a versão da carteira DEPOIS do lançamento: dá ordenação
-- estável por carteira (cursor da paginação) e impede dois lançamentos
-- disputando a mesma versão.
-- ---------------------------------------------------------------------------
CREATE TABLE wallet_ledger_entries (
    id                   UUID        PRIMARY KEY,
    wallet_id            UUID        NOT NULL,
    transaction_id       UUID        NOT NULL REFERENCES wager_transactions (id),
    direction            TEXT        NOT NULL,
    amount_minor         BIGINT      NOT NULL,
    currency             CHAR(3)     NOT NULL,
    balance_before_minor BIGINT      NOT NULL,
    balance_after_minor  BIGINT      NOT NULL,
    wallet_version       BIGINT      NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL,

    CONSTRAINT wle_wallet_currency_fk FOREIGN KEY (wallet_id, currency) REFERENCES wallets (id, currency),
    CONSTRAINT wle_wallet_transaction_uq UNIQUE (wallet_id, transaction_id),
    CONSTRAINT wle_wallet_version_uq     UNIQUE (wallet_id, wallet_version),
    CONSTRAINT wle_direction_ck CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT wle_amount_ck    CHECK (amount_minor > 0),
    CONSTRAINT wle_balances_nonneg_ck CHECK (balance_before_minor >= 0 AND balance_after_minor >= 0),
    CONSTRAINT wle_wallet_version_ck  CHECK (wallet_version >= 1),
    CONSTRAINT wle_arithmetic_ck CHECK (
        (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor) OR
        (direction = 'DEBIT'  AND balance_after_minor = balance_before_minor - amount_minor))
);

CREATE FUNCTION wallet_ledger_entries_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only (% blocked)', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER wle_no_update_delete_trg
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION wallet_ledger_entries_immutable();

CREATE TRIGGER wle_no_truncate_trg
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION wallet_ledger_entries_immutable();

-- Saldo e ledger confirmados juntos, garantido pelo banco: conferido no COMMIT.
-- Se o saldo mudou (ou nasceu positivo), tem que existir o lançamento daquela
-- versão com o mesmo saldo final. Custa um lookup no índice único acima.
CREATE FUNCTION wallets_require_ledger_entry() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (TG_OP = 'INSERT' AND NEW.balance_minor = 0)
       OR (TG_OP = 'UPDATE' AND NEW.balance_minor = OLD.balance_minor) THEN
        RETURN NULL;
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM wallet_ledger_entries e
         WHERE e.wallet_id = NEW.id
           AND e.wallet_version = NEW.version
           AND e.balance_after_minor = NEW.balance_minor) THEN
        RAISE EXCEPTION 'wallet % reached version % without a matching ledger entry', NEW.id, NEW.version
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER wallets_require_ledger_entry_trg
    AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallets_require_ledger_entry();

-- ---------------------------------------------------------------------------
-- inbox_messages: deduplicação durável do consumidor SQS.
-- Gravada na mesma transação SQL do tratamento da mensagem.
-- ---------------------------------------------------------------------------
CREATE TABLE inbox_messages (
    consumer_name  TEXT        NOT NULL,
    message_id     TEXT        NOT NULL,
    payload_hash   TEXT        NOT NULL,
    transaction_id UUID        REFERENCES wager_transactions (id),
    outcome        TEXT,
    received_at    TIMESTAMPTZ NOT NULL,
    completed_at   TIMESTAMPTZ,

    CONSTRAINT inbox_pk PRIMARY KEY (consumer_name, message_id),
    CONSTRAINT inbox_payload_hash_ck CHECK (payload_hash ~ '^[0-9a-f]{64}$')
);

-- ---------------------------------------------------------------------------
-- outbox_events: o evento nasce na mesma transação do saldo; o publisher
-- publica depois. id = eventId estável entre republicações.
-- ---------------------------------------------------------------------------
CREATE TABLE outbox_events (
    id              UUID        PRIMARY KEY,
    aggregate_type  TEXT        NOT NULL,
    aggregate_id    UUID        NOT NULL,
    event_type      TEXT        NOT NULL,
    event_version   INT         NOT NULL,
    correlation_id  TEXT        NOT NULL,
    causation_id    TEXT,
    partition_key   TEXT        NOT NULL,       -- MessageGroupId (walletId)
    payload         JSONB       NOT NULL,       -- snapshot imutável do envelope
    occurred_at     TIMESTAMPTZ NOT NULL,

    -- estado de publicação (única parte mutável)
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    locked_by       TEXT,
    locked_until    TIMESTAMPTZ,
    last_error      TEXT,
    published_at    TIMESTAMPTZ,

    CONSTRAINT outbox_aggregate_type_ck CHECK (aggregate_type IN ('wallet', 'wager_transaction')),
    CONSTRAINT outbox_event_type_ck CHECK (event_type IN (
        'WagerTransactionProcessed', 'WagerTransactionRejected',
        'WalletBalanceChanged', 'WagerTransactionPendingReference')),
    CONSTRAINT outbox_event_version_ck CHECK (event_version >= 1),
    CONSTRAINT outbox_attempts_ck      CHECK (attempts >= 0),
    CONSTRAINT outbox_lease_ck         CHECK ((locked_by IS NULL) = (locked_until IS NULL))
);

-- fila do publisher: só eventos não publicados
CREATE INDEX outbox_pending_idx
    ON outbox_events (next_attempt_at, id)
    WHERE published_at IS NULL;

-- métrica de atraso e diagnóstico por agregado
CREATE INDEX outbox_aggregate_idx ON outbox_events (aggregate_id, id);

CREATE FUNCTION outbox_events_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.aggregate_type, NEW.aggregate_id, NEW.event_type, NEW.event_version,
        NEW.correlation_id, NEW.causation_id, NEW.partition_key, NEW.payload, NEW.occurred_at)
       IS DISTINCT FROM
       (OLD.id, OLD.aggregate_type, OLD.aggregate_id, OLD.event_type, OLD.event_version,
        OLD.correlation_id, OLD.causation_id, OLD.partition_key, OLD.payload, OLD.occurred_at) THEN
        RAISE EXCEPTION 'outbox_events: event snapshot is immutable (id %)', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at THEN
        RAISE EXCEPTION 'outbox_events: published_at cannot be rewritten (id %)', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER outbox_events_guard_trg
    BEFORE UPDATE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_events_guard();

-- ---------------------------------------------------------------------------
-- Privilégios do papel de runtime. Sem DELETE/TRUNCATE em nada; ledger só INSERT.
-- O papel é criado fora da migration (init do Postgres no compose).
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'wallet_app') THEN
        GRANT SELECT, INSERT, UPDATE ON wallets, wager_transactions, inbox_messages, outbox_events TO wallet_app;
        GRANT SELECT, INSERT         ON wallet_ledger_entries TO wallet_app;
    END IF;
END;
$$;

COMMIT;
