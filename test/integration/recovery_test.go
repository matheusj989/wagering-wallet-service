//go:build integration

package integration_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestRecovery(t *testing.T) {
	t.Run("Given a consumer killed between the commit and the delete/When the message returns/Then it is not applied twice", func(t *testing.T) {
		// Given
		w := newWorld(t, 1,
			testenv.WithFailpoint("consumer.after_commit_before_delete"),
			testenv.WithoutOutboxPublisher())
		w.openWallet("1000.00")
		external := w.nextID("sqs-bet")
		messageID := "msg-" + external

		// When
		w.publish(messageID, "key-"+external, w.wager("BET", external, "25.00", ""))

		testenv.Eventually(t, 30*time.Second, "the instance dies right after the commit", func() (bool, string) {
			return !w.app(0).Running(), "the instance is still running"
		})
		if settled := w.env.DB.Count(
			"SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1 AND status = 'PROCESSED'", external); settled != 1 {
			t.Fatalf("the operation should be committed before the process died, found %d", settled)
		}

		// a healthy instance takes over and receives the same message again
		w.replacePrimary(w.env.StartApp(1, testenv.WithoutOutboxPublisher())[0])

		// Then
		testenv.Eventually(t, 45*time.Second, "the redelivered message is discarded", func() (bool, string) {
			pending := w.env.Queues.Pending(w.env.Queues.Wager)
			return pending == 0, fmt.Sprintf("%d messages still in the queue", pending)
		})
		if debits := w.env.DB.Count(
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.walletID); debits != 1 {
			t.Errorf("debits = %d, the redelivery must not move money again", debits)
		}
		if inbox := w.env.DB.Count("SELECT count(*) FROM inbox_messages WHERE message_id = $1", messageID); inbox != 1 {
			t.Errorf("inbox rows = %d, want one", inbox)
		}
		if metrics := w.app(0).Metrics(); !metrics.Has("sqs_redeliveries_total", 1) {
			t.Errorf("the redelivery should be counted:\n%s", metrics.Lines("sqs_redeliveries_total"))
		}
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given a publisher killed between the send and the mark/When another instance retries/Then the event keeps its identity", func(t *testing.T) {
		// Given
		w := newWorld(t, 1,
			testenv.WithFailpoint("outbox.after_publish_before_mark"),
			testenv.WithoutConsumer())
		w.openWallet("100.00")

		// When
		testenv.Eventually(t, 30*time.Second, "the publisher dies after sending the first event", func() (bool, string) {
			return !w.app(0).Running(), "the instance is still running"
		})
		published := w.env.DB.Count("SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL")
		if published != 0 {
			t.Fatalf("the instance died before marking, so nothing should be marked, found %d", published)
		}

		w.replacePrimary(w.env.StartApp(1, testenv.WithoutConsumer())[0])

		// Then
		testenv.Eventually(t, 45*time.Second, "the surviving publisher drains the outbox", func() (bool, string) {
			pending := w.env.DB.Count("SELECT count(*) FROM outbox_events WHERE published_at IS NULL")
			return pending == 0, fmt.Sprintf("%d events still pending", pending)
		})

		identities := map[string]int{}
		for _, message := range w.env.Queues.EventMessages(10) {
			identities[decodeEvent(t, message.Body)["eventId"].(string)]++
		}
		if len(identities) != 2 {
			t.Errorf("distinct events in the queue = %d, want the two written by the opening", len(identities))
		}
		for eventID, times := range identities {
			if times > 1 {
				t.Logf("event %s was published %d times, which at-least-once allows", eventID, times)
			}
		}
	})

	t.Run("Given every instance restarted/When the work resumes/Then pendings, idempotency and the balance survive", func(t *testing.T) {
		// Given
		w := newWorld(t, 2, testenv.WithoutReferenceWorker())
		w.openWallet("1000.00")

		bet := w.nextID("bet")
		key := "key-" + bet
		status, settled := w.submit(key, w.wager("BET", bet, "25.00", ""))
		requireStatus(t, "bet", status, 201, settled)

		late := w.nextID("late-bet")
		rollback := w.nextID("rollback")
		status, pending := w.submit("key-"+rollback, w.wager("ROLLBACK", rollback, "10.00", late))
		requireStatus(t, "rollback waiting for a bet that has not arrived", status, 202, pending)

		// When
		for _, instance := range w.apps {
			instance.Kill()
		}
		restarted := w.env.StartApp(2)
		w.replacePrimary(restarted[0])

		status, body := w.submit("key-"+late, w.wager("BET", late, "10.00", ""))
		requireStatus(t, "the awaited bet after the restart", status, 201, body)

		// Then
		testenv.Eventually(t, 45*time.Second, "the pending reversal is resolved after the restart", func() (bool, string) {
			open := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE'")
			return open == 0, fmt.Sprintf("%d reversals still pending", open)
		})

		replayStatus, replay := w.submit(key, w.wager("BET", bet, "25.00", ""))
		requireStatus(t, "replay after the restart", replayStatus, 201, replay)
		if !boolean(t, replay, "idempotentReplay") {
			t.Error("idempotency has to survive a restart")
		}
		if amountOf(t, replay, "balance") != "975.00" {
			t.Errorf("replay balance = %s, want the 975.00 observed originally", amountOf(t, replay, "balance"))
		}
		w.requireConsistent("975.00", 4)
	})

	t.Run("Given three instances/When the mandatory scenarios run across them/Then the outcome does not change", func(t *testing.T) {
		// Given
		w := newWorld(t, 3)
		w.openWallet("100.00")
		viaQueue := w.nextID("sqs-bet")
		viaHTTP := w.nextID("http-bet")

		// When
		w.publish("msg-"+viaQueue, "key-"+viaQueue, w.wager("BET", viaQueue, "80.00", ""))
		status, body := w.submitOn(w.app(2), testenv.ClientProviderA, "key-"+viaHTTP, w.wager("BET", viaHTTP, "80.00", ""))

		// Then
		if status != 201 && status != 422 {
			t.Fatalf("the http bet answered %d: %v", status, body)
		}
		testenv.Eventually(t, 45*time.Second, "both doors settle their bet", func() (bool, string) {
			settled := w.env.DB.Count(
				"SELECT count(*) FROM wager_transactions WHERE kind = 'BET' AND status IN ('PROCESSED', 'REJECTED')")
			return settled == 2, fmt.Sprintf("%d bets settled so far", settled)
		})

		processed := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE kind = 'BET' AND status = 'PROCESSED'")
		rejected := w.env.DB.Count(
			"SELECT count(*) FROM wager_transactions WHERE kind = 'BET' AND status = 'REJECTED' AND failure_code = 'INSUFFICIENT_FUNDS'")
		if processed != 1 || rejected != 1 {
			t.Errorf("bets = %d processed and %d rejected, want one of each", processed, rejected)
		}
		if debits := w.env.DB.Count(
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.walletID); debits != 1 {
			t.Errorf("debits = %d, want 1", debits)
		}
		w.requireConsistent("20.00", 2)
	})
}
