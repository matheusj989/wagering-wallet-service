//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	sqlrepositories "github.com/matheusj989/wagering-wallet-service/internal/infrastructure/postgres/repositories"
	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestOutboxPublication(t *testing.T) {
	t.Run("Given settled operations/When the publisher runs/Then the queue carries the documented events", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("bet")

		// When
		status, body := w.submit("key-"+external, w.wager("BET", external, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)

		// Then
		var events []testenv.Received
		testenv.Eventually(t, 30*time.Second, "the opening and the bet are published", func() (bool, string) {
			events = append(events, w.env.Queues.EventMessages(10)...)
			return len(events) >= 4, fmt.Sprintf("%d events so far", len(events))
		})

		betTransaction := text(t, body, "transactionId")
		byType := map[string]map[string]any{}
		var betBalanceChange map[string]any
		for _, message := range events {
			event := decodeEvent(t, message.Body)
			byType[message.Attributes["eventType"]] = event
			if message.Attributes["eventType"] == "WalletBalanceChanged" && event["causationId"] == betTransaction {
				betBalanceChange = event
			}
			if message.GroupID != w.walletID {
				t.Errorf("message group = %q, want the wallet %q", message.GroupID, w.walletID)
			}
			if message.Attributes["correlationId"] == "" {
				t.Error("every event should carry its correlation identifier")
			}
			if event["version"] != float64(1) {
				t.Errorf("event version = %v, want 1", event["version"])
			}
		}

		if betBalanceChange == nil {
			t.Fatalf("the balance change of the bet was not published, saw %v", keysOf(byType))
		}
		data, _ := betBalanceChange["data"].(map[string]any)
		if data["direction"] != "DEBIT" || data["walletVersion"] != float64(2) {
			t.Errorf("balance event = %v, want a DEBIT at version 2", data)
		}
		if amountOf(t, data, "balanceBefore") != "1000.00" || amountOf(t, data, "balanceAfter") != "975.00" {
			t.Errorf("balance event moved %v -> %v, want 1000.00 -> 975.00", data["balanceBefore"], data["balanceAfter"])
		}
		if betBalanceChange["aggregateId"] != w.walletID {
			t.Errorf("aggregateId = %v, want the wallet for a balance change", betBalanceChange["aggregateId"])
		}

		opening, ok := byType["WagerTransactionProcessed"]
		if !ok {
			t.Fatal("the processed event was not published")
		}
		if _, hasProvider := opening["data"].(map[string]any)["providerId"]; !hasProvider {
			t.Log("the opening event carries no provider metadata, as documented")
		}
	})

	t.Run("Given a publisher that is switched off/When another instance takes over/Then the pending event is published once", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutOutboxPublisher())
		w.openWallet("1000.00")
		external := w.nextID("bet")
		status, body := w.submit("key-"+external, w.wager("BET", external, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)

		if pending := w.env.DB.Count("SELECT count(*) FROM outbox_events WHERE published_at IS NULL"); pending != 4 {
			t.Fatalf("pending events = %d, want the four written without a publisher", pending)
		}

		// When
		w.app(0).Kill()
		taking := w.env.StartApp(1)

		// Then
		testenv.Eventually(t, 30*time.Second, "the new instance drains the outbox", func() (bool, string) {
			pending := w.env.DB.Count("SELECT count(*) FROM outbox_events WHERE published_at IS NULL")
			return pending == 0, fmt.Sprintf("%d events still pending", pending)
		})
		if published := len(w.env.Queues.EventMessages(10)); published < 4 {
			t.Errorf("published events = %d, want at least 4", published)
		}
		if metrics := taking[0].Metrics(); !metrics.Has("outbox_publish_attempts_total", 4, "result=success") {
			t.Errorf("published events should be counted:\n%s", metrics.Lines("outbox_publish_attempts_total"))
		}
	})

	t.Run("Given two publishers/When they race over the same outbox/Then every event is published and none is lost", func(t *testing.T) {
		// Given
		w := newWorld(t, 2, testenv.WithoutOutboxPublisher())
		w.openWallet("100000.00")
		for range 25 {
			external := w.nextID("bet")
			status, body := w.submit("key-"+external, w.wager("BET", external, "1.00", ""))
			requireStatus(t, "bet", status, 201, body)
		}
		expected := w.env.DB.Count("SELECT count(*) FROM outbox_events WHERE published_at IS NULL")
		if expected < 50 {
			t.Fatalf("pending events = %d, want at least 50", expected)
		}

		// When
		publishers := w.env.StartApp(2)

		// Then
		testenv.Eventually(t, 60*time.Second, "both publishers drain the outbox", func() (bool, string) {
			pending := w.env.DB.Count("SELECT count(*) FROM outbox_events WHERE published_at IS NULL")
			return pending == 0, fmt.Sprintf("%d events still pending", pending)
		})

		seen := map[string]int{}
		for _, message := range w.env.Queues.EventMessages(int(expected) + 20) {
			seen[decodeEvent(t, message.Body)["eventId"].(string)]++
		}
		if int64(len(seen)) != expected {
			t.Errorf("distinct events in the queue = %d, want %d", len(seen), expected)
		}
		for _, publisher := range publishers {
			if metrics := publisher.Metrics(); metrics.Value("outbox_publish_attempts_total", "result=success") == 0 {
				t.Log("one publisher lost every claim to the other, which is allowed")
			}
		}
	})

	t.Run("Given the events queue is gone/When the publisher tries/Then it backs off and records the failure", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")
		testenv.Eventually(t, 30*time.Second, "the opening is published", func() (bool, string) {
			pending := w.env.DB.Count("SELECT count(*) FROM outbox_events WHERE published_at IS NULL")
			return pending == 0, fmt.Sprintf("%d events still pending", pending)
		})
		publisher := w.app(0)

		// When
		w.env.Queues.DeleteEvents()
		external := w.nextID("bet")
		status, body := w.submit("key-"+external, w.wager("BET", external, "1.00", ""))
		requireStatus(t, "bet", status, 201, body)

		// Then
		testenv.Eventually(t, 45*time.Second, "the failure is recorded on the pending event", func() (bool, string) {
			attempts := w.env.DB.Count(
				"SELECT COALESCE(max(attempts), 0) FROM outbox_events WHERE published_at IS NULL AND last_error IS NOT NULL")
			return attempts >= 1, fmt.Sprintf("highest attempt count so far is %d", attempts)
		})
		if scheduled := w.env.DB.Count(
			"SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND next_attempt_at > now() AND locked_by IS NULL"); scheduled == 0 {
			t.Error("a failed publication should be rescheduled with the lease released")
		}
		if metrics := publisher.Metrics(); !metrics.Has("outbox_publish_attempts_total", 1, "result=failure") {
			t.Errorf("failures should be counted:\n%s", metrics.Lines("outbox_publish_attempts_total"))
		}
	})
}

func TestOutboxClaims(t *testing.T) {
	t.Run("Given a claim that expired/When a newer one exists/Then the old owner cannot finish the work", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutOutboxPublisher())
		w.openWallet("100.00")

		ctx := context.Background()
		repository := sqlrepositories.NewOutboxRepository(w.env.DB.Pool())

		firstClaim, err := repository.Claim(ctx, "instance-a", time.Now(), 50*time.Millisecond, 1)
		if err != nil || len(firstClaim) != 1 {
			t.Fatalf("the first claim failed: %v (%d events)", err, len(firstClaim))
		}
		oldReceipt := firstClaim[0].Receipt()

		time.Sleep(200 * time.Millisecond)
		secondClaim, err := repository.Claim(ctx, "instance-b", time.Now(), time.Minute, 1)
		if err != nil || len(secondClaim) != 1 {
			t.Fatalf("the second claim failed: %v (%d events)", err, len(secondClaim))
		}
		newReceipt := secondClaim[0].Receipt()
		if newReceipt.ID != oldReceipt.ID {
			t.Fatalf("the two claims took different events, the scenario needs the same one")
		}
		if newReceipt.Generation != oldReceipt.Generation+1 {
			t.Errorf("generations = %d then %d, the claim should move the generation forward", oldReceipt.Generation, newReceipt.Generation)
		}

		// When
		publishedByOld, publishErr := repository.MarkPublished(ctx, oldReceipt, time.Now())
		failedByOld, failErr := repository.MarkFailed(ctx, oldReceipt, "late failure", time.Now().Add(time.Hour))

		// Then
		if publishErr != nil || failErr != nil {
			t.Fatalf("the late calls should be refused, not fail: %v / %v", publishErr, failErr)
		}
		if publishedByOld || failedByOld {
			t.Errorf("a stale claim changed the row: published=%v failed=%v", publishedByOld, failedByOld)
		}
		if published := w.env.DB.Count("SELECT count(*) FROM outbox_events WHERE id = $1 AND published_at IS NOT NULL", newReceipt.ID); published != 0 {
			t.Error("the event should still be unpublished and owned by the new claim")
		}
		if owner := w.env.DB.Value("SELECT locked_by FROM outbox_events WHERE id = $1", newReceipt.ID); owner != "instance-b" {
			t.Errorf("owner = %q, want instance-b", owner)
		}

		// and the current owner can finish it
		done, err := repository.MarkPublished(ctx, newReceipt, time.Now())
		if err != nil || !done {
			t.Errorf("the current owner should be able to finish: %v (applied %v)", err, done)
		}
	})

	t.Run("Given an event already published/When a late claim reports success/Then the publication is untouched", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutOutboxPublisher())
		w.openWallet("100.00")

		ctx := context.Background()
		repository := sqlrepositories.NewOutboxRepository(w.env.DB.Pool())

		stale, err := repository.Claim(ctx, "same-instance", time.Now(), 50*time.Millisecond, 1)
		if err != nil || len(stale) != 1 {
			t.Fatalf("the first claim failed: %v", err)
		}
		staleReceipt := stale[0].Receipt()

		time.Sleep(200 * time.Millisecond)
		current, err := repository.Claim(ctx, "same-instance", time.Now(), time.Minute, 1)
		if err != nil || len(current) != 1 {
			t.Fatalf("the second claim failed: %v", err)
		}
		if _, err := repository.MarkPublished(ctx, current[0].Receipt(), time.Now()); err != nil {
			t.Fatalf("the current claim could not finish: %v", err)
		}
		publishedAt := w.env.DB.Value("SELECT published_at::text FROM outbox_events WHERE id = $1", staleReceipt.ID)

		// When
		applied, err := repository.MarkFailed(ctx, staleReceipt, "late failure", time.Now().Add(time.Hour))

		// Then
		if err != nil {
			t.Fatalf("the late call should be refused, not fail: %v", err)
		}
		if applied {
			t.Error("a stale claim must not rewrite a published event")
		}
		if after := w.env.DB.Value("SELECT published_at::text FROM outbox_events WHERE id = $1", staleReceipt.ID); after != publishedAt {
			t.Errorf("published_at changed from %q to %q", publishedAt, after)
		}
	})
}

func keysOf(source map[string]map[string]any) []string {
	names := make([]string, 0, len(source))
	for name := range source {
		names = append(names, name)
	}
	return names
}
