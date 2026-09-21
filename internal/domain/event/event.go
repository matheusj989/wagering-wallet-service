package event

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrMissingIdentifier = errors.New("event: identifier must not be empty")
	ErrMissingTimestamp  = errors.New("event: timestamp must not be empty")
	ErrMissingField      = errors.New("event: required field is empty")
)

const Version = 1

type Type string

const (
	TypeProcessed        Type = "WagerTransactionProcessed"
	TypeRejected         Type = "WagerTransactionRejected"
	TypeBalanceChanged   Type = "WalletBalanceChanged"
	TypePendingReference Type = "WagerTransactionPendingReference"
)

func (t Type) String() string { return string(t) }

type AggregateType string

const (
	AggregateWallet      AggregateType = "wallet"
	AggregateTransaction AggregateType = "wager_transaction"
)

func (a AggregateType) String() string { return string(a) }

type Timestamp time.Time

func (t Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC().Format("2006-01-02T15:04:05.000Z07:00"))
}

func (t Timestamp) Time() time.Time {
	return time.Time(t)
}

type Envelope struct {
	eventID       uuid.UUID
	eventType     Type
	aggregateType AggregateType
	aggregateID   uuid.UUID
	correlationID string
	causationID   string
	occurredAt    time.Time
	data          any
}

func newEnvelope(
	eventID uuid.UUID,
	eventType Type,
	aggregateType AggregateType,
	aggregateID uuid.UUID,
	correlationID string,
	causationID uuid.UUID,
	occurredAt time.Time,
	data any,
) (Envelope, error) {
	if eventID == uuid.Nil || aggregateID == uuid.Nil || causationID == uuid.Nil {
		return Envelope{}, ErrMissingIdentifier
	}
	if correlationID == "" {
		return Envelope{}, ErrMissingField
	}
	if occurredAt.IsZero() {
		return Envelope{}, ErrMissingTimestamp
	}
	return Envelope{
		eventID:       eventID,
		eventType:     eventType,
		aggregateType: aggregateType,
		aggregateID:   aggregateID,
		correlationID: correlationID,
		causationID:   causationID.String(),
		occurredAt:    occurredAt.UTC(),
		data:          data,
	}, nil
}

func (e Envelope) EventID() uuid.UUID           { return e.eventID }
func (e Envelope) EventType() Type              { return e.eventType }
func (e Envelope) AggregateType() AggregateType { return e.aggregateType }
func (e Envelope) AggregateID() uuid.UUID       { return e.aggregateID }
func (e Envelope) CorrelationID() string        { return e.correlationID }
func (e Envelope) CausationID() string          { return e.causationID }
func (e Envelope) OccurredAt() time.Time        { return e.occurredAt }
func (e Envelope) EventVersion() int            { return Version }

func (e Envelope) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		EventID       uuid.UUID `json:"eventId"`
		EventType     Type      `json:"eventType"`
		AggregateID   uuid.UUID `json:"aggregateId"`
		CorrelationID string    `json:"correlationId"`
		CausationID   string    `json:"causationId,omitempty"`
		OccurredAt    Timestamp `json:"occurredAt"`
		Version       int       `json:"version"`
		Data          any       `json:"data"`
	}{
		EventID:       e.eventID,
		EventType:     e.eventType,
		AggregateID:   e.aggregateID,
		CorrelationID: e.correlationID,
		CausationID:   e.causationID,
		OccurredAt:    Timestamp(e.occurredAt),
		Version:       Version,
		Data:          e.data,
	})
}
