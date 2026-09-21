//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestConsumer(t *testing.T) {
	t.Run("Given a valid message/When the consumer handles it/Then the result matches the http path", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("sqs-bet")
		messageID := "msg-" + external

		// When
		w.publish(messageID, "key-"+external, w.wager("BET", external, "80.00", ""))

		// Then
		testenv.Eventually(t, 30*time.Second, "the bet from the queue is settled", func() (bool, string) {
			status, body := w.transaction(external)
			if status != 200 {
				return false, fmt.Sprintf("the transaction is not there yet (%d)", status)
			}
			return optionalText(body, "status") == "PROCESSED", "status " + optionalText(body, "status")
		})

		balance, version := w.walletState()
		if balance != "920.00" || version != 2 {
			t.Errorf("wallet = %s at version %d, want 920.00 at version 2", balance, version)
		}
		if inbox := w.env.DB.Count(
			"SELECT count(*) FROM inbox_messages WHERE message_id = $1 AND outcome = 'PROCESSED' AND completed_at IS NOT NULL",
			messageID); inbox != 1 {
			t.Errorf("inbox rows for the message = %d, want one completed row", inbox)
		}
		testenv.Eventually(t, 15*time.Second, "the queue is drained", func() (bool, string) {
			pending := w.env.Queues.Pending(w.env.Queues.Wager)
			return pending == 0, fmt.Sprintf("%d messages still in the queue", pending)
		})
		w.requireConsistent("920.00", 2)
	})

	t.Run("Given a correlation id on the message/When the consumer handles it/Then the events carry it", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("sqs-correlated")
		correlationID := "correlation-" + external

		// When
		w.publishWithCorrelation("msg-"+external, "key-"+external, correlationID, w.wager("BET", external, "40.00", ""))

		// Then
		testenv.Eventually(t, 30*time.Second, "the correlated bet is settled", func() (bool, string) {
			status, body := w.transaction(external)
			if status != 200 {
				return false, fmt.Sprintf("the transaction is not there yet (%d)", status)
			}
			return optionalText(body, "status") == "PROCESSED", "status " + optionalText(body, "status")
		})

		stored := w.env.DB.Text(
			"SELECT correlation_id FROM wager_transactions WHERE provider_id = 'provider-a' AND external_transaction_id = $1",
			external)
		if stored != correlationID {
			t.Errorf("stored correlationId = %q, want the one sent on the message attribute %q", stored, correlationID)
		}
	})

	t.Run("Given the same operation on both doors/When http and the queue race/Then only one movement happens", func(t *testing.T) {
		// Given
		w := newWorld(t, 3)
		w.openWallet("1000.00")
		external := w.nextID("both")
		key := "key-" + external
		request := w.wager("BET", external, "25.00", "")

		// When
		w.publish("msg-"+external, key, request)
		status, body := w.submit(key, request)

		// Then
		if status != 201 {
			t.Fatalf("the http call answered %d: %v", status, body)
		}
		testenv.Eventually(t, 30*time.Second, "the queue message is handled", func() (bool, string) {
			pending := w.env.Queues.Pending(w.env.Queues.Wager)
			return pending == 0, fmt.Sprintf("%d messages still in the queue", pending)
		})
		if rows := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", external); rows != 1 {
			t.Errorf("transactions = %d, want a single one for both doors", rows)
		}
		if debits := w.env.DB.Count(
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.walletID); debits != 1 {
			t.Errorf("debits = %d, want 1", debits)
		}
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given a message the service cannot use/When it arrives/Then it reaches the dead letter queue with its reason", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")

		type expectation struct {
			name    string
			publish func() string
			reason  string
		}
		scenarios := []expectation{
			{
				name: "body that is not an envelope",
				publish: func() string {
					messageID := "msg-" + w.nextID("broken")
					w.env.Queues.SendRaw(messageID, w.walletID, "{not json")
					return messageID
				},
				reason: "INVALID_PAYLOAD",
			},
			{
				name: "envelope of an unknown type",
				publish: func() string {
					messageID := "msg-" + w.nextID("type")
					w.env.Queues.Send(messageID, w.walletID, map[string]any{
						"messageId": messageID, "type": "SomethingElse",
						"occurredAt": "2026-09-08T12:00:00.000Z", "data": map[string]any{},
					})
					return messageID
				},
				reason: "INVALID_PAYLOAD",
			},
			{
				name: "opening through the queue",
				publish: func() string {
					external := w.nextID("open")
					messageID := "msg-" + external
					w.publish(messageID, "key-"+external, w.wager("OPENING", external, "10.00", ""))
					return messageID
				},
				reason: "OPENING_NOT_ALLOWED",
			},
			{
				name: "wallet that does not exist",
				publish: func() string {
					external := w.nextID("nowallet")
					messageID := "msg-" + external
					request := w.wager("BET", external, "1.00", "")
					request["walletId"] = uuid.Must(uuid.NewV7()).String()
					w.env.Queues.Send(messageID, w.walletID, w.envelope(messageID, "key-"+external, request))
					return messageID
				},
				reason: "WALLET_NOT_FOUND",
			},
		}

		for _, scenario := range scenarios {
			scenario.publish()
		}

		// When, Then
		wanted := map[string]bool{}
		for _, scenario := range scenarios {
			wanted[scenario.reason] = false
		}
		testenv.Eventually(t, 45*time.Second, "every unusable message reaches the dead letter queue", func() (bool, string) {
			for _, message := range w.env.Queues.DeadLetters(20) {
				if reason, ok := message.Attributes["reason"]; ok {
					wanted[reason] = true
				}
			}
			missing := []string{}
			for reason, seen := range wanted {
				if !seen {
					missing = append(missing, reason)
				}
			}
			return len(missing) == 0, "still waiting for " + strings.Join(missing, ", ")
		})

		if rows := w.env.DB.Count("SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", w.walletID); rows != 1 {
			t.Errorf("ledger entries = %d, only the opening should exist", rows)
		}
		if metrics := w.app(0).Metrics(); !metrics.Has("sqs_dlq_total", 4) {
			t.Errorf("dead lettered messages should be counted:\n%s", metrics.Lines("sqs_dlq_total"))
		}
	})

	t.Run("Given a message that was already handled/When the same id arrives with another body/Then it is dead lettered", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("bet")
		messageID := "msg-" + external
		w.publish(messageID, "key-"+external, w.wager("BET", external, "25.00", ""))

		testenv.Eventually(t, 30*time.Second, "the first message is settled", func() (bool, string) {
			status, _ := w.transaction(external)
			return status == 200, "the transaction is not there yet"
		})

		// When
		changed := w.wager("BET", w.nextID("other"), "30.00", "")
		w.env.Queues.Send(messageID+"-resend", w.walletID, map[string]any{
			"messageId": messageID, "type": "WagerTransactionRequested",
			"occurredAt": "2026-09-08T12:00:00.000Z",
			"data":       withKey(changed, "key-changed"),
		})

		// Then
		testenv.Eventually(t, 45*time.Second, "the mismatching redelivery is dead lettered", func() (bool, string) {
			for _, message := range w.env.Queues.DeadLetters(20) {
				if message.Attributes["reason"] == "INBOX_HASH_MISMATCH" {
					return true, ""
				}
			}
			return false, "the dead letter queue does not carry the mismatch yet"
		})
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given a conflicting key on the queue/When it arrives/Then retrying will not fix it and it is dead lettered", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("bet")
		key := "key-" + external
		status, body := w.submit(key, w.wager("BET", external, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)

		// When
		messageID := "msg-" + w.nextID("conflict")
		w.publish(messageID, key, w.wager("BET", external, "30.00", ""))

		// Then
		testenv.Eventually(t, 45*time.Second, "the conflict is dead lettered", func() (bool, string) {
			for _, message := range w.env.Queues.DeadLetters(20) {
				if message.Attributes["reason"] == "IDEMPOTENCY_KEY_CONFLICT" {
					return true, ""
				}
			}
			return false, "the dead letter queue does not carry the conflict yet"
		})
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given identifiers at the edge of the contract/When they travel the queue/Then the business id survives and transport ids stay valid", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")

		scenarios := []struct {
			name      string
			messageID string
		}{
			{"identifier of 128 characters", strings.Repeat("a", 128)},
			{"identifier of 255 characters", strings.Repeat("b", 255)},
			{"identifier with unicode", "mensagem-ação-🎲-" + w.nextID("uni")},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// When
				external := w.nextID("edge")
				w.publish(scenario.messageID, "key-"+external, w.wager("BET", external, "1.00", ""))

				// Then
				testenv.Eventually(t, 30*time.Second, "the message is settled", func() (bool, string) {
					status, _ := w.transaction(external)
					return status == 200, "the transaction is not there yet"
				})
				if rows := w.env.DB.Count("SELECT count(*) FROM inbox_messages WHERE message_id = $1", scenario.messageID); rows != 1 {
					t.Errorf("the inbox should keep the business identifier untouched, found %d rows", rows)
				}
			})
		}
	})

	t.Run("Given a broken body without an identifier/When it is dead lettered/Then the broker identity is used", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("10.00")

		// When
		w.env.Queues.SendRaw("msg-"+w.nextID("noid"), w.walletID, `{"type":"WagerTransactionRequested","data":{}}`)

		// Then
		var found testenv.Received
		testenv.Eventually(t, 45*time.Second, "the message without an identifier is dead lettered", func() (bool, string) {
			for _, message := range w.env.Queues.DeadLetters(20) {
				if message.Attributes["reason"] == "INVALID_PAYLOAD" {
					found = message
					return true, ""
				}
			}
			return false, "nothing in the dead letter queue yet"
		})

		if found.Attributes["originalMessageId"] == "" {
			t.Error("the dead letter should carry the identifier the broker assigned")
		}
		if found.GroupID != w.walletID {
			t.Errorf("message group = %q, want the group of the original message", found.GroupID)
		}
		if !strings.Contains(found.Body, "WagerTransactionRequested") {
			t.Errorf("the original body should be preserved, got %q", found.Body)
		}
	})

	t.Run("Given a message in flight/When the instance is asked to stop/Then it finishes and the queue is not left behind", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("sigterm")
		w.publish("msg-"+external, "key-"+external, w.wager("BET", external, "10.00", ""))

		testenv.Eventually(t, 30*time.Second, "the message is settled", func() (bool, string) {
			status, _ := w.transaction(external)
			return status == 200, "the transaction is not there yet"
		})

		// When
		w.app(0).Stop()

		// Then
		if w.app(0).Running() {
			t.Error("the instance should have stopped")
		}
		if !strings.Contains(w.app(0).Output(), "consumer stopped") {
			t.Errorf("the shutdown should be observable in the log:\n%s", tail(w.app(0).Output(), 15))
		}
	})
}

func withKey(body map[string]any, key string) map[string]any {
	copied := make(map[string]any, len(body)+1)
	for name, value := range body {
		copied[name] = value
	}
	copied["idempotencyKey"] = key
	return copied
}

func tail(text string, lines int) string {
	all := strings.Split(strings.TrimSpace(text), "\n")
	if len(all) <= lines {
		return strings.Join(all, "\n")
	}
	return strings.Join(all[len(all)-lines:], "\n")
}

func decodeEvent(t *testing.T, raw string) map[string]any {
	t.Helper()

	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("the event is not valid json: %v", err)
	}
	return event
}
