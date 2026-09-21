package usecase

import (
	"context"
	"log/slog"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/mapper"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

type ListWalletLedger interface {
	Execute(ctx context.Context, input dto.ListLedgerInput) (dto.LedgerPage, error)
}

type listWalletLedger struct {
	unitOfWork repositories.UnitOfWork
	logger     *slog.Logger
}

func NewListWalletLedger(unitOfWork repositories.UnitOfWork, logger *slog.Logger) ListWalletLedger {
	return &listWalletLedger{unitOfWork: unitOfWork, logger: logger}
}

func (u *listWalletLedger) Execute(ctx context.Context, input dto.ListLedgerInput) (dto.LedgerPage, error) {
	if err := input.Validate(); err != nil {
		return dto.LedgerPage{}, err
	}

	var page dto.LedgerPage
	err := u.unitOfWork.Read(ctx, func(ctx context.Context, registry repositories.Registry) error {
		found, err := registry.Wallets().Find(ctx, input.WalletID)
		if err != nil {
			return err
		}
		if found == nil {
			return wallet.ErrNotFound
		}

		entries, err := registry.Ledger().ListAfterVersion(ctx, input.WalletID, input.AfterVersion, input.Limit+1)
		if err != nil {
			return err
		}
		page = mapper.LedgerPage(entries, input.Limit)
		return nil
	})
	if err != nil {
		return dto.LedgerPage{}, err
	}
	return page, nil
}
