package usecase_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
	"github.com/matheusj989/wagering-wallet-service/internal/mocks"
)

var clockReading = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

// harness wires every collaborator of the application layer as a mock, so a test
// describes only what it cares about and the unit of work runs the closure it was
// given against the same registry.
type harness struct {
	unitOfWork   *mocks.MockUnitOfWork
	registry     *mocks.MockRegistry
	wallets      *mocks.MockWallet
	ledger       *mocks.MockLedger
	transactions *mocks.MockWagerTransaction
	inbox        *mocks.MockInbox
	outbox       *mocks.MockOutbox

	clock      *mocks.MockClock
	ids        *mocks.MockIDGenerator
	notifier   *mocks.MockWalletEventNotifier
	failpoints *mocks.MockFailpoint

	wageringMetrics       *mocks.MockWageringMetrics
	referenceMetrics      *mocks.MockReferenceMetrics
	outboxMetrics         *mocks.MockOutboxMetrics
	reconciliationMetrics *mocks.MockReconciliationMetrics

	logger *slog.Logger
	now    time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	ctrl := gomock.NewController(t)
	h := &harness{
		unitOfWork:            mocks.NewMockUnitOfWork(ctrl),
		registry:              mocks.NewMockRegistry(ctrl),
		wallets:               mocks.NewMockWallet(ctrl),
		ledger:                mocks.NewMockLedger(ctrl),
		transactions:          mocks.NewMockWagerTransaction(ctrl),
		inbox:                 mocks.NewMockInbox(ctrl),
		outbox:                mocks.NewMockOutbox(ctrl),
		clock:                 mocks.NewMockClock(ctrl),
		ids:                   mocks.NewMockIDGenerator(ctrl),
		notifier:              mocks.NewMockWalletEventNotifier(ctrl),
		failpoints:            mocks.NewMockFailpoint(ctrl),
		wageringMetrics:       mocks.NewMockWageringMetrics(ctrl),
		referenceMetrics:      mocks.NewMockReferenceMetrics(ctrl),
		outboxMetrics:         mocks.NewMockOutboxMetrics(ctrl),
		reconciliationMetrics: mocks.NewMockReconciliationMetrics(ctrl),
		logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:                   clockReading,
	}

	h.registry.EXPECT().Wallets().Return(h.wallets).AnyTimes()
	h.registry.EXPECT().Ledger().Return(h.ledger).AnyTimes()
	h.registry.EXPECT().Transactions().Return(h.transactions).AnyTimes()
	h.registry.EXPECT().Inbox().Return(h.inbox).AnyTimes()
	h.registry.EXPECT().Outbox().Return(h.outbox).AnyTimes()

	h.clock.EXPECT().Now().DoAndReturn(func() time.Time { return h.now }).AnyTimes()
	h.ids.EXPECT().New().DoAndReturn(func() uuid.UUID { return uuid.Must(uuid.NewV7()) }).AnyTimes()
	h.failpoints.EXPECT().Hit(gomock.Any()).AnyTimes()

	return h
}

func (h *harness) writes(times int) {
	h.unitOfWork.EXPECT().Do(gomock.Any(), gomock.Any()).Times(times).DoAndReturn(h.runInRegistry)
}

func (h *harness) reads(times int) {
	h.unitOfWork.EXPECT().Read(gomock.Any(), gomock.Any()).Times(times).DoAndReturn(h.runInRegistry)
}

func (h *harness) runInRegistry(ctx context.Context, fn func(context.Context, repositories.Registry) error) error {
	return fn(ctx, h.registry)
}

func (h *harness) allowWageringMetrics() {
	h.wageringMetrics.EXPECT().TransactionSettled(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	h.wageringMetrics.EXPECT().IdempotentReplay(gomock.Any()).AnyTimes()
	h.wageringMetrics.EXPECT().IdempotencyConflict(gomock.Any(), gomock.Any()).AnyTimes()
	h.wageringMetrics.EXPECT().ProcessingObserved(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	h.wageringMetrics.EXPECT().ConcurrencyConflict(gomock.Any()).AnyTimes()
}

func (h *harness) allowReferenceMetrics() {
	h.referenceMetrics.EXPECT().ReferenceRetried().AnyTimes()
	h.referenceMetrics.EXPECT().ReferenceFailed().AnyTimes()
	h.referenceMetrics.EXPECT().ReferencePending(gomock.Any()).AnyTimes()
}

func (h *harness) expectEvents(times int) {
	h.outbox.EXPECT().Append(gomock.Any(), gomock.Any()).Times(times).Return(nil)
}

func brl(t *testing.T, amount string) money.Money {
	t.Helper()

	value, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", amount, err)
	}
	return value
}

func openWalletWith(t *testing.T, balance money.Money, version int64) *wallet.Wallet {
	t.Helper()

	account, err := wallet.Rehydrate(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), balance, version, clockReading, clockReading)
	if err != nil {
		t.Fatalf("the wallet could not be rehydrated: %v", err)
	}
	return account
}

func transactionWith(t *testing.T, mutate func(*wagering.Snapshot)) *wagering.Transaction {
	t.Helper()

	snapshot := wagering.Snapshot{
		ID:                    uuid.Must(uuid.NewV7()),
		Origin:                wagering.HTTP,
		Kind:                  wagering.Bet,
		Status:                wagering.Processed,
		WalletID:              uuid.Must(uuid.NewV7()),
		PlayerID:              uuid.Must(uuid.NewV7()),
		Amount:                brl(t, "25.00"),
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-1",
		IdempotencyKey:        "provider-a:transaction-1",
		PayloadHash:           "0000000000000000000000000000000000000000000000000000000000000000",
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		ResultBalance:         brl(t, "975.00"),
		CorrelationID:         "correlation-1",
		CreatedAt:             clockReading,
		UpdatedAt:             clockReading,
		CompletedAt:           clockReading,
	}
	if mutate != nil {
		mutate(&snapshot)
	}

	operation, err := wagering.Rehydrate(snapshot)
	if err != nil {
		t.Fatalf("the transaction could not be rehydrated: %v", err)
	}
	return operation
}
