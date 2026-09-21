package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
)

const (
	publishResultSuccess = "success"
	publishResultFailure = "failure"
	publishResultStale   = "stale_claim"
)

type OutboxSettings struct {
	Owner       string
	Lease       time.Duration
	BatchSize   int
	BackoffBase time.Duration
	BackoffMax  time.Duration
}

type PublishOutbox interface {
	Execute(ctx context.Context) (int, error)
	ReportPending(ctx context.Context) error
}

type publishOutbox struct {
	unitOfWork repositories.UnitOfWork
	notifier   port.WalletEventNotifier
	clock      port.Clock
	metrics    port.OutboxMetrics
	failpoints port.Failpoint
	logger     *slog.Logger
	settings   OutboxSettings
}

func NewPublishOutbox(
	unitOfWork repositories.UnitOfWork,
	notifier port.WalletEventNotifier,
	clock port.Clock,
	metrics port.OutboxMetrics,
	failpoints port.Failpoint,
	logger *slog.Logger,
	settings OutboxSettings,
) PublishOutbox {
	return &publishOutbox{
		unitOfWork: unitOfWork,
		notifier:   notifier,
		clock:      clock,
		metrics:    metrics,
		failpoints: failpoints,
		logger:     logger,
		settings:   settings,
	}
}

func (u *publishOutbox) Execute(ctx context.Context) (int, error) {
	claimed, err := u.claim(ctx)
	if err != nil {
		return 0, err
	}
	if len(claimed) == 0 {
		return 0, nil
	}

	u.logger.DebugContext(ctx, "events claimed for publication",
		slog.String("owner", u.settings.Owner),
		slog.Int("claimed", len(claimed)),
	)

	published := 0
	for _, pending := range claimed {
		if ctx.Err() != nil {
			return published, ctx.Err()
		}
		if u.deliver(ctx, pending) {
			published++
		}
	}

	u.logger.InfoContext(ctx, "outbox batch finished",
		slog.String("owner", u.settings.Owner),
		slog.Int("claimed", len(claimed)),
		slog.Int("published", published),
	)
	return published, nil
}

func (u *publishOutbox) claim(ctx context.Context) ([]messaging.OutboxEvent, error) {
	now := u.clock.Now()
	var claimed []messaging.OutboxEvent
	err := u.unitOfWork.Do(ctx, func(ctx context.Context, registry repositories.Registry) error {
		batch, err := registry.Outbox().Claim(ctx, u.settings.Owner, now, u.settings.Lease, u.settings.BatchSize)
		claimed = batch
		return err
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func (u *publishOutbox) deliver(ctx context.Context, pending messaging.OutboxEvent) bool {
	receipt := pending.Receipt()
	if !receipt.LeaseValid(u.clock.Now()) {
		u.metrics.OutboxPublishAttempt(publishResultStale)
		return false
	}

	publishCtx, cancel := context.WithDeadline(ctx, receipt.LockedUntil)
	err := u.notifier.Send(publishCtx, pending)
	cancel()

	u.failpoints.Hit("outbox.after_publish_before_mark")

	if err != nil {
		u.metrics.OutboxPublishAttempt(publishResultFailure)
		u.reschedule(ctx, receipt, err)
		return false
	}

	applied, markErr := u.mark(ctx, receipt)
	if markErr != nil {
		u.logger.ErrorContext(ctx, "could not record a published event",
			slog.String(logEventID, receipt.ID.String()),
			slog.String(logCause, markErr.Error()),
		)
		return false
	}
	if !applied {
		u.metrics.OutboxPublishAttempt(publishResultStale)
		u.logger.WarnContext(ctx, "publication finished on a claim that is no longer current",
			slog.String(logEventID, receipt.ID.String()),
			slog.Int("generation", receipt.Generation),
		)
		return false
	}

	u.metrics.OutboxPublishAttempt(publishResultSuccess)
	u.logger.DebugContext(ctx, "event published",
		slog.String(logEventID, receipt.ID.String()),
		slog.String("eventType", pending.EventType()),
		slog.String("partitionKey", pending.PartitionKey()),
	)
	return true
}

func (u *publishOutbox) mark(ctx context.Context, receipt messaging.Receipt) (bool, error) {
	applied := false
	err := u.unitOfWork.Do(ctx, func(ctx context.Context, registry repositories.Registry) error {
		updated, err := registry.Outbox().MarkPublished(ctx, receipt, u.clock.Now())
		applied = updated
		return err
	})
	return applied, err
}

func (u *publishOutbox) reschedule(ctx context.Context, receipt messaging.Receipt, cause error) {
	delay := backoff(u.settings.BackoffBase, u.settings.BackoffMax, receipt.Generation)
	nextAttempt := u.clock.Now().Add(delay)

	err := u.unitOfWork.Do(ctx, func(ctx context.Context, registry repositories.Registry) error {
		_, markErr := registry.Outbox().MarkFailed(ctx, receipt, cause.Error(), nextAttempt)
		return markErr
	})
	if err != nil {
		u.logger.ErrorContext(ctx, "could not reschedule a failed publication",
			slog.String(logEventID, receipt.ID.String()),
			slog.String(logCause, err.Error()),
		)
		return
	}

	u.logger.WarnContext(ctx, "event publication failed and was rescheduled",
		slog.String(logEventID, receipt.ID.String()),
		slog.Int(logAttempts, receipt.Generation),
		slog.Time("nextAttemptAt", nextAttempt),
		slog.String(logCause, cause.Error()),
	)
}

func (u *publishOutbox) ReportPending(ctx context.Context) error {
	var stats messaging.PendingStats
	err := u.unitOfWork.Read(ctx, func(ctx context.Context, registry repositories.Registry) error {
		result, err := registry.Outbox().PendingStats(ctx, u.clock.Now())
		stats = result
		return err
	})
	if err != nil {
		return err
	}

	u.metrics.OutboxPending(stats.Pending, stats.OldestPendingAge)
	if stats.Pending > 0 {
		u.logger.InfoContext(ctx, "events waiting in the outbox",
			slog.Int64("pending", stats.Pending),
			slog.Duration("oldestPendingAge", stats.OldestPendingAge),
		)
	}
	return nil
}
