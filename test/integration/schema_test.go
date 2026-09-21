//go:build integration

package integration_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

// ledger is the vocabulary of the schema regressions: every scenario writes raw SQL
// as the runtime role and states which protection has to refuse it.
type ledger struct {
	t   *testing.T
	env *testenv.Env
}

func newLedger(t *testing.T) *ledger {
	return &ledger{t: t, env: testenv.New(t)}
}

func (l *ledger) id() string {
	return uuid.Must(uuid.NewV7()).String()
}

func (l *ledger) openWallet(walletID string, playerID string, balanceMinor int64) string {
	l.t.Helper()

	script := fmt.Sprintf(`
		INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ('%s', '%s', 'BRL', %d, 1, now(), now());`, walletID, playerID, balanceMinor)

	transactionID := l.id()
	if balanceMinor > 0 {
		script += fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				result_balance_minor, correlation_id, created_at, updated_at, completed_at)
			VALUES ('%s', 'INTERNAL', 'OPENING', 'PROCESSED', '%s', '%s', %d, 'BRL', %d, 'schema', now(), now(), now());
			INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
				balance_before_minor, balance_after_minor, wallet_version, created_at)
			VALUES ('%s', '%s', '%s', 'CREDIT', %d, 'BRL', 0, %d, 1, now());`,
			transactionID, walletID, playerID, balanceMinor, balanceMinor,
			l.id(), walletID, transactionID, balanceMinor, balanceMinor)
	}

	if err := l.env.DB.Attempt(script); err != nil {
		l.t.Fatalf("a legitimate opening was refused: %v", err)
	}
	return transactionID
}

func (l *ledger) bet(walletID string, playerID string, external string, amountMinor int64, before int64, version int64) string {
	transactionID := l.id()
	return fmt.Sprintf(`
		INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
			provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id, correlation_id, created_at, updated_at)
		VALUES ('%s', 'HTTP', 'BET', 'PENDING', '%s', '%s', %d, 'BRL', 'provider-a', '%s', 'key-%s', repeat('a', 64), 'r', 'g', 'schema', now(), now());
		INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
			balance_before_minor, balance_after_minor, wallet_version, created_at)
		VALUES ('%s', '%s', '%s', 'DEBIT', %d, 'BRL', %d, %d, %d, now());
		UPDATE wallets SET balance_minor = %d, version = %d, updated_at = now() WHERE id = '%s' AND version = %d;
		UPDATE wager_transactions SET status = 'PROCESSED', result_balance_minor = %d, completed_at = now(), updated_at = now() WHERE id = '%s';`,
		transactionID, walletID, playerID, amountMinor, external, external,
		l.id(), walletID, transactionID, amountMinor, before, before-amountMinor, version,
		before-amountMinor, version, walletID, version-1,
		before-amountMinor, transactionID)
}

func (l *ledger) refuses(name string, script string) {
	l.t.Helper()

	if err := l.env.DB.Attempt(script); err == nil {
		l.t.Errorf("%s: the write was accepted, the schema should have refused it", name)
	} else if testing.Verbose() {
		l.t.Logf("%s refused with: %s", name, firstLine(err.Error()))
	}
}

func (l *ledger) accepts(name string, script string) {
	l.t.Helper()

	if err := l.env.DB.Attempt(script); err != nil {
		l.t.Errorf("%s: a legitimate write was refused: %v", name, err)
	}
}

func firstLine(text string) string {
	if index := strings.Index(text, "\n"); index >= 0 {
		return text[:index]
	}
	return text
}

func TestSchemaFinancialInvariants(t *testing.T) {
	t.Run("Given a consistent wallet/When legitimate writes arrive/Then the schema accepts them", func(t *testing.T) {
		// Given
		l := newLedger(t)
		walletID, playerID := l.id(), l.id()
		l.openWallet(walletID, playerID, 10000)

		// When, Then
		l.accepts("one bet", l.bet(walletID, playerID, "bet-1", 2500, 10000, 2))
		l.accepts("two movements in the same commit",
			l.bet(walletID, playerID, "bet-2", 1000, 7500, 3)+l.bet(walletID, playerID, "bet-3", 500, 6500, 4))

		balance := l.env.DB.Value("SELECT balance_minor::text FROM wallets WHERE id = $1", walletID)
		version := l.env.DB.Value("SELECT version::text FROM wallets WHERE id = $1", walletID)
		if balance != "6000" || version != "4" {
			t.Errorf("wallet = %s at version %s, want 6000 at version 4", balance, version)
		}
		if entries := l.env.DB.Count("SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", walletID); entries != 4 {
			t.Errorf("entries = %d, want 4", entries)
		}
	})

	t.Run("Given a wallet opened with zero/When its first movement arrives/Then version two starts at zero", func(t *testing.T) {
		// Given
		l := newLedger(t)
		walletID, playerID := l.id(), l.id()
		l.openWallet(walletID, playerID, 0)

		// When
		transactionID := l.id()
		script := fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id, correlation_id, created_at, updated_at)
			VALUES ('%s', 'HTTP', 'WIN', 'PENDING', '%s', '%s', 5000, 'BRL', 'provider-a', 'win-1', 'key-win-1', repeat('a', 64), 'r', 'g', 'schema', now(), now());
			INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
				balance_before_minor, balance_after_minor, wallet_version, created_at)
			VALUES ('%s', '%s', '%s', 'CREDIT', 5000, 'BRL', 0, 5000, 2, now());
			UPDATE wallets SET balance_minor = 5000, version = 2, updated_at = now() WHERE id = '%s' AND version = 1;
			UPDATE wager_transactions SET status = 'PROCESSED', result_balance_minor = 5000, completed_at = now(), updated_at = now() WHERE id = '%s';`,
			transactionID, walletID, playerID, l.id(), walletID, transactionID, walletID, transactionID)

		// Then
		l.accepts("first movement of a wallet opened with zero", script)
		l.refuses("retroactive opening of a wallet opened with zero", fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				result_balance_minor, correlation_id, created_at, updated_at, completed_at)
			VALUES ('%s', 'INTERNAL', 'OPENING', 'PROCESSED', '%s', '%s', 10000, 'BRL', 10000, 'schema', now(), now(), now());
			INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
				balance_before_minor, balance_after_minor, wallet_version, created_at)
			VALUES ('%s', '%s', '%s', 'CREDIT', 10000, 'BRL', 0, 10000, 1, now());`,
			l.id(), walletID, playerID, l.id(), walletID, l.id()))
	})

	t.Run("Given direct SQL/When the balance and the ledger disagree/Then the commit fails atomically", func(t *testing.T) {
		// Given
		l := newLedger(t)
		walletID, playerID := l.id(), l.id()
		openingID := l.openWallet(walletID, playerID, 10000)
		otherWallet, otherPlayer := l.id(), l.id()
		l.openWallet(otherWallet, otherPlayer, 0)

		operation := func(kind string, amount int64, external string) (string, string) {
			transactionID := l.id()
			return transactionID, fmt.Sprintf(`
				INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
					provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id, correlation_id, created_at, updated_at)
				VALUES ('%s', 'HTTP', '%s', 'PENDING', '%s', '%s', %d, 'BRL', 'provider-a', '%s', 'key-%s', repeat('a', 64), 'r', 'g', 'schema', now(), now());`,
				transactionID, kind, walletID, playerID, amount, external, external)
		}

		settle := func(transactionID string, balance int64) string {
			return fmt.Sprintf(`UPDATE wager_transactions SET status = 'PROCESSED', result_balance_minor = %d,
				completed_at = now(), updated_at = now() WHERE id = '%s';`, balance, transactionID)
		}
		entry := func(transactionID string, direction string, amount int64, before int64, after int64, version int64) string {
			return fmt.Sprintf(`
				INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
					balance_before_minor, balance_after_minor, wallet_version, created_at)
				VALUES ('%s', '%s', '%s', '%s', %d, 'BRL', %d, %d, %d, now());`,
				l.id(), walletID, transactionID, direction, amount, before, after, version)
		}
		moveWallet := func(balance int64, version int64) string {
			return fmt.Sprintf(`UPDATE wallets SET balance_minor = %d, version = %d, updated_at = now() WHERE id = '%s';`,
				balance, version, walletID)
		}

		// When, Then
		forged, script := operation("BET", 2500, "forged")
		l.refuses("balance before taken out of thin air",
			script+entry(forged, "DEBIT", 2500, 11500, 9000, 2)+moveWallet(9000, 2)+settle(forged, 9000))

		noEntry, script := operation("BET", 2500, "no-entry")
		l.refuses("balance moves without a ledger entry", script+moveWallet(7500, 2)+settle(noEntry, 7500))

		noWallet, script := operation("BET", 2500, "no-wallet")
		l.refuses("ledger entry without moving the wallet", script+entry(noWallet, "DEBIT", 2500, 10000, 7500, 2)+settle(noWallet, 7500))

		wrongDirection, script := operation("BET", 2500, "wrong-direction")
		l.refuses("bet recorded as a credit",
			script+entry(wrongDirection, "CREDIT", 2500, 10000, 12500, 2)+moveWallet(12500, 2)+settle(wrongDirection, 12500))

		wrongAmount, script := operation("BET", 2500, "wrong-amount")
		l.refuses("entry worth less than the operation",
			script+entry(wrongAmount, "DEBIT", 1000, 10000, 9000, 2)+moveWallet(9000, 2)+settle(wrongAmount, 9000))

		gap, script := operation("BET", 2500, "gap")
		l.refuses("entry at a version the wallet never reached",
			script+entry(gap, "DEBIT", 2500, 10000, 7500, 4)+settle(gap, 7500))

		l.refuses("operation of one wallet reused by another", fmt.Sprintf(`
			INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
				balance_before_minor, balance_after_minor, wallet_version, created_at)
			VALUES ('%s', '%s', '%s', 'CREDIT', 10000, 'BRL', 0, 10000, 2, now());
			UPDATE wallets SET balance_minor = 10000, version = 2, updated_at = now() WHERE id = '%s';`,
			l.id(), otherWallet, openingID, otherWallet))

		lossID, script := operation("LOSS", 0, "loss-with-entry")
		l.refuses("loss carrying a credit", script+entry(lossID, "CREDIT", 500, 10000, 10500, 2)+moveWallet(10500, 2)+settle(lossID, 10500))

		missing, script := operation("BET", 2500, "settled-without-entry")
		l.refuses("bet settled without its entry", script+settle(missing, 7500))

		tampered, script := operation("BET", 2500, "tampered")
		l.refuses("intermediate balance tampered with",
			script+entry(tampered, "DEBIT", 2500, 10000, 7500, 2)+moveWallet(7000, 2)+settle(tampered, 7500))

		// the wallet never moved through any of the refused writes
		if balance := l.env.DB.Value("SELECT balance_minor::text FROM wallets WHERE id = $1", walletID); balance != "10000" {
			t.Errorf("balance = %s, want the untouched 10000", balance)
		}
		if version := l.env.DB.Value("SELECT version::text FROM wallets WHERE id = $1", walletID); version != "1" {
			t.Errorf("version = %s, want 1", version)
		}
	})

	t.Run("Given the state machine/When a forbidden transition is attempted/Then the trigger refuses it", func(t *testing.T) {
		// Given
		l := newLedger(t)
		walletID, playerID := l.id(), l.id()
		openingID := l.openWallet(walletID, playerID, 10000)

		pendingID := l.id()
		l.accepts("a reversal waiting for its reference", fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
				reference_external_transaction_id, next_attempt_at, reference_deadline_at, correlation_id, created_at, updated_at)
			VALUES ('%s', 'HTTP', 'ROLLBACK', 'PENDING_REFERENCE', '%s', '%s', 2500, 'BRL', 'provider-a', 'pending-1', 'key-pending-1',
				repeat('a', 64), 'r', 'g', 'missing', now(), now() + interval '15 minutes', 'schema', now(), now());`,
			pendingID, walletID, playerID))

		// When, Then
		l.accepts("rescheduling keeps the status", fmt.Sprintf(
			`UPDATE wager_transactions SET reference_attempts = reference_attempts + 1, next_attempt_at = now() + interval '2 seconds', updated_at = now() WHERE id = '%s';`, pendingID))
		l.refuses("a pending reference going back to pending", fmt.Sprintf(
			`UPDATE wager_transactions SET status = 'PENDING', next_attempt_at = NULL, reference_deadline_at = NULL, updated_at = now() WHERE id = '%s';`, pendingID))
		l.refuses("a terminal operation being changed", fmt.Sprintf(
			`UPDATE wager_transactions SET result_balance_minor = 1, updated_at = now() WHERE id = '%s';`, openingID))
		l.refuses("an operation left pending at commit", fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id, correlation_id, created_at, updated_at)
			VALUES ('%s', 'HTTP', 'BET', 'PENDING', '%s', '%s', 2500, 'BRL', 'provider-a', 'left-pending', 'key-left-pending', repeat('a', 64), 'r', 'g', 'schema', now(), now());`,
			l.id(), walletID, playerID))
		l.refuses("a second opening for the same wallet", fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				result_balance_minor, correlation_id, created_at, updated_at, completed_at)
			VALUES ('%s', 'INTERNAL', 'OPENING', 'PROCESSED', '%s', '%s', 1, 'BRL', 1, 'schema', now(), now(), now());`,
			l.id(), walletID, playerID))
		l.refuses("an opening claiming an external origin", fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				result_balance_minor, correlation_id, created_at, updated_at, completed_at)
			VALUES ('%s', 'HTTP', 'OPENING', 'PROCESSED', '%s', '%s', 1, 'BRL', 1, 'schema', now(), now(), now());`,
			l.id(), walletID, playerID))
	})

	t.Run("Given the column contracts/When a value falls outside them/Then the check refuses it", func(t *testing.T) {
		// Given
		l := newLedger(t)
		walletID, playerID := l.id(), l.id()
		l.openWallet(walletID, playerID, 10000)

		// When, Then
		l.refuses("failure code outside the catalogue", fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
				failure_code, result_balance_minor, correlation_id, created_at, updated_at, completed_at)
			VALUES ('%s', 'HTTP', 'BET', 'REJECTED', '%s', '%s', 2500, 'BRL', 'provider-a', 'x1', 'key-x1', repeat('a', 64), 'r', 'g',
				'SOMETHING_ELSE', 10000, 'schema', now(), now(), now());`, l.id(), walletID, playerID))

		l.refuses("rejection without the balance it observed", fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
				failure_code, correlation_id, created_at, updated_at, completed_at)
			VALUES ('%s', 'HTTP', 'BET', 'REJECTED', '%s', '%s', 2500, 'BRL', 'provider-a', 'x2', 'key-x2', repeat('a', 64), 'r', 'g',
				'INSUFFICIENT_FUNDS', 'schema', now(), now(), now());`, l.id(), walletID, playerID))

		l.refuses("a bet carrying a reference", fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
				reference_external_transaction_id, correlation_id, created_at, updated_at)
			VALUES ('%s', 'HTTP', 'BET', 'PENDING', '%s', '%s', 2500, 'BRL', 'provider-a', 'x3', 'key-x3', repeat('a', 64), 'r', 'g',
				'something', 'schema', now(), now());`, l.id(), walletID, playerID))

		l.refuses("a loss carrying a reference", fmt.Sprintf(`
			INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
				reference_external_transaction_id, result_balance_minor, correlation_id, created_at, updated_at, completed_at)
			VALUES ('%s', 'HTTP', 'LOSS', 'PROCESSED', '%s', '%s', 0, 'BRL', 'provider-a', 'x4', 'key-x4', repeat('a', 64), 'r', 'g',
				'something', 10000, 'schema', now(), now(), now());`, l.id(), walletID, playerID))

		l.refuses("a currency outside the supported list", fmt.Sprintf(`
			INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
			VALUES ('%s', '%s', 'JPY', 0, 1, now(), now());`, l.id(), l.id()))

		l.accepts("dollars are supported", fmt.Sprintf(`
			INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
			VALUES ('%s', '%s', 'USD', 0, 1, now(), now());`, l.id(), l.id()))

		l.refuses("an inbox row completed without an outcome", `
			INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, completed_at, received_at)
			VALUES ('c', 'm-1', repeat('b', 64), now(), now());`)

		l.refuses("an inbox outcome outside the list", `
			INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, outcome, completed_at, received_at)
			VALUES ('c', 'm-2', repeat('b', 64), 'SOMETHING', now(), now());`)

		l.refuses("a negative balance", fmt.Sprintf(
			`UPDATE wallets SET balance_minor = -1, version = 2 WHERE id = '%s';`, walletID))
		l.refuses("a balance change without the version", fmt.Sprintf(
			`UPDATE wallets SET balance_minor = 9000 WHERE id = '%s';`, walletID))
		l.refuses("a version change without the balance", fmt.Sprintf(
			`UPDATE wallets SET version = 2 WHERE id = '%s';`, walletID))
	})

	t.Run("Given the ledger/When anything tries to rewrite history/Then it is refused for every role", func(t *testing.T) {
		// Given
		l := newLedger(t)
		walletID, playerID := l.id(), l.id()
		l.openWallet(walletID, playerID, 10000)

		// When, Then
		l.refuses("the runtime role updating an entry",
			fmt.Sprintf(`UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE wallet_id = '%s';`, walletID))
		l.refuses("the runtime role deleting an entry",
			fmt.Sprintf(`DELETE FROM wallet_ledger_entries WHERE wallet_id = '%s';`, walletID))
		l.refuses("the runtime role deleting a wallet",
			fmt.Sprintf(`DELETE FROM wallets WHERE id = '%s';`, walletID))
		l.refuses("the runtime role deleting an operation",
			fmt.Sprintf(`DELETE FROM wager_transactions WHERE wallet_id = '%s';`, walletID))

		for name, script := range map[string]string{
			"the owner updating an entry":     fmt.Sprintf(`UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE wallet_id = '%s';`, walletID),
			"the owner deleting an entry":     fmt.Sprintf(`DELETE FROM wallet_ledger_entries WHERE wallet_id = '%s';`, walletID),
			"the owner truncating the ledger": `TRUNCATE wallet_ledger_entries;`,
			"the owner deleting a wallet":     fmt.Sprintf(`DELETE FROM wallets WHERE id = '%s';`, walletID),
		} {
			if err := l.env.DB.AttemptAsOwner(script); err == nil {
				t.Errorf("%s: the write was accepted, the trigger should protect the owner too", name)
			}
		}
	})

	t.Run("Given the outbox/When a published snapshot is touched/Then it stays as it was written", func(t *testing.T) {
		// Given
		l := newLedger(t)
		walletID, playerID := l.id(), l.id()
		transactionID := l.openWallet(walletID, playerID, 10000)
		eventID := l.id()

		l.accepts("an event written by the transaction", fmt.Sprintf(`
			INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, event_version, correlation_id,
				causation_id, partition_key, payload, occurred_at, next_attempt_at)
			VALUES ('%s', 'wager_transaction', '%s', 'WagerTransactionProcessed', 1, 'schema', '%s', '%s', '{"a":1}'::jsonb, now(), now());`,
			eventID, transactionID, transactionID, walletID))

		// When, Then
		l.refuses("rewriting the snapshot",
			fmt.Sprintf(`UPDATE outbox_events SET payload = '{"a":2}'::jsonb WHERE id = '%s';`, eventID))
		l.refuses("an incoherent lease",
			fmt.Sprintf(`UPDATE outbox_events SET locked_by = 'someone' WHERE id = '%s';`, eventID))
		l.refuses("an unknown event type", fmt.Sprintf(`
			INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, event_version, correlation_id,
				partition_key, payload, occurred_at, next_attempt_at)
			VALUES ('%s', 'wallet', '%s', 'SomethingElse', 1, 'schema', '%s', '{}'::jsonb, now(), now());`,
			l.id(), walletID, walletID))

		l.accepts("claiming the event", fmt.Sprintf(
			`UPDATE outbox_events SET locked_by = 'instance-1', locked_until = now() + interval '30 seconds', attempts = attempts + 1 WHERE id = '%s';`, eventID))
		l.accepts("marking it published", fmt.Sprintf(
			`UPDATE outbox_events SET published_at = now(), locked_by = NULL, locked_until = NULL WHERE id = '%s';`, eventID))
		l.refuses("rewriting the publication",
			fmt.Sprintf(`UPDATE outbox_events SET published_at = now() WHERE id = '%s';`, eventID))
	})

	t.Run("Given the idempotency indexes/When a key or an external id repeats/Then the second write finds the first", func(t *testing.T) {
		// Given
		l := newLedger(t)
		walletID, playerID := l.id(), l.id()
		l.openWallet(walletID, playerID, 10000)
		l.accepts("the first bet", l.bet(walletID, playerID, "unique-1", 2500, 10000, 2))

		// When
		inserted := l.env.DB.Count(`
			WITH attempt AS (
				INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
					provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id, correlation_id, created_at, updated_at)
				VALUES (gen_random_uuid(), 'HTTP', 'BET', 'PENDING', $1, $2, 100, 'BRL', 'provider-a', 'unique-1', 'another-key', repeat('a', 64), 'r', 'g', 'schema', now(), now())
				ON CONFLICT DO NOTHING
				RETURNING id)
			SELECT count(*) FROM attempt`, walletID, playerID)

		// Then
		if inserted != 0 {
			t.Errorf("rows inserted = %d, the external id was already used", inserted)
		}
		byKey := l.env.DB.Count(
			"SELECT count(*) FROM wager_transactions WHERE provider_id = 'provider-a' AND idempotency_key = 'another-key'")
		byExternal := l.env.DB.Count(
			"SELECT count(*) FROM wager_transactions WHERE provider_id = 'provider-a' AND external_transaction_id = 'unique-1'")
		if byKey != 0 || byExternal != 1 {
			t.Errorf("lookups found %d by key and %d by external id, want 0 and 1", byKey, byExternal)
		}
	})
}
