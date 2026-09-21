#!/usr/bin/env bash
# Checagens complementares (validação de 20/09/2026). Sobe um Postgres 16 descartável, aplica
# plan-section-6.up.sql e conc.sql, roda as checagens e remove o container.
set -u
docker rm -f jg-pgcheck >/dev/null 2>&1
docker run -d --name jg-pgcheck -e POSTGRES_PASSWORD=pg postgres:16-alpine >/dev/null
timeout 40 sh -c 'until docker exec jg-pgcheck pg_isready -U postgres -q 2>/dev/null; do sleep 0.5; done'
docker cp plan-section-6.up.sql jg-pgcheck:/tmp/up.sql >/dev/null; docker cp conc.sql jg-pgcheck:/tmp/conc.sql >/dev/null
q() { docker exec jg-pgcheck psql -U postgres -Atq --set=VERBOSITY=verbose "$@" 2>&1; }
own() { q -c "$1"; }
app() { q -c "SET ROLE wallet_app; $1"; }
own "CREATE ROLE wallet_app LOGIN PASSWORD 'app'" >/dev/null
q -v ON_ERROR_STOP=1 -f /tmp/up.sql && q -v ON_ERROR_STOP=1 -f /tmp/conc.sql
w=00000000-0000-7000-8000-000000000001; p=00000000-0000-7000-8000-00000000aa01
app "SELECT setup_wallet('$w','$p',10000)" >/dev/null
echo "bet A: $(app "SELECT run_bet('$w','$p','tx-a1','key-a1','HTTP',8000,'NO KEY UPDATE',0)")   bet B: $(app "SELECT run_bet('$w','$p','tx-b1','key-b1','SQS',8000,'NO KEY UPDATE',0)")"
bet_id=$(own "SELECT id FROM wager_transactions WHERE external_transaction_id='tx-a1'")
app "INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency, provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id, reference_external_transaction_id, reference_transaction_id, result_balance_minor, correlation_id, created_at, updated_at, completed_at) VALUES (gen_random_uuid(),'HTTP','REFUND','PROCESSED','$w','$p',8000,'BRL','provider-a','rf-1','rf-1',repeat('b',64),'round-1','game-1','tx-a1','$bet_id',2000,'c',now(),now(),now())" >/dev/null
refund_id=$(own "SELECT id FROM wager_transactions WHERE external_transaction_id='rf-1'")
echo "d13 ID externo tx-a1 reaplicado com outra chave -> $(app "WITH ins AS (INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency, provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id, correlation_id, created_at, updated_at) VALUES (gen_random_uuid(),'HTTP','BET','PENDING','$w','$p',100,'BRL','provider-a','tx-a1','outra-chave',repeat('a',64),'round-1','game-1','c',now(),now()) ON CONFLICT DO NOTHING RETURNING id) SELECT 'inserted='||(SELECT count(*) FROM ins)||' by_key='||(SELECT count(*) FROM wager_transactions WHERE provider_id='provider-a' AND idempotency_key='outra-chave')||' by_external_id='||(SELECT count(*) FROM wager_transactions WHERE provider_id='provider-a' AND external_transaction_id='tx-a1')")  => 409, so detectavel consultando os DOIS uniques"
echo "d16 moeda divergente no ledger (FK composta):   $(app "INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, wallet_version, created_at) VALUES (gen_random_uuid(),'$w','$refund_id','CREDIT',1,'USD',0,1,99,now())" | grep -m1 ERROR)"
echo "d17 aritmetica errada no ledger:                $(app "INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, wallet_version, created_at) VALUES (gen_random_uuid(),'$w','$refund_id','CREDIT',1,'BRL',0,5,99,now())" | grep -m1 ERROR)"
echo "d18 outbox: reescrever payload:                 $(app "UPDATE outbox_events SET payload='{\"x\":1}'::jsonb WHERE partition_key='$w'" | grep -m1 ERROR)"
echo "d19 outbox: claim SKIP LOCKED + lease:          $(app "WITH c AS (UPDATE outbox_events SET locked_by='inst-1', locked_until=now()+interval '30 seconds', attempts=attempts+1 WHERE id IN (SELECT id FROM outbox_events WHERE published_at IS NULL AND next_attempt_at<=now() AND (locked_until IS NULL OR locked_until<now()) ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 2) RETURNING id) SELECT 'claimed='||count(*)||' of '||(SELECT count(*) FROM outbox_events WHERE published_at IS NULL) FROM c"); $(app "WITH p AS (UPDATE outbox_events SET published_at=now(), locked_by=NULL, locked_until=NULL WHERE locked_by='inst-1' RETURNING id) SELECT 'published='||count(*) FROM p"); segundo claim ve so o resto: $(app "SELECT 'claimable_now='||count(*) FROM outbox_events WHERE published_at IS NULL AND (locked_until IS NULL OR locked_until<now())")"
echo "d20 outbox: reescrever published_at:            $(app "UPDATE outbox_events SET published_at=now() WHERE published_at IS NOT NULL" | grep -m1 ERROR)"
echo "d21 outbox: lease incoerente (locked_by sem until): $(app "UPDATE outbox_events SET locked_by='x' WHERE published_at IS NULL" | grep -m1 ERROR)"
app "INSERT INTO inbox_messages (consumer_name,message_id,payload_hash,received_at) VALUES ('wager-consumer','msg-1',repeat('c',64),now()) ON CONFLICT DO NOTHING" >/dev/null
echo "d22 inbox: reentrega do mesmo messageId:        $(app "WITH i AS (INSERT INTO inbox_messages (consumer_name,message_id,payload_hash,received_at) VALUES ('wager-consumer','msg-1',repeat('c',64),now()) ON CONFLICT DO NOTHING RETURNING message_id) SELECT 'second_insert_rows='||count(*) FROM i")"
echo "d23 wake-up de pendentes com SKIP LOCKED (0 pendentes hoje): $(app "WITH u AS (UPDATE wager_transactions SET next_attempt_at=now() WHERE id IN (SELECT id FROM wager_transactions WHERE status='PENDING_REFERENCE' AND provider_id='provider-a' AND reference_external_transaction_id='tx-a1' FOR UPDATE SKIP LOCKED) RETURNING id) SELECT 'woken='||count(*) FROM u")"
docker rm -f jg-pgcheck >/dev/null 2>&1 && echo "container removido"
