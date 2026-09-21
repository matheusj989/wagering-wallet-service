package wallet_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

var openedAt = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func amount(t *testing.T, value string) money.Money {
	t.Helper()
	parsed, err := money.Parse(value, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", value, err)
	}
	return parsed
}

func openWallet(t *testing.T, initial string) *wallet.Wallet {
	t.Helper()
	opened, _, err := wallet.Open(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), amount(t, initial),
		uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), openedAt)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", initial, err)
	}
	return opened
}

func TestOpen(t *testing.T) {
	t.Run("Given a positive initial balance/When the wallet is opened/Then version one carries the opening entry", func(t *testing.T) {
		// Given
		walletID := uuid.Must(uuid.NewV7())
		playerID := uuid.Must(uuid.NewV7())
		transactionID := uuid.Must(uuid.NewV7())
		entryID := uuid.Must(uuid.NewV7())

		// When
		opened, entry, err := wallet.Open(walletID, playerID, amount(t, "1000.00"), transactionID, entryID, openedAt)

		// Then
		if err != nil {
			t.Fatalf("Open failed: %v", err)
		}
		if opened.Version() != 1 || opened.Balance().String() != "1000.00" {
			t.Errorf("wallet = %s at version %d, want 1000.00 at version 1", opened.Balance().String(), opened.Version())
		}
		if entry == nil {
			t.Fatal("expected an opening entry")
		}
		if entry.Direction() != wallet.Credit || entry.WalletVersion() != 1 {
			t.Errorf("entry = %s at version %d, want CREDIT at version 1", entry.Direction(), entry.WalletVersion())
		}
		if entry.BalanceBefore().String() != "0.00" || entry.BalanceAfter().String() != "1000.00" {
			t.Errorf("entry moved %s -> %s, want 0.00 -> 1000.00", entry.BalanceBefore().String(), entry.BalanceAfter().String())
		}
		if entry.TransactionID() != transactionID || entry.WalletID() != walletID {
			t.Error("entry should point at the opening transaction and the wallet")
		}
	})

	t.Run("Given a zero initial balance/When the wallet is opened/Then no entry is produced", func(t *testing.T) {
		// Given, When
		opened, entry, err := wallet.Open(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), amount(t, "0.00"),
			uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), openedAt)

		// Then
		if err != nil {
			t.Fatalf("Open failed: %v", err)
		}
		if entry != nil {
			t.Errorf("expected no entry, got one at version %d", entry.WalletVersion())
		}
		if opened.Version() != 1 || !opened.Balance().IsZero() {
			t.Errorf("wallet = %s at version %d, want 0.00 at version 1", opened.Balance().String(), opened.Version())
		}
	})

	t.Run("Given invalid arguments/When the wallet is opened/Then it is rejected and nothing is created", func(t *testing.T) {
		valid := amount(t, "10.00")
		scenarios := []struct {
			name     string
			walletID uuid.UUID
			playerID uuid.UUID
			initial  money.Money
			now      time.Time
			want     error
		}{
			{"wallet id missing", uuid.Nil, uuid.Must(uuid.NewV7()), valid, openedAt, wallet.ErrMissingIdentifier},
			{"player id missing", uuid.Must(uuid.NewV7()), uuid.Nil, valid, openedAt, wallet.ErrMissingIdentifier},
			{"currency missing", uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), money.Money{}, openedAt, money.ErrInvalidCurrency},
			{"timestamp missing", uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), valid, time.Time{}, wallet.ErrMissingTimestamp},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given, When
				opened, entry, err := wallet.Open(scenario.walletID, scenario.playerID, scenario.initial,
					uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), scenario.now)

				// Then
				if !errors.Is(err, scenario.want) {
					t.Fatalf("error = %v, want %v", err, scenario.want)
				}
				if opened != nil || entry != nil {
					t.Error("no wallet or entry should be returned on failure")
				}
			})
		}
	})
}

func TestRehydrate(t *testing.T) {
	t.Run("Given persisted state/When the wallet is rehydrated/Then it exposes that state and nothing else happens", func(t *testing.T) {
		// Given
		walletID := uuid.Must(uuid.NewV7())
		playerID := uuid.Must(uuid.NewV7())
		updatedAt := openedAt.Add(time.Hour)

		// When
		restored, err := wallet.Rehydrate(walletID, playerID, amount(t, "975.00"), 2, openedAt, updatedAt)

		// Then
		if err != nil {
			t.Fatalf("Rehydrate failed: %v", err)
		}
		if restored.Balance().String() != "975.00" || restored.Version() != 2 {
			t.Errorf("wallet = %s at version %d, want 975.00 at version 2", restored.Balance().String(), restored.Version())
		}
		if !restored.CreatedAt().Equal(openedAt) || !restored.UpdatedAt().Equal(updatedAt) {
			t.Error("timestamps should be preserved as persisted")
		}
	})

	t.Run("Given inconsistent persisted state/When the wallet is rehydrated/Then it is rejected", func(t *testing.T) {
		scenarios := []struct {
			name    string
			balance money.Money
			version int64
			want    error
		}{
			{"version below one", amount(t, "10.00"), 0, wallet.ErrInvalidWalletVersion},
			{"currency missing", money.Money{}, 1, money.ErrInvalidCurrency},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given, When
				_, err := wallet.Rehydrate(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()),
					scenario.balance, scenario.version, openedAt, openedAt)

				// Then
				if !errors.Is(err, scenario.want) {
					t.Errorf("error = %v, want %v", err, scenario.want)
				}
			})
		}
	})
}

func TestDebitAndCredit(t *testing.T) {
	t.Run("Given a wallet with funds/When it is debited/Then balance drops and the entry records the movement", func(t *testing.T) {
		// Given
		account := openWallet(t, "100.00")
		transactionID := uuid.Must(uuid.NewV7())
		entryID := uuid.Must(uuid.NewV7())
		movedAt := openedAt.Add(time.Minute)

		// When
		entry, err := account.Debit(transactionID, amount(t, "80.00"), entryID, movedAt)

		// Then
		if err != nil {
			t.Fatalf("Debit failed: %v", err)
		}
		if account.Balance().String() != "20.00" || account.Version() != 2 {
			t.Errorf("wallet = %s at version %d, want 20.00 at version 2", account.Balance().String(), account.Version())
		}
		if entry.Direction() != wallet.Debit || entry.WalletVersion() != 2 {
			t.Errorf("entry = %s at version %d, want DEBIT at version 2", entry.Direction(), entry.WalletVersion())
		}
		if entry.BalanceBefore().String() != "100.00" || entry.BalanceAfter().String() != "20.00" {
			t.Errorf("entry moved %s -> %s, want 100.00 -> 20.00", entry.BalanceBefore().String(), entry.BalanceAfter().String())
		}
		if !account.UpdatedAt().Equal(movedAt) {
			t.Errorf("UpdatedAt = %s, want %s", account.UpdatedAt(), movedAt)
		}
	})

	t.Run("Given a wallet with funds/When it is credited/Then balance rises by the exact amount", func(t *testing.T) {
		// Given
		account := openWallet(t, "100.00")

		// When
		entry, err := account.Credit(uuid.Must(uuid.NewV7()), amount(t, "25.50"), uuid.Must(uuid.NewV7()), openedAt)

		// Then
		if err != nil {
			t.Fatalf("Credit failed: %v", err)
		}
		if account.Balance().String() != "125.50" || account.Version() != 2 {
			t.Errorf("wallet = %s at version %d, want 125.50 at version 2", account.Balance().String(), account.Version())
		}
		if entry.Direction() != wallet.Credit {
			t.Errorf("entry direction = %s, want CREDIT", entry.Direction())
		}
	})

	t.Run("Given a movement the wallet cannot take/When it is attempted/Then the aggregate stays untouched", func(t *testing.T) {
		maximum, err := money.Parse("92233720368547758.07", "BRL")
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}

		scenarios := []struct {
			name    string
			balance string
			move    func(*wallet.Wallet) (wallet.LedgerEntry, error)
			want    error
		}{
			{
				name:    "debit above the balance",
				balance: "20.00",
				move: func(w *wallet.Wallet) (wallet.LedgerEntry, error) {
					return w.Debit(uuid.Must(uuid.NewV7()), amount(t, "80.00"), uuid.Must(uuid.NewV7()), openedAt)
				},
				want: wallet.ErrInsufficientFunds,
			},
			{
				name:    "credit in another currency",
				balance: "100.00",
				move: func(w *wallet.Wallet) (wallet.LedgerEntry, error) {
					dollars, parseErr := money.Parse("10.00", "USD")
					if parseErr != nil {
						t.Fatalf("Parse failed: %v", parseErr)
					}
					return w.Credit(uuid.Must(uuid.NewV7()), dollars, uuid.Must(uuid.NewV7()), openedAt)
				},
				want: money.ErrCurrencyMismatch,
			},
			{
				name:    "zero amount",
				balance: "100.00",
				move: func(w *wallet.Wallet) (wallet.LedgerEntry, error) {
					return w.Debit(uuid.Must(uuid.NewV7()), amount(t, "0.00"), uuid.Must(uuid.NewV7()), openedAt)
				},
				want: wallet.ErrInvalidAmount,
			},
			{
				name:    "entry identifier missing",
				balance: "100.00",
				move: func(w *wallet.Wallet) (wallet.LedgerEntry, error) {
					return w.Debit(uuid.Must(uuid.NewV7()), amount(t, "10.00"), uuid.Nil, openedAt)
				},
				want: wallet.ErrMissingIdentifier,
			},
			{
				name:    "timestamp missing",
				balance: "100.00",
				move: func(w *wallet.Wallet) (wallet.LedgerEntry, error) {
					return w.Credit(uuid.Must(uuid.NewV7()), amount(t, "10.00"), uuid.Must(uuid.NewV7()), time.Time{})
				},
				want: wallet.ErrMissingTimestamp,
			},
			{
				name:    "credit beyond the representable balance",
				balance: "92233720368547758.07",
				move: func(w *wallet.Wallet) (wallet.LedgerEntry, error) {
					return w.Credit(uuid.Must(uuid.NewV7()), amount(t, "0.01"), uuid.Must(uuid.NewV7()), openedAt)
				},
				want: money.ErrOverflow,
			},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				account := openWallet(t, scenario.balance)
				balanceBefore := account.Balance()
				versionBefore := account.Version()
				updatedBefore := account.UpdatedAt()

				// When
				entry, err := scenario.move(account)

				// Then
				if !errors.Is(err, scenario.want) {
					t.Fatalf("error = %v, want %v", err, scenario.want)
				}
				if !account.Balance().Equal(balanceBefore) || account.Version() != versionBefore || !account.UpdatedAt().Equal(updatedBefore) {
					t.Errorf("wallet changed to %s at version %d", account.Balance().String(), account.Version())
				}
				if entry != (wallet.LedgerEntry{}) {
					t.Error("no entry should be returned on failure")
				}
			})
		}

		if maximum.Minor() != math.MaxInt64 {
			t.Fatalf("expected the limit to be MaxInt64, got %d", maximum.Minor())
		}
	})

	t.Run("Given several movements/When they are applied in sequence/Then versions and balances chain", func(t *testing.T) {
		// Given
		account := openWallet(t, "100.00")

		// When
		first, firstErr := account.Debit(uuid.Must(uuid.NewV7()), amount(t, "25.00"), uuid.Must(uuid.NewV7()), openedAt)
		second, secondErr := account.Debit(uuid.Must(uuid.NewV7()), amount(t, "10.00"), uuid.Must(uuid.NewV7()), openedAt)

		// Then
		if firstErr != nil || secondErr != nil {
			t.Fatalf("unexpected errors: %v / %v", firstErr, secondErr)
		}
		if first.WalletVersion() != 2 || second.WalletVersion() != 3 {
			t.Errorf("versions = %d and %d, want 2 and 3", first.WalletVersion(), second.WalletVersion())
		}
		if first.BalanceAfter().String() != second.BalanceBefore().String() {
			t.Errorf("chain broken: %s then %s", first.BalanceAfter().String(), second.BalanceBefore().String())
		}
		if account.Balance().String() != "65.00" || account.Version() != 3 {
			t.Errorf("wallet = %s at version %d, want 65.00 at version 3", account.Balance().String(), account.Version())
		}
	})
}

func TestLedgerEntry(t *testing.T) {
	t.Run("Given inconsistent arithmetic/When the entry is built/Then it is rejected", func(t *testing.T) {
		// Given
		before := amount(t, "100.00")
		after := amount(t, "105.00")

		// When
		_, err := wallet.NewLedgerEntry(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()),
			wallet.Credit, amount(t, "10.00"), before, after, 2, openedAt)

		// Then
		if !errors.Is(err, wallet.ErrInconsistentLedgerEntry) {
			t.Errorf("error = %v, want ErrInconsistentLedgerEntry", err)
		}
	})

	t.Run("Given invalid fields/When the entry is built/Then it is rejected", func(t *testing.T) {
		before := amount(t, "100.00")
		after := amount(t, "90.00")
		scenarios := []struct {
			name      string
			direction wallet.Direction
			value     money.Money
			version   int64
			createdAt time.Time
			want      error
		}{
			{"unknown direction", wallet.Direction("MOVE"), amount(t, "10.00"), 2, openedAt, wallet.ErrInvalidDirection},
			{"zero amount", wallet.Debit, amount(t, "0.00"), 2, openedAt, wallet.ErrInvalidAmount},
			{"version below one", wallet.Debit, amount(t, "10.00"), 0, openedAt, wallet.ErrInvalidWalletVersion},
			{"timestamp missing", wallet.Debit, amount(t, "10.00"), 2, time.Time{}, wallet.ErrMissingTimestamp},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given, When
				_, err := wallet.NewLedgerEntry(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()),
					scenario.direction, scenario.value, before, after, scenario.version, scenario.createdAt)

				// Then
				if !errors.Is(err, scenario.want) {
					t.Errorf("error = %v, want %v", err, scenario.want)
				}
			})
		}
	})

	t.Run("Given an entry/When a caller keeps a copy/Then the wallet state is not shared", func(t *testing.T) {
		// Given
		account := openWallet(t, "100.00")
		entry, err := account.Debit(uuid.Must(uuid.NewV7()), amount(t, "25.00"), uuid.Must(uuid.NewV7()), openedAt)
		if err != nil {
			t.Fatalf("Debit failed: %v", err)
		}

		// When
		copyOfEntry := entry
		_, err = account.Debit(uuid.Must(uuid.NewV7()), amount(t, "10.00"), uuid.Must(uuid.NewV7()), openedAt)
		if err != nil {
			t.Fatalf("second Debit failed: %v", err)
		}

		// Then
		if copyOfEntry.BalanceAfter().String() != "75.00" || copyOfEntry.WalletVersion() != 2 {
			t.Errorf("the first entry changed to %s at version %d", copyOfEntry.BalanceAfter().String(), copyOfEntry.WalletVersion())
		}
	})
}
