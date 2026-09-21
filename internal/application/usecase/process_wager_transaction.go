package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/idempotency"
	"github.com/matheusj989/wagering-wallet-service/internal/application/mapper"
	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

const ConsumerName = "wager-transactions-consumer"

type ProcessSettings struct {
	ReferenceBackoff time.Duration
	ReferenceTTL     time.Duration
}

type ProcessWagerTransaction interface {
	Execute(ctx context.Context, input dto.ProcessWagerInput) (dto.ProcessWagerResult, error)
}

type processWagerTransaction struct {
	unitOfWork repositories.UnitOfWork
	clock      port.Clock
	ids        port.IDGenerator
	metrics    port.WageringMetrics
	logger     *slog.Logger
	settings   ProcessSettings
}

func NewProcessWagerTransaction(
	unitOfWork repositories.UnitOfWork,
	clock port.Clock,
	ids port.IDGenerator,
	metrics port.WageringMetrics,
	logger *slog.Logger,
	settings ProcessSettings,
) ProcessWagerTransaction {
	return &processWagerTransaction{
		unitOfWork: unitOfWork,
		clock:      clock,
		ids:        ids,
		metrics:    metrics,
		logger:     logger,
		settings:   settings,
	}
}

func (u *processWagerTransaction) Execute(ctx context.Context, input dto.ProcessWagerInput) (dto.ProcessWagerResult, error) {
	if err := input.Validate(); err != nil {
		u.logger.WarnContext(ctx, "wager transaction refused before any persistence",
			slog.String(logCorrelationID, input.CorrelationID),
			slog.String(logProviderID, input.ProviderID),
			slog.String(logCause, err.Error()),
		)
		return dto.ProcessWagerResult{}, err
	}

	u.logger.InfoContext(ctx, "processing a wager transaction",
		slog.String(logCorrelationID, input.CorrelationID),
		slog.String(logProviderID, input.ProviderID),
		slog.String(logWalletID, input.WalletID.String()),
		slog.String(logKind, input.Kind.String()),
		slog.String("origin", input.Origin.String()),
		slog.String("externalTransactionId", input.ExternalTransactionID),
	)

	startedAt := u.clock.Now()
	payloadHash, err := idempotency.Hash(mapper.IdempotencyPayload(input))
	if err != nil {
		return dto.ProcessWagerResult{}, err
	}

	var result dto.ProcessWagerResult
	err = u.unitOfWork.Do(ctx, func(ctx context.Context, registry repositories.Registry) error {
		var runErr error
		result, runErr = u.run(ctx, registry, input, payloadHash)
		return runErr
	})
	if err != nil {
		u.reportFailure(ctx, input, err)
		return dto.ProcessWagerResult{}, err
	}

	u.metrics.ProcessingObserved(input.Origin.String(), input.Kind.String(), u.clock.Now().Sub(startedAt))
	if result.IdempotentReplay {
		u.metrics.IdempotentReplay(input.Origin.String())
	} else {
		u.metrics.TransactionSettled(input.Kind.String(), result.Status.String(), input.Origin.String())
	}

	u.logger.InfoContext(ctx, "wager transaction settled",
		slog.String(logCorrelationID, input.CorrelationID),
		slog.String(logProviderID, input.ProviderID),
		slog.String(logWalletID, input.WalletID.String()),
		slog.String(logTransactionID, result.TransactionID.String()),
		slog.String(logKind, input.Kind.String()),
		slog.String(logStatus, result.Status.String()),
		slog.Bool("idempotentReplay", result.IdempotentReplay),
		slog.String("failureCode", result.FailureCode.String()),
	)
	return result, nil
}

func (u *processWagerTransaction) run(
	ctx context.Context,
	registry repositories.Registry,
	input dto.ProcessWagerInput,
	payloadHash string,
) (dto.ProcessWagerResult, error) {
	now := u.clock.Now()

	if input.FromQueue() {
		replay, handled, err := u.registerInbox(ctx, registry, input)
		if err != nil {
			return dto.ProcessWagerResult{}, err
		}
		if handled {
			return replay, nil
		}
	}

	operation, err := wagering.NewExternal(mapper.WagerCommand(input, payloadHash), u.ids.New(), now)
	if err != nil {
		return dto.ProcessWagerResult{}, err
	}

	inserted, err := registry.Transactions().Insert(ctx, operation)
	if err != nil {
		return dto.ProcessWagerResult{}, err
	}
	if !inserted {
		return u.replay(ctx, registry, input, payloadHash, now)
	}

	account, err := registry.Wallets().FindForUpdate(ctx, input.WalletID)
	if err != nil {
		return dto.ProcessWagerResult{}, err
	}

	reference, err := u.resolveReference(ctx, registry, operation)
	if err != nil {
		return dto.ProcessWagerResult{}, err
	}

	decision, err := wagering.Decide(account, operation, reference, now)
	if err != nil {
		return dto.ProcessWagerResult{}, err
	}
	u.logger.DebugContext(ctx, "decision taken for a wager transaction",
		slog.String(logCorrelationID, input.CorrelationID),
		slog.String(logTransactionID, operation.ID().String()),
		slog.String(logKind, input.Kind.String()),
		slog.String("failureCode", decision.FailureCode.String()),
		slog.Bool("movesBalance", decision.Movement.Present()),
	)

	entry, err := u.settle(ctx, registry, account, operation, decision, now)
	if err != nil {
		return dto.ProcessWagerResult{}, err
	}

	if err := registry.Transactions().Update(ctx, operation); err != nil {
		return dto.ProcessWagerResult{}, err
	}
	if err := appendSettlementEvents(ctx, registry, u.ids, now, operation, entry, account.Balance()); err != nil {
		return dto.ProcessWagerResult{}, err
	}
	if operation.Terminal() {
		if err := u.wakeUpWaiting(ctx, registry, operation, now); err != nil {
			return dto.ProcessWagerResult{}, err
		}
	}

	result := mapper.WagerResult(operation, false)
	if input.FromQueue() {
		if err := u.completeInbox(ctx, registry, input.MessageID, result, now); err != nil {
			return dto.ProcessWagerResult{}, err
		}
	}
	return result, nil
}

func (u *processWagerTransaction) wakeUpWaiting(
	ctx context.Context,
	registry repositories.Registry,
	operation *wagering.Transaction,
	now time.Time,
) error {
	awakened, err := registry.Transactions().WakeUpWaitingFor(ctx, operation.ProviderID(), operation.ExternalTransactionID(), now)
	if err != nil {
		return err
	}
	if awakened > 0 {
		u.logger.InfoContext(ctx, "pending operations woken up by this settlement",
			slog.String(logCorrelationID, operation.CorrelationID()),
			slog.String(logTransactionID, operation.ID().String()),
			slog.Int64("awakened", awakened),
		)
	}
	return nil
}

func (u *processWagerTransaction) registerInbox(
	ctx context.Context,
	registry repositories.Registry,
	input dto.ProcessWagerInput,
) (dto.ProcessWagerResult, bool, error) {
	now := u.clock.Now()
	message, err := messaging.NewInboxMessage(ConsumerName, input.MessageID, input.RawBodyHash, now)
	if err != nil {
		return dto.ProcessWagerResult{}, false, err
	}

	inserted, err := registry.Inbox().Insert(ctx, message)
	if err != nil {
		return dto.ProcessWagerResult{}, false, err
	}
	if inserted {
		return dto.ProcessWagerResult{}, false, nil
	}

	existing, err := registry.Inbox().Find(ctx, ConsumerName, input.MessageID)
	if err != nil {
		return dto.ProcessWagerResult{}, false, err
	}
	if existing == nil {
		return dto.ProcessWagerResult{}, false, repositories.ErrTransient
	}
	if !existing.SameContent(input.RawBodyHash) {
		return dto.ProcessWagerResult{}, false, messaging.ErrPayloadMismatch
	}
	if !existing.Completed() {
		return dto.ProcessWagerResult{}, false, repositories.ErrTransient
	}

	settled, err := registry.Transactions().FindByID(ctx, existing.TransactionID())
	if err != nil {
		return dto.ProcessWagerResult{}, false, err
	}
	if settled == nil {
		return dto.ProcessWagerResult{}, false, repositories.ErrTransient
	}

	u.logger.InfoContext(ctx, "message already handled, answering with the stored outcome",
		slog.String(logCorrelationID, input.CorrelationID),
		slog.String(logMessageID, input.MessageID),
		slog.String(logTransactionID, settled.ID().String()),
		slog.String("outcome", existing.Outcome().String()),
	)

	result := mapper.WagerResult(settled, true)
	result.InboxOutcome = existing.Outcome()
	return result, true, nil
}

func (u *processWagerTransaction) completeInbox(
	ctx context.Context,
	registry repositories.Registry,
	messageID string,
	result dto.ProcessWagerResult,
	now time.Time,
) error {
	outcome := messaging.OutcomeProcessed
	switch {
	case result.IdempotentReplay:
		outcome = messaging.OutcomeReplay
	case result.Status == wagering.Rejected:
		outcome = messaging.OutcomeRejected
	case result.Status == wagering.PendingReference:
		outcome = messaging.OutcomePendingReference
	}
	return registry.Inbox().Complete(ctx, ConsumerName, messageID, outcome, result.TransactionID, now)
}

func (u *processWagerTransaction) replay(
	ctx context.Context,
	registry repositories.Registry,
	input dto.ProcessWagerInput,
	payloadHash string,
	now time.Time,
) (dto.ProcessWagerResult, error) {
	existing, err := registry.Transactions().FindByIdempotencyKey(ctx, input.ProviderID, input.IdempotencyKey)
	if err != nil {
		return dto.ProcessWagerResult{}, err
	}
	if existing != nil {
		if existing.PayloadHash() != payloadHash {
			return dto.ProcessWagerResult{}, &ConflictError{Kind: ConflictIdempotencyKey, TransactionID: existing.ID()}
		}

		u.logger.InfoContext(ctx, "idempotent replay answered with the stored outcome",
			slog.String(logCorrelationID, input.CorrelationID),
			slog.String(logProviderID, input.ProviderID),
			slog.String(logTransactionID, existing.ID().String()),
			slog.String(logStatus, existing.Status().String()),
		)

		result := mapper.WagerResult(existing, true)
		if input.FromQueue() {
			if err := u.completeInbox(ctx, registry, input.MessageID, result, now); err != nil {
				return dto.ProcessWagerResult{}, err
			}
		}
		return result, nil
	}

	reused, err := registry.Transactions().FindByExternalID(ctx, input.ProviderID, input.ExternalTransactionID)
	if err != nil {
		return dto.ProcessWagerResult{}, err
	}
	if reused != nil {
		return dto.ProcessWagerResult{}, &ConflictError{Kind: ConflictExternalID, TransactionID: reused.ID()}
	}
	return dto.ProcessWagerResult{}, repositories.ErrTransient
}

func (u *processWagerTransaction) resolveReference(
	ctx context.Context,
	registry repositories.Registry,
	operation *wagering.Transaction,
) (*wagering.Reference, error) {
	if operation.ReferenceExternalID() == "" {
		return nil, nil
	}

	original, err := registry.Transactions().FindByExternalID(ctx, operation.ProviderID(), operation.ReferenceExternalID())
	if err != nil {
		return nil, err
	}
	if original == nil {
		u.logger.DebugContext(ctx, "the referenced operation has not arrived yet",
			slog.String(logCorrelationID, operation.CorrelationID()),
			slog.String(logTransactionID, operation.ID().String()),
			slog.String("referenceExternalTransactionId", operation.ReferenceExternalID()),
		)
		return nil, nil
	}

	reference := &wagering.Reference{Transaction: original}
	if operation.Kind().Reversal() {
		reversed, err := registry.Transactions().HasSuccessfulReversal(ctx, original.ID())
		if err != nil {
			return nil, err
		}
		reference.AlreadyReversed = reversed
	}
	return reference, nil
}

func (u *processWagerTransaction) settle(
	ctx context.Context,
	registry repositories.Registry,
	account *wallet.Wallet,
	operation *wagering.Transaction,
	decision wagering.Decision,
	now time.Time,
) (*wallet.LedgerEntry, error) {
	switch decision.Outcome {
	case wagering.OutcomeReject:
		return nil, operation.MarkRejected(decision.FailureCode, account.Balance(), now)

	case wagering.OutcomeWaitReference:
		deadline := operation.CreatedAt().Add(u.settings.ReferenceTTL)
		nextAttempt := now.Add(u.settings.ReferenceBackoff)
		u.logger.InfoContext(ctx, "operation parked until its reference arrives",
			slog.String(logCorrelationID, operation.CorrelationID()),
			slog.String(logTransactionID, operation.ID().String()),
			slog.String("referenceExternalTransactionId", operation.ReferenceExternalID()),
			slog.Time("nextAttemptAt", nextAttempt),
			slog.Time("deadlineAt", deadline),
		)
		return nil, operation.MarkPendingReference(nextAttempt, deadline, now)

	case wagering.OutcomeProcess:
		if !decision.Movement.Present() {
			return nil, operation.MarkProcessed(account.Balance(), decision.ReferenceID, now)
		}
		return u.applyMovement(ctx, registry, account, operation, decision, now)

	default:
		return nil, fmt.Errorf("wagering: unexpected decision outcome %d", decision.Outcome)
	}
}

func (u *processWagerTransaction) applyMovement(
	ctx context.Context,
	registry repositories.Registry,
	account *wallet.Wallet,
	operation *wagering.Transaction,
	decision wagering.Decision,
	now time.Time,
) (*wallet.LedgerEntry, error) {
	expectedVersion := account.Version()
	entry, err := applyFinancialMovement(ctx, registry, u.ids, account, operation, decision, now)
	if err != nil {
		if errors.Is(err, wallet.ErrVersionChanged) {
			u.metrics.ConcurrencyConflict("version")
			u.logger.WarnContext(ctx, "wallet changed while this operation was being settled",
				slog.String(logCorrelationID, operation.CorrelationID()),
				slog.String(logWalletID, account.ID().String()),
				slog.String(logTransactionID, operation.ID().String()),
				slog.Int64("expectedVersion", expectedVersion))
		}
		return nil, err
	}

	u.logger.DebugContext(ctx, "wallet balance moved",
		slog.String(logCorrelationID, operation.CorrelationID()),
		slog.String(logWalletID, account.ID().String()),
		slog.String(logTransactionID, operation.ID().String()),
		slog.String("direction", decision.Movement.Direction.String()),
		slog.Int64("walletVersion", account.Version()),
	)
	return entry, nil
}

func (u *processWagerTransaction) reportFailure(ctx context.Context, input dto.ProcessWagerInput, err error) {
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		u.metrics.IdempotencyConflict(input.Origin.String(), string(conflict.Kind))
		u.logger.WarnContext(ctx, "wager transaction refused by idempotency",
			slog.String(logCorrelationID, input.CorrelationID),
			slog.String(logProviderID, input.ProviderID),
			slog.String(logTransactionID, conflict.TransactionID.String()),
			slog.String("conflict", string(conflict.Kind)),
		)
		return
	}
	if errors.Is(err, repositories.ErrLockTimeout) {
		u.metrics.ConcurrencyConflict("lock_timeout")
	}

	u.logger.ErrorContext(ctx, "wager transaction could not be settled",
		slog.String(logCorrelationID, input.CorrelationID),
		slog.String(logProviderID, input.ProviderID),
		slog.String(logWalletID, input.WalletID.String()),
		slog.String(logKind, input.Kind.String()),
		slog.String(logCause, err.Error()),
	)
}
