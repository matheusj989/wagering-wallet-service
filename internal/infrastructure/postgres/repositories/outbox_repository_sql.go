package repositories

import (
	"context"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
)

const outboxColumns = `id, aggregate_type, aggregate_id, event_type, event_version, correlation_id, causation_id,
	partition_key, payload, occurred_at, attempts, next_attempt_at, locked_by, locked_until, last_error, published_at`

type OutboxRepository struct {
	queries Querier
}

func NewOutboxRepository(queries Querier) *OutboxRepository {
	return &OutboxRepository{queries: queries}
}

func (r *OutboxRepository) Append(ctx context.Context, outboxEvent messaging.OutboxEvent) error {
	const statement = `
		INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, event_version, correlation_id,
			causation_id, partition_key, payload, occurred_at, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	_, err := r.queries.Exec(ctx, statement,
		outboxEvent.ID(), outboxEvent.AggregateType(), outboxEvent.AggregateID(), outboxEvent.EventType(),
		outboxEvent.EventVersion(), outboxEvent.CorrelationID(), nullString(outboxEvent.CausationID()),
		outboxEvent.PartitionKey(), outboxEvent.Payload(), outboxEvent.OccurredAt(), outboxEvent.NextAttemptAt())
	if err != nil {
		return Classify(err)
	}
	return nil
}

func (r *OutboxRepository) Claim(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]messaging.OutboxEvent, error) {
	const statement = `
		UPDATE outbox_events
		   SET locked_by = $1, locked_until = $2, attempts = attempts + 1
		 WHERE id IN (
		       SELECT id FROM outbox_events
		        WHERE published_at IS NULL
		          AND next_attempt_at <= $3
		          AND (locked_until IS NULL OR locked_until < $3)
		        ORDER BY next_attempt_at, id
		          FOR UPDATE SKIP LOCKED
		        LIMIT $4)
		RETURNING ` + outboxColumns

	rows, err := r.queries.Query(ctx, statement, owner, now.Add(lease), now, limit)
	if err != nil {
		return nil, Classify(err)
	}
	defer rows.Close()

	claimed := make([]messaging.OutboxEvent, 0, limit)
	for rows.Next() {
		outboxEvent, err := scanOutboxEvent(rows)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, outboxEvent)
	}
	if err := rows.Err(); err != nil {
		return nil, Classify(err)
	}
	return claimed, nil
}

func (r *OutboxRepository) MarkPublished(ctx context.Context, receipt messaging.Receipt, publishedAt time.Time) (bool, error) {
	const statement = `
		UPDATE outbox_events
		   SET published_at = $1, locked_by = NULL, locked_until = NULL, last_error = NULL
		 WHERE id = $2 AND locked_by = $3 AND attempts = $4
		   AND published_at IS NULL AND locked_until > clock_timestamp()`

	return r.finish(ctx, statement, publishedAt, receipt)
}

func (r *OutboxRepository) MarkFailed(ctx context.Context, receipt messaging.Receipt, reason string, nextAttemptAt time.Time) (bool, error) {
	const statement = `
		UPDATE outbox_events
		   SET next_attempt_at = $1, last_error = $5, locked_by = NULL, locked_until = NULL
		 WHERE id = $2 AND locked_by = $3 AND attempts = $4
		   AND published_at IS NULL AND locked_until > clock_timestamp()`

	tag, err := r.queries.Exec(ctx, statement, nextAttemptAt, receipt.ID, receipt.Owner, receipt.Generation, truncate(reason, 1000))
	if err != nil {
		return false, Classify(err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *OutboxRepository) PendingStats(ctx context.Context, now time.Time) (messaging.PendingStats, error) {
	const statement = `
		SELECT COUNT(*), COALESCE(EXTRACT(EPOCH FROM ($1 - MIN(occurred_at))), 0)
		  FROM outbox_events
		 WHERE published_at IS NULL`

	var (
		pending   int64
		oldestAge float64
	)
	if err := r.queries.QueryRow(ctx, statement, now).Scan(&pending, &oldestAge); err != nil {
		return messaging.PendingStats{}, Classify(err)
	}
	if oldestAge < 0 {
		oldestAge = 0
	}
	return messaging.PendingStats{
		Pending:          pending,
		OldestPendingAge: time.Duration(oldestAge * float64(time.Second)),
	}, nil
}

func (r *OutboxRepository) finish(ctx context.Context, statement string, moment time.Time, receipt messaging.Receipt) (bool, error) {
	tag, err := r.queries.Exec(ctx, statement, moment, receipt.ID, receipt.Owner, receipt.Generation)
	if err != nil {
		return false, Classify(err)
	}
	return tag.RowsAffected() > 0, nil
}

func scanOutboxEvent(row scanner) (messaging.OutboxEvent, error) {
	var (
		snapshot    messaging.OutboxSnapshot
		causationID *string
		lockedBy    *string
		lockedUntil *time.Time
		lastError   *string
		publishedAt *time.Time
	)

	err := row.Scan(&snapshot.ID, &snapshot.AggregateType, &snapshot.AggregateID, &snapshot.EventType,
		&snapshot.EventVersion, &snapshot.CorrelationID, &causationID, &snapshot.PartitionKey,
		&snapshot.Payload, &snapshot.OccurredAt, &snapshot.Attempts, &snapshot.NextAttemptAt,
		&lockedBy, &lockedUntil, &lastError, &publishedAt)
	if err != nil {
		return messaging.OutboxEvent{}, Classify(err)
	}

	snapshot.CausationID = text(causationID)
	snapshot.LockedBy = text(lockedBy)
	snapshot.LockedUntil = moment(lockedUntil)
	snapshot.LastError = text(lastError)
	snapshot.PublishedAt = moment(publishedAt)

	return messaging.RehydrateOutboxEvent(snapshot)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
