-- 000001_init.down.sql
-- Reverte a migration inicial. Ordem inversa das dependências.
-- DROP TABLE não dispara os triggers de proteção (eles cobrem UPDATE/DELETE/TRUNCATE).

BEGIN;

DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS inbox_messages;
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;

DROP FUNCTION IF EXISTS outbox_events_guard();
DROP FUNCTION IF EXISTS wallets_require_ledger_entry();
DROP FUNCTION IF EXISTS wallet_ledger_entries_immutable();
DROP FUNCTION IF EXISTS wager_transactions_guard();
DROP FUNCTION IF EXISTS wallets_guard();

COMMIT;
