package listener_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs/listener"
	"github.com/matheusj989/wagering-wallet-service/internal/mocks"
)

const queueURL = "http://localhost:4566/000000000000/wager-transactions.fifo"

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

type listenerHarness struct {
	listener *listener.WagerTransactionListener
	process  *mocks.MockProcessWagerTransaction
	metrics  *mocks.MockConsumerMetrics
}

func newListenerHarness(t *testing.T) *listenerHarness {
	t.Helper()

	ctrl := gomock.NewController(t)
	process := mocks.NewMockProcessWagerTransaction(ctrl)
	metrics := mocks.NewMockConsumerMetrics(ctrl)

	return &listenerHarness{
		listener: listener.NewWagerTransactionListener(queueURL, process, metrics,
			slog.New(slog.NewTextHandler(io.Discard, nil))),
		process: process,
		metrics: metrics,
	}
}

func delivery(payload string) listener.Message {
	return listener.Message{
		BrokerID:     "broker-1",
		Body:         []byte(payload),
		GroupID:      "0199c0d0-0000-7000-8000-000000000002",
		ReceiveCount: 1,
		Attributes:   map[string]string{},
	}
}

func TestWagerTransactionListener(t *testing.T) {
	t.Run("Given the listener is built/When it is asked what it watches/Then it answers the queue it was given", func(t *testing.T) {
		// Given, When
		h := newListenerHarness(t)

		// Then
		if h.listener.Watching() != queueURL {
			t.Errorf("watching = %s, want %s", h.listener.Watching(), queueURL)
		}
	})

	t.Run("Given a message the ingestion service sent/When it is handled/Then the use case receives the operation", func(t *testing.T) {
		// Given
		h := newListenerHarness(t)
		var received dto.ProcessWagerInput
		h.process.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, input dto.ProcessWagerInput) (dto.ProcessWagerResult, error) {
				received = input
				return dto.ProcessWagerResult{TransactionID: uuid.Must(uuid.NewV7()), Status: wagering.Processed}, nil
			})

		// When
		err := h.listener.Handle(context.Background(), delivery(body))

		// Then
		if err != nil {
			t.Fatalf("the message should have been handled, got %v", err)
		}
		if received.Origin != wagering.Queue || received.MessageID != "message-1" {
			t.Errorf("input = %s from message %q", received.Origin, received.MessageID)
		}
		if received.IdempotencyKey != "provider-a:transaction-1" || received.RawBodyHash == "" {
			t.Errorf("input = key %q with hash %q", received.IdempotencyKey, received.RawBodyHash)
		}
	})

	t.Run("Given a message that was already handled/When it arrives again/Then the redelivery is counted", func(t *testing.T) {
		// Given
		h := newListenerHarness(t)
		h.process.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(dto.ProcessWagerResult{
			TransactionID: uuid.Must(uuid.NewV7()),
			Status:        wagering.Processed,
			InboxOutcome:  messaging.OutcomeProcessed,
		}, nil)
		h.metrics.EXPECT().MessageRedelivered()

		// When
		err := h.listener.Handle(context.Background(), delivery(body))

		// Then
		if err != nil {
			t.Fatalf("a redelivery is not a failure, got %v", err)
		}
	})

	t.Run("Given a message the contract refuses/When it is handled/Then it is rejected and the use case is never called", func(t *testing.T) {
		scenarios := []struct {
			name    string
			payload string
			reason  string
		}{
			{"not an envelope", "{", listener.ReasonInvalidPayload},
			{"unknown type", strings.Replace(body, "WagerTransactionRequested", "Something", 1), listener.ReasonInvalidPayload},
			{"wallet is not a uuid", strings.Replace(body, "0199c0d0-0000-7000-8000-000000000002", "not-a-uuid", 1), listener.ReasonInvalidPayload},
			{"opening over the queue", strings.Replace(body, `"kind": "BET"`, `"kind": "OPENING"`, 1), listener.ReasonOpeningNotAllowed},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				h := newListenerHarness(t)

				// When
				err := h.listener.Handle(context.Background(), delivery(scenario.payload))

				// Then
				var rejection *listener.Rejection
				if !errors.As(err, &rejection) {
					t.Fatalf("error = %v, want a rejection", err)
				}
				if rejection.Reason != scenario.reason {
					t.Errorf("reason = %s, want %s", rejection.Reason, scenario.reason)
				}
			})
		}
	})

	t.Run("Given the use case fails/When the failure is classified/Then the consumer is told what to do", func(t *testing.T) {
		transient := errors.New("the database is busy")
		conflict := &usecase.ConflictError{Kind: usecase.ConflictExternalID, TransactionID: uuid.Must(uuid.NewV7())}

		scenarios := []struct {
			name    string
			failure error
			reason  string
			unknown bool
			retry   bool
		}{
			{"wallet does not exist", wallet.ErrNotFound, listener.ReasonWalletNotFound, false, false},
			{"same id with another body", messaging.ErrPayloadMismatch, listener.ReasonInboxMismatch, false, false},
			{"idempotency conflict", conflict, string(usecase.ConflictExternalID), false, false},
			{"commit outcome unknown", repositories.ErrCommitOutcomeUnknown, "", true, false},
			{"transient failure", transient, "", false, true},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				h := newListenerHarness(t)
				h.process.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(dto.ProcessWagerResult{}, scenario.failure)

				// When
				err := h.listener.Handle(context.Background(), delivery(body))

				// Then
				var rejection *listener.Rejection
				switch {
				case scenario.unknown:
					if !errors.Is(err, listener.ErrUnknownOutcome) {
						t.Fatalf("error = %v, want an unknown outcome", err)
					}
				case scenario.retry:
					if errors.As(err, &rejection) || errors.Is(err, listener.ErrUnknownOutcome) {
						t.Fatalf("error = %v, want the plain failure so the message comes back", err)
					}
					if !errors.Is(err, transient) {
						t.Errorf("error = %v, want %v", err, transient)
					}
				default:
					if !errors.As(err, &rejection) {
						t.Fatalf("error = %v, want a rejection", err)
					}
					if rejection.Reason != scenario.reason {
						t.Errorf("reason = %s, want %s", rejection.Reason, scenario.reason)
					}
				}
			})
		}
	})

	t.Run("Given a correlation id on the message/When it is handled/Then it travels with the operation", func(t *testing.T) {
		scenarios := []struct {
			name      string
			attribute string
			want      string
		}{
			{"sent by the publisher", "correlation-from-the-edge", "correlation-from-the-edge"},
			{"absent", "", "message-1"},
			{"not printable ascii", "quebra\nlinha", "message-1"},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				h := newListenerHarness(t)
				message := delivery(body)
				if scenario.attribute != "" {
					message.Attributes["correlationId"] = scenario.attribute
				}

				var received dto.ProcessWagerInput
				h.process.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, input dto.ProcessWagerInput) (dto.ProcessWagerResult, error) {
						received = input
						return dto.ProcessWagerResult{TransactionID: uuid.Must(uuid.NewV7())}, nil
					})

				// When
				if err := h.listener.Handle(context.Background(), message); err != nil {
					t.Fatalf("the message should have been handled, got %v", err)
				}

				// Then
				if received.CorrelationID != scenario.want {
					t.Errorf("correlationId = %q, want %q", received.CorrelationID, scenario.want)
				}
			})
		}
	})
}
