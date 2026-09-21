package repositories

import (
	"context"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

type Ledger interface {
	Append(ctx context.Context, entry wallet.LedgerEntry) error
	ListAfterVersion(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) ([]wallet.LedgerEntry, error)
	Totals(ctx context.Context, walletID uuid.UUID, currency money.Currency) (wallet.LedgerTotals, error)
}
