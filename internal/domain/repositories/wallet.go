package repositories

import (
	"context"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

type Wallet interface {
	Create(ctx context.Context, opened *wallet.Wallet) error
	Find(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	FindForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	FindByPlayer(ctx context.Context, playerID uuid.UUID, currency money.Currency) (*wallet.Wallet, error)
	Update(ctx context.Context, updated *wallet.Wallet, expectedVersion int64) error
}
