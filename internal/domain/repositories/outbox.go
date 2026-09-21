package repositories

import (
	"context"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
)

type Outbox interface {
	Append(ctx context.Context, outboxEvent messaging.OutboxEvent) error
	Claim(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]messaging.OutboxEvent, error)
	MarkPublished(ctx context.Context, receipt messaging.Receipt, publishedAt time.Time) (bool, error)
	MarkFailed(ctx context.Context, receipt messaging.Receipt, reason string, nextAttemptAt time.Time) (bool, error)
	PendingStats(ctx context.Context, now time.Time) (messaging.PendingStats, error)
}
