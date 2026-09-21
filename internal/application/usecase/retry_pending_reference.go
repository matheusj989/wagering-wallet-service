package usecase

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

var errNoPendingWork = errors.New("wagering: no pending reference is due")

type ReferenceSettings struct {
	BackoffBase time.Duration
	BackoffMax  time.Duration
	BatchSize   int
}

type RetryPendingReference interface {
	Execute(ctx context.Context) (int, error)
	ReportPending(ctx context.Context) error
}

type retryPendingReference struct {
	unitOfWork repositories.UnitOfWork
	clock      port.Clock
	ids        port.IDGenerator
	metrics    port.ReferenceMetrics
	logger     *slog.Logger
	settings   ReferenceSettings
}

func NewRetryPendingReference(
	unitOfWork repositories.UnitOfWork,
	clock port.Clock,
	ids port.IDGenerator,
	metrics port.ReferenceMetrics,
	logger *slog.Logger,
	settings ReferenceSettings,
) RetryPendingReference {
	return &retryPendingReference{
		unitOfWork: unitOfWork,
		clock:      clock,
		ids:        ids,
		metrics:    metrics,
		logger:     logger,
		settings:   settings,
	}
}

func (u *retryPendingReference) Execute(ctx context.Context) (int, error) {
	handled := 0
	for handled < u.settings.BatchSize {
		if ctx.Err() != nil {
			return handled, ctx.Err()
		}

		claimed, err := u.attempt(ctx)
		if errors.Is(err, errNoPendingWork) {
			return handled, nil
		}
		if err != nil {
			return handled, err
		}
		if claimed != uuid.Nil {
			handled++
		}
	}
	return handled, nil
}

func (u *retryPendingReference) attempt(ctx context.Context) (uuid.UUID, error) {
	now := u.clock.Now()
	var claimedID uuid.UUID

	err := u.unitOfWork.Do(ctx, func(ctx context.Context, registry repositories.Registry) error {
		claimedID = uuid.Nil
		pending, err := registry.Transactions().ClaimDuePending(ctx, now, 1)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return errNoPendingWork
		}

		operation := pending[0]
		claimedID = operation.ID()

		u.logger.DebugContext(ctx, "retrying a pending reference",
			slog.String(logCorrelationID, operation.CorrelationID()),
			slog.String(logTransactionID, operation.ID().String()),
			slog.String("referenceExternalTransactionId", operation.ReferenceExternalID()),
			slog.Int(logAttempts, operation.ReferenceAttempts()),
		)

		account, err := registry.Wallets().FindForUpdate(ctx, operation.WalletID())
		if err != nil {
			return err
		}

		reference, err := u.resolveReference(ctx, registry, operation)
		if err != nil {
			return err
		}

		decision, err := wagering.Decide(account, operation, reference, now)
		if err != nil {
			return err
		}

		if decision.Outcome == wagering.OutcomeWaitReference {
			return u.reschedule(ctx, registry, operation, now)
		}
		return u.settle(ctx, registry, account, operation, decision, now)
	})

	if err == nil || errors.Is(err, errNoPendingWork) {
		return claimedID, err
	}
	if recoverable(err) || claimedID == uuid.Nil {
		return uuid.Nil, err
	}
	return claimedID, u.giveUp(ctx, claimedID, err)
}

func (u *retryPendingReference) reschedule(
	ctx context.Context,
	registry repositories.Registry,
	operation *wagering.Transaction,
	now time.Time,
) error {
	delay := backoff(u.settings.BackoffBase, u.settings.BackoffMax, operation.ReferenceAttempts()+1)
	nextAttempt := now.Add(delay)
	if err := operation.Reschedule(nextAttempt, now); err != nil {
		return err
	}
	u.metrics.ReferenceRetried()

	u.logger.DebugContext(ctx, "the reference is still missing, the operation stays pending",
		slog.String(logCorrelationID, operation.CorrelationID()),
		slog.String(logTransactionID, operation.ID().String()),
		slog.Int(logAttempts, operation.ReferenceAttempts()),
		slog.Time("nextAttemptAt", nextAttempt),
	)
	return registry.Transactions().Update(ctx, operation)
}

func (u *retryPendingReference) settle(
	ctx context.Context,
	registry repositories.Registry,
	account *wallet.Wallet,
	operation *wagering.Transaction,
	decision wagering.Decision,
	now time.Time,
) error {
	entry, err := u.apply(ctx, registry, account, operation, decision, now)
	if err != nil {
		return err
	}
	if err := registry.Transactions().Update(ctx, operation); err != nil {
		return err
	}
	if err := appendSettlementEvents(ctx, registry, u.ids, now, operation, entry, account.Balance()); err != nil {
		return err
	}
	if _, err := registry.Transactions().WakeUpWaitingFor(ctx, operation.ProviderID(), operation.ExternalTransactionID(), now); err != nil {
		return err
	}

	u.logger.InfoContext(ctx, "pending reference settled",
		slog.String(logCorrelationID, operation.CorrelationID()),
		slog.String(logTransactionID, operation.ID().String()),
		slog.String(logWalletID, operation.WalletID().String()),
		slog.String(logKind, operation.Kind().String()),
		slog.String(logStatus, operation.Status().String()),
		slog.Int(logAttempts, operation.ReferenceAttempts()),
	)
	return nil
}

func (u *retryPendingReference) apply(
	ctx context.Context,
	registry repositories.Registry,
	account *wallet.Wallet,
	operation *wagering.Transaction,
	decision wagering.Decision,
	now time.Time,
) (*wallet.LedgerEntry, error) {
	if decision.Outcome == wagering.OutcomeReject {
		return nil, operation.MarkRejected(decision.FailureCode, account.Balance(), now)
	}
	if !decision.Movement.Present() {
		return nil, operation.MarkProcessed(account.Balance(), decision.ReferenceID, now)
	}

	return applyFinancialMovement(ctx, registry, u.ids, account, operation, decision, now)
}

func (u *retryPendingReference) giveUp(ctx context.Context, transactionID uuid.UUID, cause error) error {
	now := u.clock.Now()
	failed := false
	err := u.unitOfWork.Do(ctx, func(ctx context.Context, registry repositories.Registry) error {
		failed = false
		operation, err := registry.Transactions().FindByIDForUpdate(ctx, transactionID)
		if err != nil {
			return err
		}
		if operation == nil || operation.Status() != wagering.PendingReference {
			return nil
		}
		if err := operation.MarkFailed(now); err != nil {
			return err
		}
		if err := registry.Transactions().Update(ctx, operation); err != nil {
			return err
		}
		failed = true
		return nil
	})
	if err != nil || !failed {
		return err
	}

	u.metrics.ReferenceFailed()
	u.logger.ErrorContext(ctx, "pending reference reached a permanent failure",
		slog.String(logTransactionID, transactionID.String()),
		slog.String(logCause, cause.Error()),
	)
	return nil
}

func (u *retryPendingReference) ReportPending(ctx context.Context) error {
	var count int64
	err := u.unitOfWork.Read(ctx, func(ctx context.Context, registry repositories.Registry) error {
		var err error
		count, err = registry.Transactions().CountPending(ctx)
		return err
	})
	if err == nil {
		u.metrics.ReferencePending(count)
	}
	return err
}

func (u *retryPendingReference) resolveReference(
	ctx context.Context,
	registry repositories.Registry,
	operation *wagering.Transaction,
) (*wagering.Reference, error) {
	original, err := registry.Transactions().FindByExternalID(ctx, operation.ProviderID(), operation.ReferenceExternalID())
	if err != nil {
		return nil, err
	}
	if original == nil {
		return nil, nil
	}

	reversed, err := registry.Transactions().HasSuccessfulReversal(ctx, original.ID())
	if err != nil {
		return nil, err
	}
	return &wagering.Reference{Transaction: original, AlreadyReversed: reversed}, nil
}
