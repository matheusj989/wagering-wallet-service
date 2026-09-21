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

func openWalletUseCase(h *harness) usecase.OpenWallet {
	return usecase.NewOpenWallet(h.unitOfWork, h.clock, h.ids, h.logger)
}

func TestOpenWallet(t *testing.T) {
	t.Run("Given a funded opening/When it is executed/Then the wallet, the opening and the ledger are written together", func(t *testing.T) {
		// Given
		h := newHarness(t)
		playerID := uuid.Must(uuid.NewV7())

		var created *wallet.Wallet
		var opening *wagering.Transaction
		var entry wallet.LedgerEntry

		h.writes(1)
		h.wallets.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, opened *wallet.Wallet) error {
				created = opened
				return nil
			})
		h.transactions.EXPECT().Insert(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, operation *wagering.Transaction) (bool, error) {
				opening = operation
				return true, nil
			})
		h.ledger.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, appended wallet.LedgerEntry) error {
				entry = appended
				return nil
			})
		h.expectEvents(2)

		// When
		opened, err := openWalletUseCase(h).Execute(context.Background(), dto.OpenWalletInput{
			PlayerID:       playerID,
			InitialBalance: brl(t, "1000.00"),
			CorrelationID:  "correlation-1",
		})

		// Then
		if err != nil {
			t.Fatalf("the wallet should have been opened, got %v", err)
		}
		if opened.PlayerID() != playerID || opened.Balance().String() != "1000.00" {
			t.Errorf("wallet = player %s with %s", opened.PlayerID(), opened.Balance().String())
		}
		if opened.Version() != 1 {
			t.Errorf("version = %d, want 1", opened.Version())
		}
		if created != opened {
			t.Error("the wallet handed to the repository should be the one answered")
		}
		if opening.Kind() != wagering.Opening || opening.Origin() != wagering.Internal {
			t.Errorf("opening = %s from %s, want OPENING from INTERNAL", opening.Kind(), opening.Origin())
		}
		if entry.BalanceAfter().String() != "1000.00" || entry.WalletVersion() != 1 {
			t.Errorf("entry closes at %s on version %d", entry.BalanceAfter().String(), entry.WalletVersion())
		}
	})

	t.Run("Given an opening with no money/When it is executed/Then no transaction and no ledger entry are written", func(t *testing.T) {
		// Given
		h := newHarness(t)
		h.writes(1)
		h.wallets.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

		// When
		opened, err := openWalletUseCase(h).Execute(context.Background(), dto.OpenWalletInput{
			PlayerID:       uuid.Must(uuid.NewV7()),
			InitialBalance: brl(t, "0.00"),
			CorrelationID:  "correlation-1",
		})

		// Then
		if err != nil {
			t.Fatalf("the wallet should have been opened, got %v", err)
		}
		if !opened.Balance().IsZero() {
			t.Errorf("balance = %s, want 0.00", opened.Balance().String())
		}
	})

	t.Run("Given an input outside the contract/When it is executed/Then nothing is persisted", func(t *testing.T) {
		scenarios := []struct {
			name  string
			input dto.OpenWalletInput
			field string
		}{
			{"player missing", dto.OpenWalletInput{InitialBalance: brl(t, "10.00")}, "playerId"},
			{"balance not decoded", dto.OpenWalletInput{PlayerID: uuid.Must(uuid.NewV7())}, "initialBalance.amount"},
			{
				"balance negative",
				dto.OpenWalletInput{PlayerID: uuid.Must(uuid.NewV7()), InitialBalance: negative(t)},
				"initialBalance.amount",
			},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				h := newHarness(t)

				// When
				_, err := openWalletUseCase(h).Execute(context.Background(), scenario.input)

				// Then
				var problems *validation.Error
				if !errors.As(err, &problems) {
					t.Fatalf("error = %v, want a validation error", err)
				}
				if problems.Fields[0].Field != scenario.field {
					t.Errorf("reported field = %s, want %s", problems.Fields[0].Field, scenario.field)
				}
			})
		}
	})

	t.Run("Given the player already has a wallet/When it is executed/Then the answer carries the existing wallet", func(t *testing.T) {
		// Given
		h := newHarness(t)
		existing := openWalletWith(t, brl(t, "500.00"), 3)

		h.writes(1)
		h.wallets.EXPECT().Create(gomock.Any(), gomock.Any()).Return(wallet.ErrAlreadyExists)
		h.reads(1)
		h.wallets.EXPECT().FindByPlayer(gomock.Any(), gomock.Any(), money.Currency("BRL")).Return(existing, nil)

		// When
		_, err := openWalletUseCase(h).Execute(context.Background(), dto.OpenWalletInput{
			PlayerID:       existing.PlayerID(),
			InitialBalance: brl(t, "1000.00"),
			CorrelationID:  "correlation-1",
		})

		// Then
		var conflict *usecase.WalletConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("error = %v, want a wallet conflict", err)
		}
		if conflict.WalletID != existing.ID() {
			t.Errorf("conflict points at %s, want %s", conflict.WalletID, existing.ID())
		}
	})

	t.Run("Given the wallet exists but cannot be read back/When it is executed/Then the plain domain error is answered", func(t *testing.T) {
		// Given
		h := newHarness(t)
		h.writes(1)
		h.wallets.EXPECT().Create(gomock.Any(), gomock.Any()).Return(wallet.ErrAlreadyExists)
		h.reads(1)
		h.wallets.EXPECT().FindByPlayer(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)

		// When
		_, err := openWalletUseCase(h).Execute(context.Background(), dto.OpenWalletInput{
			PlayerID:       uuid.Must(uuid.NewV7()),
			InitialBalance: brl(t, "1000.00"),
		})

		// Then
		if !errors.Is(err, wallet.ErrAlreadyExists) {
			t.Fatalf("error = %v, want %v", err, wallet.ErrAlreadyExists)
		}
	})

	t.Run("Given the ledger cannot be written/When it is executed/Then the failure reaches the caller", func(t *testing.T) {
		// Given
		h := newHarness(t)
		broken := errors.New("the ledger is unavailable")

		h.writes(1)
		h.wallets.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
		h.transactions.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(true, nil)
		h.ledger.EXPECT().Append(gomock.Any(), gomock.Any()).Return(broken)

		// When
		_, err := openWalletUseCase(h).Execute(context.Background(), dto.OpenWalletInput{
			PlayerID:       uuid.Must(uuid.NewV7()),
			InitialBalance: brl(t, "1000.00"),
			CorrelationID:  "correlation-1",
		})

		// Then
		if !errors.Is(err, broken) {
			t.Fatalf("error = %v, want %v", err, broken)
		}
	})
}

func negative(t *testing.T) money.Money {
	t.Helper()

	value, err := brl(t, "10.00").Sub(brl(t, "20.00"))
	if err != nil {
		t.Fatalf("the negative amount could not be built: %v", err)
	}
	return value
}
