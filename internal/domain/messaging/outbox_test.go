package messaging_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/event"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

func TestOutboxSnapshot(t *testing.T) {
	t.Run("Given a persisted event/When input and returned byte slices are changed/Then the outbox keeps its original snapshot", func(t *testing.T) {
		// Given
		now := time.Now()
		amount, _ := money.Parse("25.00", "BRL")
		operation, err := wagering.NewOpening(uuid.New(), uuid.New(), uuid.New(), amount, "correlation", now)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := event.NewProcessed(uuid.New(), operation, amount)
		if err != nil {
			t.Fatal(err)
		}
		original, err := messaging.NewOutboxEvent(envelope, operation.WalletID(), now)
		if err != nil {
			t.Fatal(err)
		}
		input := original.Payload()
		want := bytes.Clone(input)
		restored, err := messaging.RehydrateOutboxEvent(messaging.OutboxSnapshot{ID: original.ID(), AggregateID: original.AggregateID(), EventType: original.EventType(), PartitionKey: original.PartitionKey(), Payload: input, AggregateType: original.AggregateType(), EventVersion: original.EventVersion(), CorrelationID: original.CorrelationID(), CausationID: original.CausationID(), OccurredAt: original.OccurredAt(), NextAttemptAt: original.NextAttemptAt()})
		if err != nil {
			t.Fatal(err)
		}
		// When
		input[0] = '!'
		returned := restored.Payload()
		returned[0] = '?'
		// Then
		if !bytes.Equal(restored.Payload(), want) || !bytes.Equal(original.Payload(), want) {
			t.Fatal("event snapshot shares mutable bytes")
		}
	})
}
