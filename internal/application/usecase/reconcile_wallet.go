package usecase

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/mapper"
	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

type ReconcileWallet interface {
	Execute(ctx context.Context, walletID uuid.UUID) (dto.Reconciliation, error)
}

type reconcileWallet struct {
	unitOfWork repositories.UnitOfWork
	metrics    port.ReconciliationMetrics
	logger     *slog.Logger
}

func NewReconcileWallet(
	unitOfWork repositories.UnitOfWork,
	metrics port.ReconciliationMetrics,
	logger *slog.Logger,
) ReconcileWallet {
	return &reconcileWallet{unitOfWork: unitOfWork, metrics: metrics, logger: logger}
}

func (u *reconcileWallet) Execute(ctx context.Context, walletID uuid.UUID) (dto.Reconciliation, error) {
	var report dto.Reconciliation
	err := u.unitOfWork.Read(ctx, func(ctx context.Context, registry repositories.Registry) error {
		found, err := registry.Wallets().Find(ctx, walletID)
		if err != nil {
			return err
		}
		if found == nil {
			return wallet.ErrNotFound
		}

		totals, err := registry.Ledger().Totals(ctx, walletID, found.Currency())
		if err != nil {
			return err
		}
		report, err = mapper.Reconciliation(found, totals)
		return err
	})
	if err != nil {
		return dto.Reconciliation{}, err
	}

	if !report.Consistent {
		u.metrics.ReconciliationDivergence()
		u.logger.ErrorContext(ctx, "wallet balance diverges from its ledger",
			slog.String(logWalletID, walletID.String()),
			slog.Int64("checkedEntries", report.CheckedEntries),
		)
		return report, nil
	}

	u.logger.DebugContext(ctx, "wallet balance matches its ledger",
		slog.String(logWalletID, walletID.String()),
		slog.Int64("checkedEntries", report.CheckedEntries),
	)
	return report, nil
}
