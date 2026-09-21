//go:build integration

package integration_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestPendingReference(t *testing.T) {
	t.Run("Given a rollback that arrives before its bet/When the bet is settled/Then the reversal is applied afterwards", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutReferenceWorker())
		w.openWallet("1000.00")
		bet := w.nextID("bet")
		rollback := w.nextID("rollback")

		// When
		status, pending := w.submit("key-"+rollback, w.wager("ROLLBACK", rollback, "25.00", bet))

		// Then
		requireStatus(t, "rollback before the bet", status, 202, pending)
		requireField(t, "rollback before the bet", pending, "status", "PENDING_REFERENCE")
		if pending["balance"] != nil {
			t.Errorf("a pending reversal should not report a balance, got %v", pending["balance"])
		}
		if rows := w.env.DB.Count(
			"SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1 AND status = 'PENDING_REFERENCE' AND next_attempt_at IS NOT NULL AND reference_deadline_at IS NOT NULL",
			rollback); rows != 1 {
			t.Errorf("the pending row should carry its schedule, found %d", rows)
		}
		if events := w.env.DB.Count(
			"SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionPendingReference' AND causation_id = $1",
			text(t, pending, "transactionId")); events != 1 {
			t.Errorf("pending reference events = %d, want 1", events)
		}

		replayStatus, replay := w.submit("key-"+rollback, w.wager("ROLLBACK", rollback, "25.00", bet))
		requireStatus(t, "replay while pending", replayStatus, 202, replay)
		if !boolean(t, replay, "idempotentReplay") {
			t.Error("the second call should be a replay of the pending reversal")
		}

		// the bet arrives and a worker is switched on
		status, body := w.submit("key-"+bet, w.wager("BET", bet, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)
		w.env.StartApp(1)

		testenv.Eventually(t, 30*time.Second, "the pending rollback is settled", func() (bool, string) {
			status, found := w.transaction(rollback)
			if status != 200 {
				return false, "the reversal is not readable yet"
			}
			return optionalText(found, "status") == "PROCESSED", "status " + optionalText(found, "status")
		})

		balance, _ := w.walletState()
		if balance != "1000.00" {
			t.Errorf("balance = %s, want the bet to be undone back to 1000.00", balance)
		}
		w.requireConsistent("1000.00", 3)
	})

	t.Run("Given a reversal whose reference never arrives/When the deadline passes/Then it is rejected and auditable", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		rollback := w.nextID("rollback")

		// When
		status, pending := w.submit("key-"+rollback, w.wager("ROLLBACK", rollback, "25.00", "never-"+rollback))
		requireStatus(t, "rollback without a reference", status, 202, pending)

		// Then
		testenv.Eventually(t, 45*time.Second, "the pending reversal expires", func() (bool, string) {
			status, found := w.transaction(rollback)
			if status != 200 {
				return false, "the reversal is not readable yet"
			}
			return optionalText(found, "status") == "REJECTED", "status " + optionalText(found, "status")
		})

		status, rejected := w.transaction(rollback)
		requireStatus(t, "expired reversal", status, 200, rejected)
		requireField(t, "expired reversal", rejected, "failureCode", "REFERENCE_NOT_FOUND")
		if events := w.env.DB.Count(
			"SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionRejected' AND causation_id = $1",
			text(t, pending, "transactionId")); events != 1 {
			t.Errorf("rejection events = %d, want 1", events)
		}
		if attempts := w.env.DB.Count(
			"SELECT reference_attempts FROM wager_transactions WHERE external_transaction_id = $1", rollback); attempts < 1 {
			t.Errorf("the worker should have retried before giving up, attempts = %d", attempts)
		}
		w.requireConsistent("1000.00", 1)
	})

	t.Run("Given a reference that ended without success/When the reversal is retried/Then it reports that the reference is unusable", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("20.00")
		bet := w.nextID("bet")
		status, body := w.submit("key-"+bet, w.wager("BET", bet, "80.00", ""))
		requireStatus(t, "bet without funds", status, 422, body)

		// When
		refund := w.nextID("refund")
		status, refunded := w.submit("key-"+refund, w.wager("REFUND", refund, "80.00", bet))

		// Then
		requireStatus(t, "refund of a rejected bet", status, 422, refunded)
		requireField(t, "refund of a rejected bet", refunded, "failureCode", "REFERENCE_NOT_PROCESSED")
		w.requireConsistent("20.00", 1)
	})

	t.Run("Given a pending reversal/When the instance that registered it dies/Then another instance resolves it", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutReferenceWorker())
		w.openWallet("1000.00")
		bet := w.nextID("bet")
		rollback := w.nextID("rollback")

		status, pending := w.submit("key-"+rollback, w.wager("ROLLBACK", rollback, "25.00", bet))
		requireStatus(t, "rollback before the bet", status, 202, pending)
		status, body := w.submit("key-"+bet, w.wager("BET", bet, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)

		// When
		w.app(0).Kill()
		w.replacePrimary(w.env.StartApp(1)[0])

		// Then
		testenv.Eventually(t, 30*time.Second, "the surviving instance resolves the pending reversal", func() (bool, string) {
			settled := w.env.DB.Count(
				"SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1 AND status = 'PROCESSED'", rollback)
			return settled == 1, fmt.Sprintf("%d settled reversals", settled)
		})
		w.requireConsistent("1000.00", 3)
	})

	t.Run("Given a reversal that would credit above the limit/When the worker resolves it/Then it is rejected without movement", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutReferenceWorker())
		w.openWallet("92233720368547758.06")
		bet := w.nextID("bet")
		refund := w.nextID("refund")

		status, pending := w.submit("key-"+refund, w.wager("REFUND", refund, "0.01", bet))
		requireStatus(t, "refund before the bet", status, 202, pending)

		status, body := w.submit("key-"+bet, w.wager("BET", bet, "0.01", ""))
		requireStatus(t, "bet", status, 201, body)

		topUp := w.nextID("win")
		status, body = w.submit("key-"+topUp, w.wager("WIN", topUp, "0.02", ""))
		requireStatus(t, "top up to the limit", status, 201, body)

		// When
		w.env.StartApp(1)

		// Then
		testenv.Eventually(t, 30*time.Second, "the worker settles the pending refund", func() (bool, string) {
			status, found := w.transaction(refund)
			if status != 200 {
				return false, "the refund is not readable yet"
			}
			settled := optionalText(found, "status")
			return settled == "REJECTED" || settled == "PROCESSED", "status " + settled
		})

		status, settled := w.transaction(refund)
		requireStatus(t, "refund at the limit", status, 200, settled)
		requireField(t, "refund at the limit", settled, "status", "REJECTED")
		requireField(t, "refund at the limit", settled, "failureCode", "BALANCE_LIMIT_EXCEEDED")
		if failed := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE status = 'FAILED'"); failed != 0 {
			t.Errorf("a business rejection must not become a permanent failure, found %d", failed)
		}
		w.requireConsistent("92233720368547758.07", 3)
	})

	t.Run("Given a pending reversal claimed by the worker/When the referenced bet is settled/Then neither side blocks the other", func(t *testing.T) {
		// Given
		w := newWorld(t, 2, testenv.WithoutReferenceWorker())
		w.openWallet("1000.00")

		pendings := make([]string, 5)
		for index := range pendings {
			rollback := w.nextID("rollback")
			pendings[index] = rollback
			status, body := w.submit("key-"+rollback, w.wager("ROLLBACK", rollback, "5.00", "late-"+rollback))
			requireStatus(t, "pending rollback", status, 202, body)
		}

		// When
		workers := w.env.StartApp(2)
		for _, rollback := range pendings {
			reference := "late-" + rollback
			status, body := w.submit("key-"+reference, w.wager("BET", reference, "5.00", ""))
			requireStatus(t, "the awaited bet", status, 201, body)
		}

		// Then
		testenv.Eventually(t, 45*time.Second, "every pending reversal is settled", func() (bool, string) {
			open := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE'")
			return open == 0, fmt.Sprintf("%d reversals still pending", open)
		})
		if processed := w.env.DB.Count(
			"SELECT count(*) FROM wager_transactions WHERE kind = 'ROLLBACK' AND status = 'PROCESSED'"); processed != int64(len(pendings)) {
			t.Errorf("settled reversals = %d, want %d", processed, len(pendings))
		}
		for _, worker := range workers {
			if metrics := worker.Metrics(); metrics.Value("reference_failed_total") > 0 {
				t.Errorf("no reversal should end as a permanent failure:\n%s", metrics.Lines("reference_failed_total"))
			}
		}
		w.requireConsistent("1000.00", 11)
	})
}

func TestReferenceResolution(t *testing.T) {
	t.Run("Given each kind of reference/When a reversal names it/Then the documented outcome is applied", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")

		bet := w.nextID("bet")
		status, body := w.submit("key-"+bet, w.wager("BET", bet, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)

		win := w.nextID("win")
		status, body = w.submit("key-"+win, w.wager("WIN", win, "50.00", ""))
		requireStatus(t, "win", status, 201, body)

		loss := w.nextID("loss")
		status, body = w.submit("key-"+loss, w.wager("LOSS", loss, "0.00", ""))
		requireStatus(t, "loss", status, 201, body)

		scenarios := []struct {
			name      string
			kind      string
			amount    string
			reference string
			status    int
			failure   string
		}{
			{"refund of a bet is a credit", "REFUND", "25.00", bet, 201, ""},
			{"rollback of a win is a debit", "ROLLBACK", "50.00", win, 201, ""},
			{"refund of a win is not allowed", "REFUND", "50.00", win, 422, "REFERENCE_KIND_NOT_ALLOWED"},
			{"rollback of a loss is not allowed", "ROLLBACK", "0.01", loss, 422, "REFERENCE_KIND_NOT_ALLOWED"},
			{"rollback with another amount", "ROLLBACK", "250.00", bet, 422, "REFERENCE_MISMATCH"},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// When
				external := w.nextID("reversal")
				status, body := w.submit("key-"+external, w.wager(scenario.kind, external, scenario.amount, scenario.reference))

				// Then
				requireStatus(t, scenario.name, status, scenario.status, body)
				if scenario.failure != "" {
					requireField(t, scenario.name, body, "failureCode", scenario.failure)
				}
			})
		}

		w.requireConsistent("1000.00", 5)
	})

	t.Run("Given a bet that was refunded/When the refund is rolled back/Then the bet counts again and cannot be undone twice", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		bet := w.nextID("bet")
		status, body := w.submit("key-"+bet, w.wager("BET", bet, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)

		refund := w.nextID("refund")
		status, body = w.submit("key-"+refund, w.wager("REFUND", refund, "25.00", bet))
		requireStatus(t, "refund", status, 201, body)

		// When
		rollbackOfRefund := w.nextID("rollback")
		status, undone := w.submit("key-"+rollbackOfRefund, w.wager("ROLLBACK", rollbackOfRefund, "25.00", refund))

		rollbackOfBet := w.nextID("rollback")
		secondStatus, second := w.submit("key-"+rollbackOfBet, w.wager("ROLLBACK", rollbackOfBet, "25.00", bet))

		// Then
		requireStatus(t, "rollback of the refund", status, 201, undone)
		if amountOf(t, undone, "balance") != "975.00" {
			t.Errorf("balance = %s, want the bet to count again at 975.00", amountOf(t, undone, "balance"))
		}
		requireStatus(t, "rollback of the bet", secondStatus, 422, second)
		requireField(t, "rollback of the bet", second, "failureCode", "ALREADY_REVERSED")
		w.requireConsistent("975.00", 4)
	})
}
