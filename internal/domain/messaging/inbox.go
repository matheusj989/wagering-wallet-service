package messaging

import (
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
)

type Outcome string

const (
	OutcomeProcessed        Outcome = "PROCESSED"
	OutcomeRejected         Outcome = "REJECTED"
	OutcomePendingReference Outcome = "PENDING_REFERENCE"
	OutcomeReplay           Outcome = "REPLAY"
)

func (o Outcome) Valid() bool {
	switch o {
	case OutcomeProcessed, OutcomeRejected, OutcomePendingReference, OutcomeReplay:
		return true
	default:
		return false
	}
}

func (o Outcome) String() string { return string(o) }

var payloadHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type InboxMessage struct {
	consumerName  string
	messageID     string
	payloadHash   string
	transactionID uuid.UUID
	outcome       Outcome
	receivedAt    time.Time
	completedAt   time.Time
}

func NewInboxMessage(consumerName string, messageID string, payloadHash string, receivedAt time.Time) (InboxMessage, error) {
	if consumerName == "" || messageID == "" {
		return InboxMessage{}, ErrMissingField
	}
	if !payloadHashPattern.MatchString(payloadHash) {
		return InboxMessage{}, ErrMissingField
	}
	if receivedAt.IsZero() {
		return InboxMessage{}, ErrMissingTimestamp
	}
	return InboxMessage{
		consumerName: consumerName,
		messageID:    messageID,
		payloadHash:  payloadHash,
		receivedAt:   receivedAt.UTC(),
	}, nil
}

type InboxSnapshot struct {
	ConsumerName  string
	MessageID     string
	PayloadHash   string
	TransactionID uuid.UUID
	Outcome       Outcome
	ReceivedAt    time.Time
	CompletedAt   time.Time
}

func RehydrateInboxMessage(snapshot InboxSnapshot) (InboxMessage, error) {
	if snapshot.ConsumerName == "" || snapshot.MessageID == "" {
		return InboxMessage{}, ErrMissingField
	}
	if snapshot.Outcome != "" && !snapshot.Outcome.Valid() {
		return InboxMessage{}, ErrInvalidOutcome
	}
	return InboxMessage{
		consumerName:  snapshot.ConsumerName,
		messageID:     snapshot.MessageID,
		payloadHash:   snapshot.PayloadHash,
		transactionID: snapshot.TransactionID,
		outcome:       snapshot.Outcome,
		receivedAt:    snapshot.ReceivedAt,
		completedAt:   snapshot.CompletedAt,
	}, nil
}

func (m InboxMessage) ConsumerName() string     { return m.consumerName }
func (m InboxMessage) MessageID() string        { return m.messageID }
func (m InboxMessage) PayloadHash() string      { return m.payloadHash }
func (m InboxMessage) TransactionID() uuid.UUID { return m.transactionID }
func (m InboxMessage) Outcome() Outcome         { return m.outcome }
func (m InboxMessage) ReceivedAt() time.Time    { return m.receivedAt }
func (m InboxMessage) CompletedAt() time.Time   { return m.completedAt }
func (m InboxMessage) Completed() bool          { return m.outcome != "" }

// ErrPayloadMismatch means the same message id arrived carrying a different body.
// The identity of a message is its id, so two different bodies under one id is a
// broken contract from whoever published it, not something to retry.
var ErrPayloadMismatch = errors.New("messaging: message id was already handled with a different body")

func (m InboxMessage) SameContent(payloadHash string) bool {
	return m.payloadHash == payloadHash
}
