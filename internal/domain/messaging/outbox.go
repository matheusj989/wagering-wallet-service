package messaging

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/event"
)

var (
	ErrMissingIdentifier = errors.New("messaging: identifier must not be empty")
	ErrMissingTimestamp  = errors.New("messaging: timestamp must not be empty")
	ErrMissingField      = errors.New("messaging: required field is empty")
	ErrInvalidOutcome    = errors.New("messaging: outcome is not valid")
)

type OutboxEvent struct {
	id            uuid.UUID
	aggregateType string
	aggregateID   uuid.UUID
	eventType     string
	eventVersion  int
	correlationID string
	causationID   string
	partitionKey  string
	payload       []byte
	occurredAt    time.Time

	attempts      int
	nextAttemptAt time.Time
	lockedBy      string
	lockedUntil   time.Time
	lastError     string
	publishedAt   time.Time
}

func NewOutboxEvent(envelope event.Envelope, partitionKey uuid.UUID, now time.Time) (OutboxEvent, error) {
	if partitionKey == uuid.Nil {
		return OutboxEvent{}, ErrMissingIdentifier
	}
	if now.IsZero() {
		return OutboxEvent{}, ErrMissingTimestamp
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		id:            envelope.EventID(),
		aggregateType: envelope.AggregateType().String(),
		aggregateID:   envelope.AggregateID(),
		eventType:     envelope.EventType().String(),
		eventVersion:  envelope.EventVersion(),
		correlationID: envelope.CorrelationID(),
		causationID:   envelope.CausationID(),
		partitionKey:  partitionKey.String(),
		payload:       payload,
		occurredAt:    envelope.OccurredAt(),
		nextAttemptAt: now.UTC(),
	}, nil
}

type OutboxSnapshot struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	EventVersion  int
	CorrelationID string
	CausationID   string
	PartitionKey  string
	Payload       []byte
	OccurredAt    time.Time
	Attempts      int
	NextAttemptAt time.Time
	LockedBy      string
	LockedUntil   time.Time
	LastError     string
	PublishedAt   time.Time
}

func RehydrateOutboxEvent(snapshot OutboxSnapshot) (OutboxEvent, error) {
	if snapshot.ID == uuid.Nil || snapshot.AggregateID == uuid.Nil {
		return OutboxEvent{}, ErrMissingIdentifier
	}
	if len(snapshot.Payload) == 0 || snapshot.PartitionKey == "" || snapshot.EventType == "" {
		return OutboxEvent{}, ErrMissingField
	}
	return OutboxEvent{
		id:            snapshot.ID,
		aggregateType: snapshot.AggregateType,
		aggregateID:   snapshot.AggregateID,
		eventType:     snapshot.EventType,
		eventVersion:  snapshot.EventVersion,
		correlationID: snapshot.CorrelationID,
		causationID:   snapshot.CausationID,
		partitionKey:  snapshot.PartitionKey,
		payload:       slices.Clone(snapshot.Payload),
		occurredAt:    snapshot.OccurredAt,
		attempts:      snapshot.Attempts,
		nextAttemptAt: snapshot.NextAttemptAt,
		lockedBy:      snapshot.LockedBy,
		lockedUntil:   snapshot.LockedUntil,
		lastError:     snapshot.LastError,
		publishedAt:   snapshot.PublishedAt,
	}, nil
}

func (e OutboxEvent) ID() uuid.UUID          { return e.id }
func (e OutboxEvent) AggregateType() string  { return e.aggregateType }
func (e OutboxEvent) AggregateID() uuid.UUID { return e.aggregateID }
func (e OutboxEvent) EventType() string      { return e.eventType }
func (e OutboxEvent) EventVersion() int      { return e.eventVersion }
func (e OutboxEvent) CorrelationID() string  { return e.correlationID }
func (e OutboxEvent) CausationID() string    { return e.causationID }
func (e OutboxEvent) PartitionKey() string   { return e.partitionKey }
func (e OutboxEvent) OccurredAt() time.Time  { return e.occurredAt }
func (e OutboxEvent) Attempts() int          { return e.attempts }
func (e OutboxEvent) NextAttemptAt() time.Time {
	return e.nextAttemptAt
}
func (e OutboxEvent) LockedUntil() time.Time { return e.lockedUntil }
func (e OutboxEvent) Published() bool        { return !e.publishedAt.IsZero() }

func (e OutboxEvent) Payload() []byte {
	return slices.Clone(e.payload)
}

func (e OutboxEvent) Receipt() Receipt {
	return Receipt{
		ID:          e.id,
		Owner:       e.lockedBy,
		Generation:  e.attempts,
		LockedUntil: e.lockedUntil,
	}
}

type Receipt struct {
	ID          uuid.UUID
	Owner       string
	Generation  int
	LockedUntil time.Time
}

func (r Receipt) LeaseValid(now time.Time) bool {
	return r.Owner != "" && !r.LockedUntil.IsZero() && now.Before(r.LockedUntil)
}

type PendingStats struct {
	Pending          int64
	OldestPendingAge time.Duration
}
