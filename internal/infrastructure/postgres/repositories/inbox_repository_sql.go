package repositories

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
)

type InboxRepository struct {
	queries Querier
}

func NewInboxRepository(queries Querier) *InboxRepository {
	return &InboxRepository{queries: queries}
}

func (r *InboxRepository) Insert(ctx context.Context, message messaging.InboxMessage) (bool, error) {
	const statement = `
		INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING
		RETURNING message_id`

	var inserted string
	err := r.queries.QueryRow(ctx, statement,
		message.ConsumerName(), message.MessageID(), message.PayloadHash(), message.ReceivedAt()).Scan(&inserted)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, Classify(err)
	}
	return true, nil
}

func (r *InboxRepository) Find(ctx context.Context, consumerName string, messageID string) (*messaging.InboxMessage, error) {
	const statement = `
		SELECT consumer_name, message_id, payload_hash, transaction_id, outcome, received_at, completed_at
		  FROM inbox_messages
		 WHERE consumer_name = $1 AND message_id = $2`

	var (
		snapshot      messaging.InboxSnapshot
		transactionID *uuid.UUID
		outcome       *string
		completedAt   *time.Time
	)

	err := r.queries.QueryRow(ctx, statement, consumerName, messageID).Scan(
		&snapshot.ConsumerName, &snapshot.MessageID, &snapshot.PayloadHash,
		&transactionID, &outcome, &snapshot.ReceivedAt, &completedAt)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, Classify(err)
	}

	if transactionID != nil {
		snapshot.TransactionID = *transactionID
	}
	snapshot.Outcome = messaging.Outcome(text(outcome))
	snapshot.CompletedAt = moment(completedAt)

	message, err := messaging.RehydrateInboxMessage(snapshot)
	if err != nil {
		return nil, err
	}
	return &message, nil
}

func (r *InboxRepository) Complete(
	ctx context.Context,
	consumerName string,
	messageID string,
	outcome messaging.Outcome,
	transactionID uuid.UUID,
	completedAt time.Time,
) error {
	const statement = `
		UPDATE inbox_messages
		   SET outcome = $3, transaction_id = $4, completed_at = $5
		 WHERE consumer_name = $1 AND message_id = $2`

	tag, err := r.queries.Exec(ctx, statement, consumerName, messageID,
		outcome.String(), nullUUID(transactionID), completedAt)
	if err != nil {
		return Classify(err)
	}
	if tag.RowsAffected() == 0 {
		return messaging.ErrMissingField
	}
	return nil
}
