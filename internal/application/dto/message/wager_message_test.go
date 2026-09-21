package message_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/message"
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
)

const body = `{
  "messageId": "message-1",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-21T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-1",
    "playerId": "0199c0d0-0000-7000-8000-000000000001",
    "walletId": "0199c0d0-0000-7000-8000-000000000002",
    "roundId": "round-1",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": {"amount": "25.00", "currency": "BRL"},
    "referenceExternalTransactionId": null,
    "idempotencyKey": "provider-a:transaction-1"
  }
}`

func TestDecode(t *testing.T) {
	t.Run("Given a message the ingestion service would send/When it is decoded/Then every field is read", func(t *testing.T) {
		// Given, When
		wager, err := message.Decode([]byte(body))

		// Then
		if err != nil {
			t.Fatalf("the message should be accepted, got %v", err)
		}
		if wager.MessageID != "message-1" || wager.Type != message.WagerTransactionRequested {
			t.Errorf("envelope = %s of type %s", wager.MessageID, wager.Type)
		}
		if wager.Data.Money.String() != "25.00" || wager.Data.IdempotencyKey != "provider-a:transaction-1" {
			t.Errorf("data = %s with key %q", wager.Data.Money.String(), wager.Data.IdempotencyKey)
		}
		if wager.Reference() != "" {
			t.Errorf("reference = %q, want empty", wager.Reference())
		}
	})

	t.Run("Given a reference in the message/When it is decoded/Then it is read as text", func(t *testing.T) {
		// Given
		withReference := strings.Replace(body,
			`"referenceExternalTransactionId": null`,
			`"referenceExternalTransactionId": "transaction-0"`, 1)

		// When
		wager, err := message.Decode([]byte(withReference))

		// Then
		if err != nil {
			t.Fatalf("the message should be accepted, got %v", err)
		}
		if wager.Reference() != "transaction-0" {
			t.Errorf("reference = %q, want transaction-0", wager.Reference())
		}
	})

	t.Run("Given a body the contract does not accept/When it is decoded/Then it is refused", func(t *testing.T) {
		scenarios := []struct {
			name string
			body string
		}{
			{"not json", "{"},
			{"two documents", body + body},
			{"trailing bracket", body + "]"},
			{"trailing brace", body + "}"},
			{"missing timestamp", strings.Replace(body, `"occurredAt": "2026-09-21T12:00:00.000Z",`, "", 1)},
			{"invalid timestamp", strings.Replace(body, "2026-09-21T12:00:00.000Z", "not-a-date", 1)},
			{"unknown field", strings.Replace(body, `"gameId"`, `"nickname"`, 1)},
			{"unknown type", strings.Replace(body, "WagerTransactionRequested", "SomethingElse", 1)},
			{"message id missing", strings.Replace(body, `"messageId": "message-1"`, `"messageId": ""`, 1)},
			{"amount as a number", strings.Replace(body, `"amount": "25.00"`, `"amount": 25.00`, 1)},
			{"currency outside the list", strings.Replace(body, `"currency": "BRL"`, `"currency": "JPY"`, 1)},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given, When
				_, err := message.Decode([]byte(scenario.body))

				// Then
				if err == nil {
					t.Fatal("the message should have been refused")
				}
			})
		}
	})

	t.Run("Given a message id longer than the contract allows/When it is decoded/Then the field is named", func(t *testing.T) {
		// Given
		long := strings.Replace(body, `"messageId": "message-1"`,
			`"messageId": "`+strings.Repeat("m", 256)+`"`, 1)

		// When
		_, err := message.Decode([]byte(long))

		// Then
		var problems *validation.Error
		if !errors.As(err, &problems) {
			t.Fatalf("error = %v, want a validation error", err)
		}
		if problems.Fields[0].Field != "messageId" {
			t.Errorf("reported field = %s, want messageId", problems.Fields[0].Field)
		}
	})
}
