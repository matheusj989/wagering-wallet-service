package sqs_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs/listener"
)

type stubListener struct {
	name     string
	watching string
}

func (s stubListener) Name() string     { return s.name }
func (s stubListener) Watching() string { return s.watching }

func (s stubListener) Handle(context.Context, listener.Message) error { return nil }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewConsumer(t *testing.T) {
	t.Run("Given listeners on different queues/When the consumer is built/Then it accepts them", func(t *testing.T) {
		// Given
		wager := stubListener{name: "wager", watching: "http://localhost:4566/000000000000/wager.fifo"}
		refunds := stubListener{name: "refunds", watching: "http://localhost:4566/000000000000/refunds.fifo"}

		// When
		consumer, err := sqs.NewConsumer(nil, "http://localhost:4566/000000000000/dlq.fifo",
			nil, discardLogger(), sqs.ConsumerSettings{Workers: 4}, wager, refunds)

		// Then
		if err != nil {
			t.Fatalf("the consumer should have been built, got %v", err)
		}
		if consumer == nil {
			t.Fatal("the consumer should not be nil")
		}
	})

	t.Run("Given two listeners on the same queue/When the consumer is built/Then it refuses to start", func(t *testing.T) {
		// Given
		first := stubListener{name: "first", watching: "http://localhost:4566/000000000000/wager.fifo"}
		second := stubListener{name: "second", watching: "http://localhost:4566/000000000000/wager.fifo"}

		// When
		_, err := sqs.NewConsumer(nil, "dlq", nil, discardLogger(), sqs.ConsumerSettings{Workers: 4}, first, second)

		// Then
		if err == nil {
			t.Fatal("two listeners on one queue should be refused")
		}
		if !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
			t.Errorf("error = %v, want both listeners named", err)
		}
	})

	t.Run("Given a listener that watches nothing/When the consumer is built/Then it refuses to start", func(t *testing.T) {
		// Given
		silent := stubListener{name: "silent"}

		// When
		_, err := sqs.NewConsumer(nil, "dlq", nil, discardLogger(), sqs.ConsumerSettings{Workers: 4}, silent)

		// Then
		if err == nil {
			t.Fatal("a listener without a queue should be refused")
		}
		if !strings.Contains(err.Error(), "silent") {
			t.Errorf("error = %v, want the listener named", err)
		}
	})

	t.Run("Given no listeners/When the consumer is stopped before starting/Then nothing blocks", func(t *testing.T) {
		// Given
		consumer, err := sqs.NewConsumer(nil, "dlq", nil, discardLogger(), sqs.ConsumerSettings{Workers: 4})
		if err != nil {
			t.Fatalf("an empty consumer is valid, got %v", err)
		}

		// When
		stopErr := consumer.Stop(context.Background())

		// Then
		if stopErr != nil {
			t.Errorf("stopping before starting should be a no-op, got %v", stopErr)
		}
	})
}
