package repositories

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

type WagerTransaction interface {
	Insert(ctx context.Context, operation *wagering.Transaction) (bool, error)
	FindByID(ctx context.Context, id uuid.UUID) (*wagering.Transaction, error)
	FindByIDForUpdate(ctx context.Context, id uuid.UUID) (*wagering.Transaction, error)
	CountPending(ctx context.Context) (int64, error)
	FindByIdempotencyKey(ctx context.Context, providerID string, key string) (*wagering.Transaction, error)
	FindByExternalID(ctx context.Context, providerID string, externalTransactionID string) (*wagering.Transaction, error)
	Update(ctx context.Context, operation *wagering.Transaction) error
	ClaimDuePending(ctx context.Context, now time.Time, limit int) ([]*wagering.Transaction, error)
	WakeUpWaitingFor(ctx context.Context, providerID string, externalTransactionID string, now time.Time) (int64, error)
	HasSuccessfulReversal(ctx context.Context, referenceTransactionID uuid.UUID) (bool, error)
}
