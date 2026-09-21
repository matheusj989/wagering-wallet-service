BEGIN;

-- wallets: aggregate root and target of the per-wallet lock (SELECT ... FOR NO KEY UPDATE).

CREATE TABLE wallets (
    id            UUID        PRIMARY KEY,
    player_id     UUID        NOT NULL,
    currency      CHAR(3)     NOT NULL,
    balance_minor BIGINT      NOT NULL,
    version       BIGINT      NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallets_player_currency_uq UNIQUE (player_id, currency),
    CONSTRAINT wallets_id_currency_uq     UNIQUE (id, currency),
    CONSTRAINT wallets_currency_ck        CHECK (currency IN ('BRL', 'USD')),
    CONSTRAINT wallets_balance_nonneg_ck  CHECK (balance_minor >= 0),
    CONSTRAINT wallets_version_ck         CHECK (version >= 1)
);

CREATE FUNCTION wallets_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wallets: delete is not allowed'
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'wallets: initial version must be 1, got %', NEW.version
                USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;

    IF (NEW.id, NEW.player_id, NEW.currency, NEW.created_at)
       IS DISTINCT FROM (OLD.id, OLD.player_id, OLD.currency, OLD.created_at) THEN
        RAISE EXCEPTION 'wallets: identity columns are immutable (id %)', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF NEW.balance_minor <> OLD.balance_minor THEN
        IF NEW.version <> OLD.version + 1 THEN
            RAISE EXCEPTION 'wallets: balance change requires version % (got %)', OLD.version + 1, NEW.version
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

-- wager_transactions: the operation itself, idempotency keys and the state machine.

CREATE TABLE wager_transactions (
    id                                UUID        PRIMARY KEY,
    origin                            TEXT        NOT NULL,
    kind                              TEXT        NOT NULL,
    status                            TEXT        NOT NULL,
    wallet_id                         UUID        NOT NULL REFERENCES wallets (id),
    player_id                         UUID        NOT NULL,
    amount_minor                      BIGINT      NOT NULL,
    currency                          CHAR(3)     NOT NULL,

    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      TEXT,
    round_id                          TEXT,
    game_id                           TEXT,
    reference_external_transaction_id TEXT,
    reference_transaction_id          UUID        REFERENCES wager_transactions (id),

    failure_code                      TEXT,
    result_balance_minor              BIGINT,

    reference_attempts                INT         NOT NULL DEFAULT 0,
    next_attempt_at                   TIMESTAMPTZ,
    reference_deadline_at             TIMESTAMPTZ,

    correlation_id                    TEXT        NOT NULL,
    created_at                        TIMESTAMPTZ NOT NULL,
    updated_at                        TIMESTAMPTZ NOT NULL,
    completed_at                      TIMESTAMPTZ,

    CONSTRAINT wtx_id_wallet_currency_uq UNIQUE (id, wallet_id, currency),

    CONSTRAINT wtx_origin_ck   CHECK (origin IN ('INTERNAL', 'HTTP', 'SQS')),
    CONSTRAINT wtx_kind_ck     CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    CONSTRAINT wtx_status_ck   CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    CONSTRAINT wtx_currency_ck CHECK (currency IN ('BRL', 'USD')),

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

    CONSTRAINT wtx_amount_ck CHECK (CASE WHEN kind = 'LOSS' THEN amount_minor = 0 ELSE amount_minor > 0 END),

    CONSTRAINT wtx_reference_required_ck  CHECK (kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NOT NULL),
    CONSTRAINT wtx_reference_forbidden_ck CHECK (kind NOT IN ('BET', 'LOSS') OR reference_external_transaction_id IS NULL),
    CONSTRAINT wtx_no_self_reference_ck   CHECK (reference_transaction_id IS NULL OR reference_transaction_id <> id),

    CONSTRAINT wtx_completed_ck    CHECK ((completed_at IS NOT NULL) = (status IN ('PROCESSED', 'REJECTED', 'FAILED'))),
    CONSTRAINT wtx_failure_code_ck CHECK ((failure_code IS NOT NULL) = (status IN ('REJECTED', 'FAILED'))),
    CONSTRAINT wtx_failure_code_catalog_ck CHECK (failure_code IS NULL OR failure_code IN (
        'INSUFFICIENT_FUNDS', 'REVERSAL_INSUFFICIENT_FUNDS', 'BALANCE_LIMIT_EXCEEDED',
        'REFERENCE_NOT_FOUND', 'REFERENCE_NOT_PROCESSED', 'REFERENCE_MISMATCH',
        'REFERENCE_KIND_NOT_ALLOWED', 'ALREADY_REVERSED', 'CURRENCY_MISMATCH',
        'WALLET_PLAYER_MISMATCH', 'PERMANENT_FAILURE')),
    CONSTRAINT wtx_result_balance_ck CHECK ((result_balance_minor IS NOT NULL) = (status IN ('PROCESSED', 'REJECTED'))),
    CONSTRAINT wtx_result_balance_nonneg_ck CHECK (result_balance_minor IS NULL OR result_balance_minor >= 0),
    CONSTRAINT wtx_processed_reversal_resolved_ck CHECK (
        status <> 'PROCESSED' OR kind NOT IN ('REFUND', 'ROLLBACK') OR reference_transaction_id IS NOT NULL),
    CONSTRAINT wtx_pending_reference_ck CHECK (
        status <> 'PENDING_REFERENCE' OR (next_attempt_at IS NOT NULL AND reference_deadline_at IS NOT NULL)),
    CONSTRAINT wtx_reference_attempts_ck CHECK (reference_attempts >= 0),

    CONSTRAINT wtx_provider_idempotency_key_uq UNIQUE (provider_id, idempotency_key),
    CONSTRAINT wtx_provider_external_id_uq     UNIQUE (provider_id, external_transaction_id)
);

CREATE UNIQUE INDEX wtx_opening_per_wallet_uq
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

CREATE UNIQUE INDEX wtx_single_successful_reversal_uq
    ON wager_transactions (reference_transaction_id)
    WHERE kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED';

CREATE INDEX wtx_pending_reference_due_idx
    ON wager_transactions (next_attempt_at, id)
    WHERE status = 'PENDING_REFERENCE';

CREATE INDEX wtx_waiting_for_reference_idx
    ON wager_transactions (provider_id, reference_external_transaction_id)
    WHERE status = 'PENDING_REFERENCE';

CREATE INDEX wtx_wallet_idx ON wager_transactions (wallet_id, id);

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

    IF OLD.status = 'PENDING' AND NEW.status NOT IN ('PROCESSED', 'REJECTED', 'PENDING_REFERENCE') THEN
        RAISE EXCEPTION 'wager_transactions: PENDING cannot move to % (id %)', NEW.status, OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF OLD.status = 'PENDING_REFERENCE' AND NEW.status NOT IN ('PROCESSED', 'REJECTED', 'FAILED', 'PENDING_REFERENCE') THEN
        RAISE EXCEPTION 'wager_transactions: PENDING_REFERENCE cannot move to % (id %)', NEW.status, OLD.id
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

-- wallet_ledger_entries: append-only, one entry per balance movement.

CREATE TABLE wallet_ledger_entries (
    id                   UUID        PRIMARY KEY,
    wallet_id            UUID        NOT NULL,
    transaction_id       UUID        NOT NULL,
    direction            TEXT        NOT NULL,
    amount_minor         BIGINT      NOT NULL,
    currency             CHAR(3)     NOT NULL,
    balance_before_minor BIGINT      NOT NULL,
    balance_after_minor  BIGINT      NOT NULL,
    wallet_version       BIGINT      NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL,

    CONSTRAINT wle_wallet_transaction_uq UNIQUE (wallet_id, transaction_id),
    CONSTRAINT wle_wallet_version_uq     UNIQUE (wallet_id, wallet_version),
    CONSTRAINT wle_transaction_uq        UNIQUE (transaction_id),

    CONSTRAINT wle_wallet_currency_fk FOREIGN KEY (wallet_id, currency)
        REFERENCES wallets (id, currency),
    CONSTRAINT wle_transaction_wallet_fk FOREIGN KEY (transaction_id, wallet_id, currency)
        REFERENCES wager_transactions (id, wallet_id, currency),

    CONSTRAINT wle_currency_ck  CHECK (currency IN ('BRL', 'USD')),
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

-- Deferred checks: every balance change carries its entry, with the exact OLD and NEW values.

CREATE FUNCTION wallets_balance_change_check() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    entry wallet_ledger_entries%ROWTYPE;
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT * INTO entry
          FROM wallet_ledger_entries e
         WHERE e.wallet_id = NEW.id AND e.wallet_version = 1;

        IF NEW.balance_minor = 0 THEN
            IF FOUND THEN
                RAISE EXCEPTION 'wallet % opened with zero must not have a version 1 entry', NEW.id
                    USING ERRCODE = 'integrity_constraint_violation';
            END IF;
            RETURN NULL;
        END IF;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'wallet % opened with balance % has no opening entry', NEW.id, NEW.balance_minor
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;

        IF entry.direction <> 'CREDIT'
           OR entry.balance_before_minor <> 0
           OR entry.balance_after_minor <> NEW.balance_minor THEN
            RAISE EXCEPTION 'wallet % opening entry does not match the initial balance %', NEW.id, NEW.balance_minor
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;

        RETURN NULL;
    END IF;

    IF NEW.balance_minor = OLD.balance_minor THEN
        RETURN NULL;
    END IF;

    SELECT * INTO entry
      FROM wallet_ledger_entries e
     WHERE e.wallet_id = NEW.id AND e.wallet_version = NEW.version;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'wallet % reached version % without a matching ledger entry', NEW.id, NEW.version
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF entry.balance_before_minor <> OLD.balance_minor OR entry.balance_after_minor <> NEW.balance_minor THEN
        RAISE EXCEPTION 'wallet % moved % -> % but version % records % -> %',
            NEW.id, OLD.balance_minor, NEW.balance_minor, NEW.version,
            entry.balance_before_minor, entry.balance_after_minor
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER wallets_balance_change_check_trg
    AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallets_balance_change_check();

-- Deferred checks: every entry agrees with its operation and with the version chain of its wallet.

CREATE FUNCTION wallet_ledger_entries_chain_check() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    wallet_balance      BIGINT;
    wallet_version      BIGINT;
    operation           wager_transactions%ROWTYPE;
    reference           wager_transactions%ROWTYPE;
    expected_direction  TEXT;
    referenced_direction TEXT;
    neighbour_balance   BIGINT;
BEGIN
    SELECT w.balance_minor, w.version INTO wallet_balance, wallet_version
      FROM wallets w
     WHERE w.id = NEW.wallet_id
       FOR NO KEY UPDATE;

    IF NEW.wallet_version > wallet_version THEN
        RAISE EXCEPTION 'ledger entry for wallet % uses version % but the wallet is at version %',
            NEW.wallet_id, NEW.wallet_version, wallet_version
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    SELECT * INTO operation FROM wager_transactions t WHERE t.id = NEW.transaction_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ledger entry references unknown operation %', NEW.transaction_id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF operation.status <> 'PROCESSED' THEN
        RAISE EXCEPTION 'ledger entry requires a PROCESSED operation, % is % (id %)',
            operation.kind, operation.status, operation.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF operation.amount_minor <> NEW.amount_minor THEN
        RAISE EXCEPTION 'ledger entry moves % but operation % is worth %',
            NEW.amount_minor, operation.id, operation.amount_minor
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF operation.kind = 'ROLLBACK' THEN
        SELECT * INTO reference FROM wager_transactions t WHERE t.id = operation.reference_transaction_id;
        IF NOT FOUND OR reference.status <> 'PROCESSED'
           OR reference.wallet_id <> NEW.wallet_id OR reference.currency <> NEW.currency THEN
            RAISE EXCEPTION 'rollback % does not agree with its reference', operation.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;

        SELECT e.direction INTO referenced_direction
          FROM wallet_ledger_entries e
         WHERE e.transaction_id = reference.id;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'rollback % references operation % without a ledger entry', operation.id, reference.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;

        expected_direction := CASE referenced_direction WHEN 'CREDIT' THEN 'DEBIT' ELSE 'CREDIT' END;
    ELSE
        expected_direction := CASE operation.kind
            WHEN 'OPENING' THEN 'CREDIT'
            WHEN 'WIN'     THEN 'CREDIT'
            WHEN 'REFUND'  THEN 'CREDIT'
            WHEN 'BET'     THEN 'DEBIT'
        END;
    END IF;

    IF expected_direction IS NULL THEN
        RAISE EXCEPTION '% must not produce a ledger entry (id %)', operation.kind, operation.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF NEW.direction <> expected_direction THEN
        RAISE EXCEPTION 'ledger entry for % must be % and not %', operation.kind, expected_direction, NEW.direction
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF (NEW.wallet_version = 1) <> (operation.kind = 'OPENING') THEN
        RAISE EXCEPTION 'version 1 belongs to OPENING only, got % at version %', operation.kind, NEW.wallet_version
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF NEW.wallet_version = 1 THEN
        IF NEW.balance_before_minor <> 0 THEN
            RAISE EXCEPTION 'opening entry of wallet % must start at zero', NEW.wallet_id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
    ELSE
        SELECT e.balance_after_minor INTO neighbour_balance
          FROM wallet_ledger_entries e
         WHERE e.wallet_id = NEW.wallet_id AND e.wallet_version = NEW.wallet_version - 1;

        IF FOUND THEN
            IF neighbour_balance <> NEW.balance_before_minor THEN
                RAISE EXCEPTION 'wallet % version % starts at % but version % ended at %',
                    NEW.wallet_id, NEW.wallet_version, NEW.balance_before_minor,
                    NEW.wallet_version - 1, neighbour_balance
                    USING ERRCODE = 'integrity_constraint_violation';
            END IF;
        ELSIF NEW.wallet_version <> 2 OR NEW.balance_before_minor <> 0 THEN
            RAISE EXCEPTION 'wallet % has no entry for version %', NEW.wallet_id, NEW.wallet_version - 1
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
    END IF;

    IF NEW.wallet_version = wallet_version THEN
        IF NEW.balance_after_minor <> wallet_balance THEN
            RAISE EXCEPTION 'wallet % is at % but version % ends at %',
                NEW.wallet_id, wallet_balance, NEW.wallet_version, NEW.balance_after_minor
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
    ELSE
        SELECT e.balance_before_minor INTO neighbour_balance
          FROM wallet_ledger_entries e
         WHERE e.wallet_id = NEW.wallet_id AND e.wallet_version = NEW.wallet_version + 1;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'wallet % has no entry for version %', NEW.wallet_id, NEW.wallet_version + 1
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;

        IF neighbour_balance <> NEW.balance_after_minor THEN
            RAISE EXCEPTION 'wallet % version % ends at % but version % starts at %',
                NEW.wallet_id, NEW.wallet_version, NEW.balance_after_minor,
                NEW.wallet_version + 1, neighbour_balance
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER wallet_ledger_entries_chain_check_trg
    AFTER INSERT ON wallet_ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallet_ledger_entries_chain_check();

-- Deferred checks: the settled operation carries exactly the movement its outcome implies.

CREATE FUNCTION wager_transactions_settlement_check() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    operation      wager_transactions%ROWTYPE;
    entry_count    INT;
    expected_count INT;
BEGIN
    SELECT * INTO operation FROM wager_transactions t WHERE t.id = NEW.id;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    IF operation.status = 'PENDING' THEN
        RAISE EXCEPTION 'operation % cannot stay PENDING at commit', operation.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    SELECT count(*) INTO entry_count
      FROM wallet_ledger_entries e
     WHERE e.transaction_id = operation.id;

    expected_count := CASE
        WHEN operation.status = 'PROCESSED' AND operation.kind <> 'LOSS' THEN 1
        ELSE 0
    END;

    IF entry_count <> expected_count THEN
        RAISE EXCEPTION '% % (id %) requires % ledger entries and has %',
            operation.kind, operation.status, operation.id, expected_count, entry_count
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER wager_transactions_settlement_check_trg
    AFTER INSERT OR UPDATE ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wager_transactions_settlement_check();

-- inbox_messages: durable deduplication for the SQS consumer.

CREATE TABLE inbox_messages (
    consumer_name  TEXT        NOT NULL,
    message_id     TEXT        NOT NULL,
    payload_hash   TEXT        NOT NULL,
    transaction_id UUID        REFERENCES wager_transactions (id),
    outcome        TEXT,
    received_at    TIMESTAMPTZ NOT NULL,
    completed_at   TIMESTAMPTZ,

    CONSTRAINT inbox_pk PRIMARY KEY (consumer_name, message_id),
    CONSTRAINT inbox_payload_hash_ck CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT inbox_outcome_ck CHECK (outcome IS NULL OR outcome IN ('PROCESSED', 'REJECTED', 'PENDING_REFERENCE', 'REPLAY')),
    CONSTRAINT inbox_completed_ck CHECK ((completed_at IS NULL) = (outcome IS NULL))
);

-- outbox_events: the event is written in the same commit as the balance and published later.

CREATE TABLE outbox_events (
    id              UUID        PRIMARY KEY,
    aggregate_type  TEXT        NOT NULL,
    aggregate_id    UUID        NOT NULL,
    event_type      TEXT        NOT NULL,
    event_version   INT         NOT NULL,
    correlation_id  TEXT        NOT NULL,
    causation_id    TEXT,
    partition_key   TEXT        NOT NULL,
    payload         JSONB       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,

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

CREATE INDEX outbox_pending_idx
    ON outbox_events (next_attempt_at, id)
    WHERE published_at IS NULL;

CREATE INDEX outbox_aggregate_idx ON outbox_events (aggregate_id, id);

CREATE FUNCTION outbox_events_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'outbox_events: delete is not allowed'
            USING ERRCODE = 'restrict_violation';
    END IF;

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
    BEFORE UPDATE OR DELETE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_events_guard();

-- Runtime role: no DELETE or TRUNCATE anywhere, ledger restricted to INSERT and SELECT.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'wallet_app') THEN
        GRANT SELECT, INSERT, UPDATE ON wallets, wager_transactions, inbox_messages, outbox_events TO wallet_app;
        GRANT SELECT, INSERT         ON wallet_ledger_entries TO wallet_app;
    END IF;
END;
$$;

COMMIT;
