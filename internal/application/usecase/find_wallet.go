package usecase

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

type FindWallet interface {
	Execute(ctx context.Context, walletID uuid.UUID) (*wallet.Wallet, error)
}

type findWallet struct {
	unitOfWork repositories.UnitOfWork
	logger     *slog.Logger
}

func NewFindWallet(unitOfWork repositories.UnitOfWork, logger *slog.Logger) FindWallet {
	return &findWallet{unitOfWork: unitOfWork, logger: logger}
}

func (u *findWallet) Execute(ctx context.Context, walletID uuid.UUID) (*wallet.Wallet, error) {
	var found *wallet.Wallet
	err := u.unitOfWork.Read(ctx, func(ctx context.Context, registry repositories.Registry) error {
		result, err := registry.Wallets().Find(ctx, walletID)
		found = result
		return err
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, wallet.ErrNotFound
	}
	return found, nil
}
