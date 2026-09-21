package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

var retrySettings = usecase.ReferenceSettings{
	BackoffBase: time.Second,
	BackoffMax:  time.Minute,
	BatchSize:   1,
}

func retryUseCase(h *harness) usecase.RetryPendingReference {
	return usecase.NewRetryPendingReference(h.unitOfWork, h.clock, h.ids, h.referenceMetrics, h.logger, retrySettings)
}

func pendingRollback(t *testing.T, account *wallet.Wallet, attempts int) *wagering.Transaction {
	t.Helper()

	return transactionWith(t, func(s *wagering.Snapshot) {
		s.Kind = wagering.Rollback
		s.Status = wagering.PendingReference
		s.Origin = wagering.Queue
		s.WalletID = account.ID()
		s.PlayerID = account.PlayerID()
		s.ExternalTransactionID = "rollback-1"
		s.IdempotencyKey = "provider-a:rollback-1"
		s.ReferenceExternalID = "transaction-0"
		s.ReferenceAttempts = attempts
		s.NextAttemptAt = clockReading
		s.ReferenceDeadlineAt = clockReading.Add(10 * time.Minute)
		s.ResultBalance = money.Money{}
		s.CompletedAt = time.Time{}
	})
}

func settledBet(t *testing.T, account *wallet.Wallet) *wagering.Transaction {
	t.Helper()

	return transactionWith(t, func(s *wagering.Snapshot) {
		s.WalletID = account.ID()
		s.PlayerID = account.PlayerID()
		s.ExternalTransactionID = "transaction-0"
		s.IdempotencyKey = "provider-a:transaction-0"
	})
}

func TestRetryPendingReference(t *testing.T) {
	t.Run("Given no pending operation is due/When the worker runs/Then nothing is claimed", func(t *testing.T) {
		// Given
		h := newHarness(t)
		h.writes(1)
		h.transactions.EXPECT().ClaimDuePending(gomock.Any(), clockReading, 1).Return(nil, nil)

		// When
		handled, err := retryUseCase(h).Execute(context.Background())

		// Then
		if err != nil {
			t.Fatalf("an empty round is not a failure, got %v", err)
		}
		if handled != 0 {
			t.Errorf("handled = %d, want 0", handled)
		}
	})

	t.Run("Given the reference finally arrived/When the worker runs/Then the operation is settled and waiters are woken", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		pending := pendingRollback(t, account, 1)
		original := settledBet(t, account)

		var entry wallet.LedgerEntry
		h.writes(1)
		h.transactions.EXPECT().ClaimDuePending(gomock.Any(), clockReading, 1).Return([]*wagering.Transaction{pending}, nil)
		h.wallets.EXPECT().FindForUpdate(gomock.Any(), account.ID()).Return(account, nil)
		h.transactions.EXPECT().FindByExternalID(gomock.Any(), "provider-a", "transaction-0").Return(original, nil)
		h.transactions.EXPECT().HasSuccessfulReversal(gomock.Any(), original.ID()).Return(false, nil)
		h.ledger.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, appended wallet.LedgerEntry) error {
				entry = appended
				return nil
			})
		h.wallets.EXPECT().Update(gomock.Any(), account, int64(4)).Return(nil)
		h.transactions.EXPECT().Update(gomock.Any(), pending).Return(nil)
		h.expectEvents(2)
		h.transactions.EXPECT().WakeUpWaitingFor(gomock.Any(), "provider-a", "rollback-1", clockReading).Return(int64(0), nil)
		h.allowReferenceMetrics()

		// When
		handled, err := retryUseCase(h).Execute(context.Background())

		// Then
		if err != nil {
			t.Fatalf("the pending operation should have been settled, got %v", err)
		}
		if handled != 1 {
			t.Errorf("handled = %d, want 1", handled)
		}
		if pending.Status() != wagering.Processed {
			t.Errorf("status = %s, want PROCESSED", pending.Status())
		}
		if entry.Direction() != wallet.Credit || entry.BalanceAfter().String() != "1025.00" {
			t.Errorf("entry = %s to %s", entry.Direction(), entry.BalanceAfter().String())
		}
	})

	t.Run("Given the reference is still missing/When the worker runs/Then the next attempt grows with the backoff", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		pending := pendingRollback(t, account, 1)

		h.writes(1)
		h.transactions.EXPECT().ClaimDuePending(gomock.Any(), clockReading, 1).Return([]*wagering.Transaction{pending}, nil)
		h.wallets.EXPECT().FindForUpdate(gomock.Any(), account.ID()).Return(account, nil)
		h.transactions.EXPECT().FindByExternalID(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
		h.transactions.EXPECT().Update(gomock.Any(), pending).Return(nil)
		h.referenceMetrics.EXPECT().ReferenceRetried()

		// When
		handled, err := retryUseCase(h).Execute(context.Background())

		// Then
		if err != nil {
			t.Fatalf("waiting again is not a failure, got %v", err)
		}
		if handled != 1 {
			t.Errorf("handled = %d, want 1", handled)
		}
		if pending.Status() != wagering.PendingReference {
			t.Errorf("status = %s, want PENDING_REFERENCE", pending.Status())
		}
		if want := clockReading.Add(4 * time.Second); !pending.NextAttemptAt().Equal(want) {
			t.Errorf("next attempt = %s, want %s", pending.NextAttemptAt(), want)
		}
		if pending.ReferenceAttempts() != 2 {
			t.Errorf("attempts = %d, want 2", pending.ReferenceAttempts())
		}
	})

	t.Run("Given a failure that will not heal/When the worker runs/Then the operation is marked as failed", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		claimed := pendingRollback(t, account, 1)
		stored := pendingRollback(t, account, 1)
		broken := errors.New("the wallet row is corrupt")

		h.writes(2)
		h.transactions.EXPECT().ClaimDuePending(gomock.Any(), gomock.Any(), gomock.Any()).Return([]*wagering.Transaction{claimed}, nil)
		h.wallets.EXPECT().FindForUpdate(gomock.Any(), account.ID()).Return(nil, broken)
		h.transactions.EXPECT().FindByIDForUpdate(gomock.Any(), claimed.ID()).Return(stored, nil)
		h.transactions.EXPECT().Update(gomock.Any(), stored).Return(nil)
		h.referenceMetrics.EXPECT().ReferenceFailed()

		// When
		handled, err := retryUseCase(h).Execute(context.Background())

		// Then
		if err != nil {
			t.Fatalf("giving up is a handled outcome, got %v", err)
		}
		if handled != 1 {
			t.Errorf("handled = %d, want 1", handled)
		}
		if stored.Status() != wagering.Failed {
			t.Errorf("status = %s, want FAILED", stored.Status())
		}
	})

	t.Run("Given a failure that may heal/When the worker runs/Then the operation is left pending", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		claimed := pendingRollback(t, account, 1)

		h.writes(1)
		h.transactions.EXPECT().ClaimDuePending(gomock.Any(), gomock.Any(), gomock.Any()).Return([]*wagering.Transaction{claimed}, nil)
		h.wallets.EXPECT().FindForUpdate(gomock.Any(), account.ID()).Return(nil, repositories.ErrTransient)

		// When
		handled, err := retryUseCase(h).Execute(context.Background())

		// Then
		if err == nil {
			t.Fatal("a transient failure should reach the worker loop")
		}
		if handled != 0 {
			t.Errorf("handled = %d, want 0", handled)
		}
		if claimed.Status() != wagering.PendingReference {
			t.Errorf("status = %s, want PENDING_REFERENCE", claimed.Status())
		}
	})
}
