#!/usr/bin/env bash
# Cenários de concorrência e proteções do schema (validação de 20/09/2026).
# Pré-requisitos: container Postgres 16 chamado jg-pgcheck, migration da seção 6 aplicada,
# papel wallet_app criado, conc.sql neste diretório.
set -u
PSQL="docker exec jg-pgcheck psql -U postgres -Atq --set=VERBOSITY=verbose"
app() { $PSQL -c "SET ROLE wallet_app; $1" 2>&1; }
own() { $PSQL -c "$1" 2>&1; }
W() { printf '00000000-0000-7000-8000-0000000000%02x' "$1"; }
P() { printf '00000000-0000-7000-8000-00000000aa%02x' "$1"; }
bet() { app "SELECT run_bet('$1','$2','$3','$4','$5',$6,'$7',$8)"; }
summary() { own "SELECT 'balance='||balance_minor||' version='||version FROM wallets WHERE id='$1'";
            own "SELECT 'wtx: '||string_agg(kind||'/'||status||'/'||coalesce(origin,'')||'/'||coalesce(result_balance_minor::text,'-'), ' | ' ORDER BY created_at) FROM wager_transactions WHERE wallet_id='$1'";
            own "SELECT 'ledger: '||count(*)||' entries, debits='||count(*) FILTER (WHERE direction='DEBIT')||' credits='||count(*) FILTER (WHERE direction='CREDIT') FROM wallet_ledger_entries WHERE wallet_id='$1'";
            own "SELECT 'outbox: '||string_agg(event_type||'x'||n, ', ' ORDER BY event_type) FROM (SELECT event_type, count(*) n FROM outbox_events WHERE partition_key='$1' GROUP BY 1) s"; }

docker cp conc.sql jg-pgcheck:/tmp/conc.sql && $PSQL -v ON_ERROR_STOP=1 -f /tmp/conc.sql

echo "##### A) 100.00, duas BETs de 80.00 em paralelo, HTTP x SQS, FOR NO KEY UPDATE (3 rodadas)"
for r in 1 2 3; do
  w=$(W $r); p=$(P $r); app "SELECT setup_wallet('$w','$p',10000)" >/dev/null
  bet "$w" "$p" "tx-a$r" "key-a$r" HTTP 8000 'NO KEY UPDATE' 1.5 > /tmp/ra.$r & bet "$w" "$p" "tx-b$r" "key-b$r" SQS 8000 'NO KEY UPDATE' 1.5 > /tmp/rb.$r & wait
  echo "-- rodada $r: HTTP=$(cat /tmp/ra.$r) SQS=$(cat /tmp/rb.$r)"; summary "$w"
done

echo "##### B) mesmo cenário com FOR UPDATE (esperado: deadlock 40P01 numa das sessões)"
w=$(W 10); p=$(P 10); app "SELECT setup_wallet('$w','$p',10000)" >/dev/null
bet "$w" "$p" tx-a10 key-a10 HTTP 8000 'UPDATE' 1.5 > /tmp/ra.10 & bet "$w" "$p" tx-b10 key-b10 SQS 8000 'UPDATE' 1.5 > /tmp/rb.10 & wait
echo "-- HTTP: $(head -1 /tmp/ra.10)"; echo "-- SQS : $(head -1 /tmp/rb.10)"; summary "$w"

echo "##### C) mesma BET (mesma chave) 30x em paralelo"
w=$(W 20); p=$(P 20); app "SELECT setup_wallet('$w','$p',10000)" >/dev/null
for i in $(seq 1 30); do bet "$w" "$p" tx-same key-same HTTP 2500 'NO KEY UPDATE' 1.0 > /tmp/rc.$i & done; wait
cat /tmp/rc.* | sort | uniq -c; summary "$w"
own "SELECT 'wtx rows com key-same: '||count(*) FROM wager_transactions WHERE idempotency_key='key-same'"

echo "##### D) proteções do schema (cada linha deve FALHAR com o erro esperado)"
w=$(W 1)
echo "d1 saldo muda sem ledger (falha no COMMIT):        $(app "UPDATE wallets SET balance_minor=balance_minor-100, version=version+1, updated_at=now() WHERE id='$w'" | grep -m1 ERROR)"
echo "d2 saldo muda sem version+1:                       $(app "UPDATE wallets SET balance_minor=balance_minor-100 WHERE id='$w'" | grep -m1 ERROR)"
echo "d3 version muda sem saldo:                         $(app "UPDATE wallets SET version=version+1 WHERE id='$w'" | grep -m1 ERROR)"
echo "d4 saldo negativo direto:                          $(app "UPDATE wallets SET balance_minor=-1, version=version+1 WHERE id='$w'" | grep -m1 ERROR)"
echo "d5 UPDATE ledger como wallet_app:                  $(app "UPDATE wallet_ledger_entries SET amount_minor=1 WHERE wallet_id='$w'" | grep -m1 ERROR)"
echo "d6 UPDATE ledger como dono:                        $(own "UPDATE wallet_ledger_entries SET amount_minor=1 WHERE wallet_id='$w'" | grep -m1 ERROR)"
echo "d7 DELETE ledger como dono:                        $(own "DELETE FROM wallet_ledger_entries WHERE wallet_id='$w'" | grep -m1 ERROR)"
echo "d8 TRUNCATE ledger como dono:                      $(own "TRUNCATE wallet_ledger_entries" | grep -m1 ERROR)"
echo "d9 DELETE wallet como dono:                        $(own "DELETE FROM wallets WHERE id='$w'" | grep -m1 ERROR)"
echo "d10 transição de estado terminal:                  $(app "UPDATE wager_transactions SET status='PENDING', completed_at=NULL, failure_code=NULL WHERE wallet_id='$w' AND status='REJECTED'" | grep -m1 ERROR)"
echo "d11 segundo OPENING na carteira:                   $(app "INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency, result_balance_minor, correlation_id, created_at, updated_at, completed_at) VALUES (gen_random_uuid(),'INTERNAL','OPENING','PROCESSED','$w','$(P 1)',1,'BRL',1,'c',now(),now(),now())" | grep -m1 ERROR)"
echo "d12 OPENING vindo por HTTP:                        $(app "INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency, result_balance_minor, correlation_id, created_at, updated_at, completed_at) VALUES (gen_random_uuid(),'HTTP','OPENING','PROCESSED','$w','$(P 1)',1,'BRL',1,'c',now(),now(),now())" | grep -m1 ERROR)"
bet_id=$(own "SELECT id FROM wager_transactions WHERE wallet_id='$w' AND kind='BET' AND status='PROCESSED' LIMIT 1")
rb() { app "INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency, provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id, reference_external_transaction_id, reference_transaction_id, result_balance_minor, correlation_id, created_at, updated_at, completed_at) VALUES (gen_random_uuid(),'HTTP','$1','PROCESSED','$w','$(P 1)',8000,'BRL','provider-a','$2','$2',repeat('b',64),'round-1','game-1','tx-a1','$bet_id',2000,'c',now(),now(),now())"; }
echo "d14 primeira reversão PROCESSED (deve passar):      $(rb REFUND rf-1 | grep -m1 -E 'ERROR|INSERT')"
echo "d15 segunda reversão PROCESSED da mesma BET:        $(rb ROLLBACK rb-1 | grep -m1 ERROR)"

echo "##### E) reconciliação da carteira 1 (REPEATABLE READ READ ONLY)"
own "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SELECT 'stored='||w.balance_minor||' calculated='||COALESCE(SUM(CASE e.direction WHEN 'CREDIT' THEN e.amount_minor ELSE -e.amount_minor END),0)||' entries='||count(e.id) FROM wallets w LEFT JOIN wallet_ledger_entries e ON e.wallet_id=w.id WHERE w.id='$w' GROUP BY w.balance_minor; COMMIT;"

echo "##### F) planos com enable_seqscan=off (índices parciais aplicáveis?)"
own "SET enable_seqscan=off; EXPLAIN (COSTS OFF) SELECT id FROM outbox_events WHERE published_at IS NULL AND next_attempt_at <= now() AND (locked_until IS NULL OR locked_until < now()) ORDER BY next_attempt_at, id LIMIT 10" | grep -i -E 'index|scan' | head -3
own "SET enable_seqscan=off; EXPLAIN (COSTS OFF) SELECT id FROM wager_transactions WHERE status='PENDING_REFERENCE' AND next_attempt_at <= now() ORDER BY next_attempt_at, id LIMIT 10" | grep -i -E 'index|scan' | head -3
own "SET enable_seqscan=off; EXPLAIN (COSTS OFF) SELECT id FROM wallet_ledger_entries WHERE wallet_id='$w' AND wallet_version > 1 ORDER BY wallet_version LIMIT 50" | grep -i -E 'index|scan' | head -3
