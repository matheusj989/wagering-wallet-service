package usecase

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

type FindWagerTransaction interface {
	ByID(ctx context.Context, transactionID uuid.UUID) (*wagering.Transaction, error)
	ByExternalID(ctx context.Context, providerID string, externalTransactionID string) (*wagering.Transaction, error)
}

type findWagerTransaction struct {
	unitOfWork repositories.UnitOfWork
	logger     *slog.Logger
}

func NewFindWagerTransaction(unitOfWork repositories.UnitOfWork, logger *slog.Logger) FindWagerTransaction {
	return &findWagerTransaction{unitOfWork: unitOfWork, logger: logger}
}

func (u *findWagerTransaction) ByID(ctx context.Context, transactionID uuid.UUID) (*wagering.Transaction, error) {
	return u.read(ctx, func(ctx context.Context, registry repositories.Registry) (*wagering.Transaction, error) {
		return registry.Transactions().FindByID(ctx, transactionID)
	})
}

func (u *findWagerTransaction) ByExternalID(ctx context.Context, providerID string, externalTransactionID string) (*wagering.Transaction, error) {
	return u.read(ctx, func(ctx context.Context, registry repositories.Registry) (*wagering.Transaction, error) {
		return registry.Transactions().FindByExternalID(ctx, providerID, externalTransactionID)
	})
}

func (u *findWagerTransaction) read(
	ctx context.Context,
	query func(context.Context, repositories.Registry) (*wagering.Transaction, error),
) (*wagering.Transaction, error) {
	var found *wagering.Transaction
	err := u.unitOfWork.Read(ctx, func(ctx context.Context, registry repositories.Registry) error {
		result, err := query(ctx, registry)
		found = result
		return err
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, wagering.ErrNotFound
	}
	return found, nil
}
