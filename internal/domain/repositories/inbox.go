package repositories

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
)

type Inbox interface {
	Insert(ctx context.Context, message messaging.InboxMessage) (bool, error)
	Find(ctx context.Context, consumerName string, messageID string) (*messaging.InboxMessage, error)
	Complete(ctx context.Context, consumerName string, messageID string, outcome messaging.Outcome, transactionID uuid.UUID, completedAt time.Time) error
}
