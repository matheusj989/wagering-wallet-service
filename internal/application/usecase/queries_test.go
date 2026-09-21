package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

func ledgerEntry(t *testing.T, walletID uuid.UUID, before string, after string, version int64) wallet.LedgerEntry {
	t.Helper()

	entry, err := wallet.NewLedgerEntry(uuid.Must(uuid.NewV7()), walletID, uuid.Must(uuid.NewV7()),
		wallet.Debit, brl(t, "25.00"), brl(t, before), brl(t, after), version, clockReading)
	if err != nil {
		t.Fatalf("the ledger entry could not be built: %v", err)
	}
	return entry
}

func TestFindWallet(t *testing.T) {
	t.Run("Given a wallet that exists/When it is read/Then it comes back", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		h.reads(1)
		h.wallets.EXPECT().Find(gomock.Any(), account.ID()).Return(account, nil)

		// When
		found, err := usecase.NewFindWallet(h.unitOfWork, h.logger).Execute(context.Background(), account.ID())

		// Then
		if err != nil {
			t.Fatalf("the wallet should have been read, got %v", err)
		}
		if found.ID() != account.ID() {
			t.Errorf("wallet = %s, want %s", found.ID(), account.ID())
		}
	})

	t.Run("Given no wallet with that id/When it is read/Then the domain error is answered", func(t *testing.T) {
		// Given
		h := newHarness(t)
		h.reads(1)
		h.wallets.EXPECT().Find(gomock.Any(), gomock.Any()).Return(nil, nil)

		// When
		_, err := usecase.NewFindWallet(h.unitOfWork, h.logger).Execute(context.Background(), uuid.Must(uuid.NewV7()))

		// Then
		if !errors.Is(err, wallet.ErrNotFound) {
			t.Fatalf("error = %v, want %v", err, wallet.ErrNotFound)
		}
	})
}

func TestListWalletLedger(t *testing.T) {
	t.Run("Given more entries than the page holds/When the ledger is read/Then the page carries a cursor", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "950.00"), 3)
		entries := []wallet.LedgerEntry{
			ledgerEntry(t, account.ID(), "1000.00", "975.00", 2),
			ledgerEntry(t, account.ID(), "975.00", "950.00", 3),
			ledgerEntry(t, account.ID(), "950.00", "925.00", 4),
		}

		h.reads(1)
		h.wallets.EXPECT().Find(gomock.Any(), account.ID()).Return(account, nil)
		h.ledger.EXPECT().ListAfterVersion(gomock.Any(), account.ID(), int64(1), 3).Return(entries, nil)

		// When
		page, err := usecase.NewListWalletLedger(h.unitOfWork, h.logger).Execute(context.Background(), dto.ListLedgerInput{
			WalletID:     account.ID(),
			AfterVersion: 1,
			Limit:        2,
		})

		// Then
		if err != nil {
			t.Fatalf("the ledger should have been read, got %v", err)
		}
		if len(page.Entries) != 2 || !page.HasMore {
			t.Fatalf("page = %d entries with more %v, want 2 and true", len(page.Entries), page.HasMore)
		}
		if page.NextCursor != 3 {
			t.Errorf("cursor = %d, want the version of the last entry in the page", page.NextCursor)
		}
	})

	t.Run("Given the last page/When the ledger is read/Then no cursor is offered", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "975.00"), 2)

		h.reads(1)
		h.wallets.EXPECT().Find(gomock.Any(), account.ID()).Return(account, nil)
		h.ledger.EXPECT().ListAfterVersion(gomock.Any(), account.ID(), int64(0), 51).
			Return([]wallet.LedgerEntry{ledgerEntry(t, account.ID(), "1000.00", "975.00", 2)}, nil)

		// When
		page, err := usecase.NewListWalletLedger(h.unitOfWork, h.logger).Execute(context.Background(), dto.ListLedgerInput{
			WalletID: account.ID(),
			Limit:    50,
		})

		// Then
		if err != nil {
			t.Fatalf("the ledger should have been read, got %v", err)
		}
		if page.HasMore || page.NextCursor != 0 {
			t.Errorf("page offers more %v with cursor %d, want false and 0", page.HasMore, page.NextCursor)
		}
	})

	t.Run("Given a page size outside the contract/When the ledger is read/Then nothing is read", func(t *testing.T) {
		// Given
		h := newHarness(t)

		// When
		_, err := usecase.NewListWalletLedger(h.unitOfWork, h.logger).Execute(context.Background(), dto.ListLedgerInput{
			WalletID: uuid.Must(uuid.NewV7()),
			Limit:    500,
		})

		// Then
		var problems *validation.Error
		if !errors.As(err, &problems) {
			t.Fatalf("error = %v, want a validation error", err)
		}
		if problems.Fields[0].Field != "limit" {
			t.Errorf("reported field = %s, want limit", problems.Fields[0].Field)
		}
	})
}

func TestReconcileWallet(t *testing.T) {
	t.Run("Given a balance that matches the ledger/When it is reconciled/Then the report is consistent", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "975.00"), 2)

		h.reads(1)
		h.wallets.EXPECT().Find(gomock.Any(), account.ID()).Return(account, nil)
		h.ledger.EXPECT().Totals(gomock.Any(), account.ID(), money.Currency("BRL")).
			Return(wallet.LedgerTotals{Balance: brl(t, "975.00"), Entries: 2}, nil)

		// When
		report, err := usecase.NewReconcileWallet(h.unitOfWork, h.reconciliationMetrics, h.logger).
			Execute(context.Background(), account.ID())

		// Then
		if err != nil {
			t.Fatalf("the wallet should have been reconciled, got %v", err)
		}
		if !report.Consistent || !report.Difference.IsZero() || report.CheckedEntries != 2 {
			t.Errorf("report = consistent %v with difference %s over %d entries",
				report.Consistent, report.Difference.String(), report.CheckedEntries)
		}
	})

	t.Run("Given a balance that drifted from the ledger/When it is reconciled/Then the divergence is counted", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 2)

		h.reads(1)
		h.wallets.EXPECT().Find(gomock.Any(), account.ID()).Return(account, nil)
		h.ledger.EXPECT().Totals(gomock.Any(), account.ID(), gomock.Any()).
			Return(wallet.LedgerTotals{Balance: brl(t, "975.00"), Entries: 2}, nil)
		h.reconciliationMetrics.EXPECT().ReconciliationDivergence()

		// When
		report, err := usecase.NewReconcileWallet(h.unitOfWork, h.reconciliationMetrics, h.logger).
			Execute(context.Background(), account.ID())

		// Then
		if err != nil {
			t.Fatalf("a divergence is a report, not a failure, got %v", err)
		}
		if report.Consistent || report.Difference.String() != "25.00" {
			t.Errorf("report = consistent %v with difference %s", report.Consistent, report.Difference.String())
		}
	})
}

func TestFindWagerTransaction(t *testing.T) {
	t.Run("Given a transaction that exists/When it is read by id/Then it comes back", func(t *testing.T) {
		// Given
		h := newHarness(t)
		operation := transactionWith(t, nil)
		h.reads(1)
		h.transactions.EXPECT().FindByID(gomock.Any(), operation.ID()).Return(operation, nil)

		// When
		found, err := usecase.NewFindWagerTransaction(h.unitOfWork, h.logger).ByID(context.Background(), operation.ID())

		// Then
		if err != nil {
			t.Fatalf("the transaction should have been read, got %v", err)
		}
		if found.ID() != operation.ID() {
			t.Errorf("transaction = %s, want %s", found.ID(), operation.ID())
		}
	})

	t.Run("Given a transaction that exists/When it is read by provider and external id/Then it comes back", func(t *testing.T) {
		// Given
		h := newHarness(t)
		operation := transactionWith(t, nil)
		h.reads(1)
		h.transactions.EXPECT().FindByExternalID(gomock.Any(), "provider-a", "transaction-1").Return(operation, nil)

		// When
		found, err := usecase.NewFindWagerTransaction(h.unitOfWork, h.logger).
			ByExternalID(context.Background(), "provider-a", "transaction-1")

		// Then
		if err != nil {
			t.Fatalf("the transaction should have been read, got %v", err)
		}
		if found.ExternalTransactionID() != "transaction-1" {
			t.Errorf("external id = %s, want transaction-1", found.ExternalTransactionID())
		}
	})

	t.Run("Given no transaction with that identity/When it is read/Then the domain error is answered", func(t *testing.T) {
		// Given
		h := newHarness(t)
		h.reads(1)
		h.transactions.EXPECT().FindByID(gomock.Any(), gomock.Any()).Return(nil, nil)

		// When
		_, err := usecase.NewFindWagerTransaction(h.unitOfWork, h.logger).ByID(context.Background(), uuid.Must(uuid.NewV7()))

		// Then
		if !errors.Is(err, wagering.ErrNotFound) {
			t.Fatalf("error = %v, want %v", err, wagering.ErrNotFound)
		}
	})
}
