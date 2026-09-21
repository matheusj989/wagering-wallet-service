package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
)

const publisherOwner = "instance-1"

var outboxSettings = usecase.OutboxSettings{
	Owner:       publisherOwner,
	Lease:       30 * time.Second,
	BatchSize:   10,
	BackoffBase: time.Second,
	BackoffMax:  5 * time.Minute,
}

func publishUseCase(h *harness) usecase.PublishOutbox {
	return usecase.NewPublishOutbox(h.unitOfWork, h.notifier, h.clock, h.outboxMetrics, h.failpoints, h.logger, outboxSettings)
}

func claimedEvent(t *testing.T, lockedUntil time.Time, generation int) messaging.OutboxEvent {
	t.Helper()

	outboxEvent, err := messaging.RehydrateOutboxEvent(messaging.OutboxSnapshot{
		ID:            uuid.Must(uuid.NewV7()),
		AggregateType: "WALLET",
		AggregateID:   uuid.Must(uuid.NewV7()),
		EventType:     "WagerTransactionProcessed",
		EventVersion:  1,
		CorrelationID: "correlation-1",
		PartitionKey:  uuid.Must(uuid.NewV7()).String(),
		Payload:       []byte(`{"eventId":"1"}`),
		OccurredAt:    clockReading,
		Attempts:      generation,
		NextAttemptAt: clockReading,
		LockedBy:      publisherOwner,
		LockedUntil:   lockedUntil,
	})
	if err != nil {
		t.Fatalf("the outbox event could not be rehydrated: %v", err)
	}
	return outboxEvent
}

func TestPublishOutbox(t *testing.T) {
	t.Run("Given claimed events/When the queue accepts them/Then each one is marked as published", func(t *testing.T) {
		// Given
		h := newHarness(t)
		first := claimedEvent(t, clockReading.Add(30*time.Second), 0)
		second := claimedEvent(t, clockReading.Add(30*time.Second), 0)

		h.writes(3)
		h.outbox.EXPECT().Claim(gomock.Any(), publisherOwner, clockReading, outboxSettings.Lease, outboxSettings.BatchSize).
			Return([]messaging.OutboxEvent{first, second}, nil)
		h.notifier.EXPECT().Send(gomock.Any(), gomock.Any()).Times(2).Return(nil)
		h.outbox.EXPECT().MarkPublished(gomock.Any(), first.Receipt(), clockReading).Return(true, nil)
		h.outbox.EXPECT().MarkPublished(gomock.Any(), second.Receipt(), clockReading).Return(true, nil)
		h.outboxMetrics.EXPECT().OutboxPublishAttempt("success").Times(2)

		// When
		published, err := publishUseCase(h).Execute(context.Background())

		// Then
		if err != nil {
			t.Fatalf("the batch should have been published, got %v", err)
		}
		if published != 2 {
			t.Errorf("published = %d, want 2", published)
		}
	})

	t.Run("Given the queue refuses an event/When it is published/Then it is rescheduled with backoff", func(t *testing.T) {
		// Given
		h := newHarness(t)
		pending := claimedEvent(t, clockReading.Add(30*time.Second), 0)
		refused := errors.New("the queue is unreachable")

		h.writes(2)
		h.outbox.EXPECT().Claim(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return([]messaging.OutboxEvent{pending}, nil)
		h.notifier.EXPECT().Send(gomock.Any(), gomock.Any()).Return(refused)
		h.outbox.EXPECT().MarkFailed(gomock.Any(), pending.Receipt(), refused.Error(), clockReading.Add(outboxSettings.BackoffBase)).
			Return(true, nil)
		h.outboxMetrics.EXPECT().OutboxPublishAttempt("failure")

		// When
		published, err := publishUseCase(h).Execute(context.Background())

		// Then
		if err != nil {
			t.Fatalf("a failed publication is not a batch failure, got %v", err)
		}
		if published != 0 {
			t.Errorf("published = %d, want 0", published)
		}
	})

	t.Run("Given a claim whose lease already expired/When the batch runs/Then the event is left for the next owner", func(t *testing.T) {
		// Given
		h := newHarness(t)
		expired := claimedEvent(t, clockReading.Add(-time.Second), 2)

		h.writes(1)
		h.outbox.EXPECT().Claim(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return([]messaging.OutboxEvent{expired}, nil)
		h.outboxMetrics.EXPECT().OutboxPublishAttempt("stale_claim")

		// When
		published, err := publishUseCase(h).Execute(context.Background())

		// Then
		if err != nil {
			t.Fatalf("an expired lease is not a failure, got %v", err)
		}
		if published != 0 {
			t.Errorf("published = %d, want 0", published)
		}
	})

	t.Run("Given the claim was taken over while publishing/When the event is marked/Then it does not count as published", func(t *testing.T) {
		// Given
		h := newHarness(t)
		pending := claimedEvent(t, clockReading.Add(30*time.Second), 1)

		h.writes(2)
		h.outbox.EXPECT().Claim(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return([]messaging.OutboxEvent{pending}, nil)
		h.notifier.EXPECT().Send(gomock.Any(), gomock.Any()).Return(nil)
		h.outbox.EXPECT().MarkPublished(gomock.Any(), pending.Receipt(), gomock.Any()).Return(false, nil)
		h.outboxMetrics.EXPECT().OutboxPublishAttempt("stale_claim")

		// When
		published, err := publishUseCase(h).Execute(context.Background())

		// Then
		if err != nil {
			t.Fatalf("a lost claim is not a failure, got %v", err)
		}
		if published != 0 {
			t.Errorf("published = %d, want 0", published)
		}
	})

	t.Run("Given nothing was claimed/When the batch runs/Then the queue is not touched", func(t *testing.T) {
		// Given
		h := newHarness(t)
		h.writes(1)
		h.outbox.EXPECT().Claim(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)

		// When
		published, err := publishUseCase(h).Execute(context.Background())

		// Then
		if err != nil || published != 0 {
			t.Fatalf("published = %d with error %v, want 0 and no error", published, err)
		}
	})

	t.Run("Given events waiting in the outbox/When the backlog is reported/Then the gauges receive it", func(t *testing.T) {
		// Given
		h := newHarness(t)
		h.reads(1)
		h.outbox.EXPECT().PendingStats(gomock.Any(), clockReading).
			Return(messaging.PendingStats{Pending: 3, OldestPendingAge: 90 * time.Second}, nil)
		h.outboxMetrics.EXPECT().OutboxPending(int64(3), 90*time.Second)

		// When
		err := publishUseCase(h).ReportPending(context.Background())

		// Then
		if err != nil {
			t.Fatalf("the backlog should have been reported, got %v", err)
		}
	})
}
