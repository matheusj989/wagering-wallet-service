package event_test

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/event"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"strings"
	"testing"
	"time"
)

func TestEventSnapshot(t *testing.T) {
	t.Run("Given a pending reference event/When its transaction is rescheduled and settled/Then the original event remains byte identical", func(t *testing.T) {
		// Given
		now := time.Now()
		value, _ := money.Parse("25.00", "BRL")
		operation, err := wagering.NewExternal(wagering.Command{Origin: wagering.HTTP, Kind: wagering.Rollback, ProviderID: "provider", ExternalTransactionID: "rollback", IdempotencyKey: "key", PayloadHash: strings.Repeat("a", 64), WalletID: uuid.New(), PlayerID: uuid.New(), RoundID: "round", GameID: "game", Amount: value, ReferenceExternalID: "bet", CorrelationID: "trace"}, uuid.New(), now)
		if err != nil {
			t.Fatal(err)
		}
		if err := operation.MarkPendingReference(now.Add(time.Second), now.Add(time.Hour), now); err != nil {
			t.Fatal(err)
		}
		envelope, err := event.NewPendingReference(uuid.New(), operation)
		if err != nil {
			t.Fatal(err)
		}
		before, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		// When
		if err := operation.Reschedule(now.Add(time.Minute), now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := operation.MarkProcessed(value, uuid.New(), now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		// Then
		if !bytes.Equal(before, after) {
			t.Fatal("the event retained mutable transaction state")
		}
	})
}
