-- Fluxo central do plano (§6.5) executado dentro do Postgres, para os cenários de concorrência.
-- Requer a migration da seção 6 aplicada e o papel wallet_app existente.
CREATE OR REPLACE FUNCTION setup_wallet(w uuid, p uuid, initial bigint) RETURNS void LANGUAGE plpgsql AS $$
DECLARE t uuid := gen_random_uuid();
BEGIN
  INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
  VALUES (w, p, 'BRL', initial, 1, now(), now());
  IF initial > 0 THEN
    INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
                                    result_balance_minor, correlation_id, created_at, updated_at, completed_at)
    VALUES (t, 'INTERNAL', 'OPENING', 'PROCESSED', w, p, initial, 'BRL', initial, 'corr-open', now(), now(), now());
    INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
                                       balance_before_minor, balance_after_minor, wallet_version, created_at)
    VALUES (gen_random_uuid(), w, t, 'CREDIT', initial, 'BRL', 0, initial, 1, now());
    INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, event_version, correlation_id, partition_key, payload, occurred_at, next_attempt_at)
    VALUES (gen_random_uuid(), 'wager_transaction', t, 'WagerTransactionProcessed', 1, 'corr-open', w::text, '{}'::jsonb, now(), now()),
           (gen_random_uuid(), 'wallet', w, 'WalletBalanceChanged', 1, 'corr-open', w::text, '{}'::jsonb, now(), now());
  END IF;
END $$;

-- BET: insert antes do lock (idempotência), sleep para forçar a disputa, lock, decide, commit.
CREATE OR REPLACE FUNCTION run_bet(w uuid, p uuid, ext text, key text, origin text, amount bigint, lockmode text, delay float)
RETURNS text LANGUAGE plpgsql AS $$
DECLARE tid uuid := gen_random_uuid(); bal bigint; ver bigint; existing record;
BEGIN
  INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency, provider_id,
      external_transaction_id, idempotency_key, payload_hash, round_id, game_id, correlation_id, created_at, updated_at)
  VALUES (tid, origin, 'BET', 'PENDING', w, p, amount, 'BRL', 'provider-a', ext, key, repeat('a', 64), 'round-1', 'game-1', 'corr-' || ext, now(), now())
  ON CONFLICT DO NOTHING;
  IF NOT FOUND THEN
    SELECT status, result_balance_minor INTO existing FROM wager_transactions WHERE provider_id = 'provider-a' AND idempotency_key = key;
    RETURN 'REPLAY:' || existing.status || ':' || coalesce(existing.result_balance_minor::text, 'null');
  END IF;
  PERFORM pg_sleep(delay);
  EXECUTE format('SELECT balance_minor, version FROM wallets WHERE id = $1 FOR %s', lockmode) INTO bal, ver USING w;
  IF bal >= amount THEN
    INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, wallet_version, created_at)
    VALUES (gen_random_uuid(), w, tid, 'DEBIT', amount, 'BRL', bal, bal - amount, ver + 1, now());
    UPDATE wallets SET balance_minor = bal - amount, version = version + 1, updated_at = now() WHERE id = w AND version = ver;
    IF NOT FOUND THEN RAISE EXCEPTION 'version conflict'; END IF;
    UPDATE wager_transactions SET status = 'PROCESSED', result_balance_minor = bal - amount, completed_at = now(), updated_at = now() WHERE id = tid;
    INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, event_version, correlation_id, partition_key, payload, occurred_at, next_attempt_at)
    VALUES (gen_random_uuid(), 'wager_transaction', tid, 'WagerTransactionProcessed', 1, 'corr-' || ext, w::text, '{}'::jsonb, now(), now()),
           (gen_random_uuid(), 'wallet', w, 'WalletBalanceChanged', 1, 'corr-' || ext, w::text, '{}'::jsonb, now(), now());
    RETURN 'PROCESSED:' || (bal - amount);
  ELSE
    UPDATE wager_transactions SET status = 'REJECTED', failure_code = 'INSUFFICIENT_FUNDS', result_balance_minor = bal, completed_at = now(), updated_at = now() WHERE id = tid;
    INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, event_version, correlation_id, partition_key, payload, occurred_at, next_attempt_at)
    VALUES (gen_random_uuid(), 'wager_transaction', tid, 'WagerTransactionRejected', 1, 'corr-' || ext, w::text, '{}'::jsonb, now(), now());
    RETURN 'REJECTED:' || bal;
  END IF;
END $$;
