package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/application/idempotency"
	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

const (
	referenceBackoff = 5 * time.Second
	referenceTTL     = 15 * time.Minute
)

func processUseCase(h *harness) usecase.ProcessWagerTransaction {
	return usecase.NewProcessWagerTransaction(h.unitOfWork, h.clock, h.ids, h.wageringMetrics, h.logger,
		usecase.ProcessSettings{ReferenceBackoff: referenceBackoff, ReferenceTTL: referenceTTL})
}

func wagerInput(t *testing.T, account *wallet.Wallet, mutate func(*dto.ProcessWagerInput)) dto.ProcessWagerInput {
	t.Helper()

	input := dto.ProcessWagerInput{
		Origin:                wagering.HTTP,
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-1",
		IdempotencyKey:        "provider-a:transaction-1",
		PlayerID:              account.PlayerID(),
		WalletID:              account.ID(),
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  wagering.Bet,
		Amount:                brl(t, "25.00"),
		CorrelationID:         "correlation-1",
	}
	if mutate != nil {
		mutate(&input)
	}
	return input
}

func hashOf(t *testing.T, input dto.ProcessWagerInput) string {
	t.Helper()

	hash, err := idempotency.Hash(idempotency.Payload{
		ProviderID:            input.ProviderID,
		ExternalTransactionID: input.ExternalTransactionID,
		PlayerID:              input.PlayerID,
		WalletID:              input.WalletID,
		RoundID:               input.RoundID,
		GameID:                input.GameID,
		Kind:                  input.Kind,
		Amount:                input.Amount,
		ReferenceExternalID:   input.ReferenceExternalID,
	})
	if err != nil {
		t.Fatalf("the payload hash could not be built: %v", err)
	}
	return hash
}

func TestProcessWagerTransaction(t *testing.T) {
	t.Run("Given a bet the wallet can pay/When it is executed/Then the ledger, the wallet and the events are written once", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		input := wagerInput(t, account, nil)

		var entry wallet.LedgerEntry
		var settled *wagering.Transaction

		h.writes(1)
		h.transactions.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(true, nil)
		h.wallets.EXPECT().FindForUpdate(gomock.Any(), account.ID()).Return(account, nil)
		h.ledger.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, appended wallet.LedgerEntry) error {
				entry = appended
				return nil
			})
		h.wallets.EXPECT().Update(gomock.Any(), account, int64(4)).Return(nil)
		h.transactions.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, operation *wagering.Transaction) error {
				settled = operation
				return nil
			})
		h.expectEvents(2)
		h.transactions.EXPECT().WakeUpWaitingFor(gomock.Any(), "provider-a", "transaction-1", gomock.Any()).Return(int64(0), nil)
		h.allowWageringMetrics()

		// When
		result, err := processUseCase(h).Execute(context.Background(), input)

		// Then
		if err != nil {
			t.Fatalf("the bet should have been settled, got %v", err)
		}
		if result.Status != wagering.Processed || result.IdempotentReplay {
			t.Errorf("result = %s, replay %v", result.Status, result.IdempotentReplay)
		}
		if result.Balance.String() != "975.00" {
			t.Errorf("balance = %s, want 975.00", result.Balance.String())
		}
		if entry.Direction() != wallet.Debit || entry.BalanceAfter().String() != "975.00" {
			t.Errorf("entry = %s to %s", entry.Direction(), entry.BalanceAfter().String())
		}
		if settled.Status() != wagering.Processed {
			t.Errorf("stored status = %s, want PROCESSED", settled.Status())
		}
	})

	t.Run("Given a bet above the balance/When it is executed/Then it is rejected and still recorded", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "10.00"), 2)
		input := wagerInput(t, account, func(i *dto.ProcessWagerInput) { i.Amount = brl(t, "5000.00") })

		h.writes(1)
		h.transactions.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(true, nil)
		h.wallets.EXPECT().FindForUpdate(gomock.Any(), account.ID()).Return(account, nil)
		h.transactions.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
		h.expectEvents(1)
		h.transactions.EXPECT().WakeUpWaitingFor(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(int64(0), nil)
		h.allowWageringMetrics()

		// When
		result, err := processUseCase(h).Execute(context.Background(), input)

		// Then
		if err != nil {
			t.Fatalf("a rejection is a settled outcome, got %v", err)
		}
		if result.Status != wagering.Rejected || result.FailureCode != wagering.InsufficientFunds {
			t.Errorf("result = %s / %s, want REJECTED / INSUFFICIENT_FUNDS", result.Status, result.FailureCode)
		}
		if result.Balance.String() != "10.00" {
			t.Errorf("balance = %s, want the untouched 10.00", result.Balance.String())
		}
	})

	t.Run("Given the same key and the same payload/When it is executed/Then the stored outcome is answered", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		input := wagerInput(t, account, nil)
		existing := transactionWith(t, func(s *wagering.Snapshot) {
			s.PayloadHash = hashOf(t, input)
			s.WalletID = account.ID()
			s.PlayerID = account.PlayerID()
		})

		h.writes(1)
		h.transactions.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(false, nil)
		h.transactions.EXPECT().FindByIdempotencyKey(gomock.Any(), "provider-a", "provider-a:transaction-1").Return(existing, nil)
		h.allowWageringMetrics()

		// When
		result, err := processUseCase(h).Execute(context.Background(), input)

		// Then
		if err != nil {
			t.Fatalf("a replay should be answered, got %v", err)
		}
		if !result.IdempotentReplay || result.TransactionID != existing.ID() {
			t.Errorf("result = %s with replay %v", result.TransactionID, result.IdempotentReplay)
		}
	})

	t.Run("Given a key already used with another payload/When it is executed/Then the key conflict names the first transaction", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		input := wagerInput(t, account, nil)
		existing := transactionWith(t, func(s *wagering.Snapshot) {
			s.PayloadHash = "1111111111111111111111111111111111111111111111111111111111111111"
		})

		h.writes(1)
		h.transactions.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(false, nil)
		h.transactions.EXPECT().FindByIdempotencyKey(gomock.Any(), gomock.Any(), gomock.Any()).Return(existing, nil)
		h.allowWageringMetrics()

		// When
		_, err := processUseCase(h).Execute(context.Background(), input)

		// Then
		var conflict *usecase.ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("error = %v, want a conflict", err)
		}
		if conflict.Kind != usecase.ConflictIdempotencyKey || conflict.TransactionID != existing.ID() {
			t.Errorf("conflict = %s on %s", conflict.Kind, conflict.TransactionID)
		}
	})

	t.Run("Given an external id already used with another key/When it is executed/Then the external conflict is answered", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		input := wagerInput(t, account, nil)
		reused := transactionWith(t, nil)

		h.writes(1)
		h.transactions.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(false, nil)
		h.transactions.EXPECT().FindByIdempotencyKey(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
		h.transactions.EXPECT().FindByExternalID(gomock.Any(), "provider-a", "transaction-1").Return(reused, nil)
		h.allowWageringMetrics()

		// When
		_, err := processUseCase(h).Execute(context.Background(), input)

		// Then
		var conflict *usecase.ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("error = %v, want a conflict", err)
		}
		if conflict.Kind != usecase.ConflictExternalID {
			t.Errorf("conflict = %s, want the external transaction id", conflict.Kind)
		}
	})

	t.Run("Given a rollback whose reference has not arrived/When it is executed/Then it waits with a deadline", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		input := wagerInput(t, account, func(i *dto.ProcessWagerInput) {
			i.Kind = wagering.Rollback
			i.ReferenceExternalID = "transaction-0"
		})

		var pending *wagering.Transaction
		h.writes(1)
		h.transactions.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(true, nil)
		h.wallets.EXPECT().FindForUpdate(gomock.Any(), account.ID()).Return(account, nil)
		h.transactions.EXPECT().FindByExternalID(gomock.Any(), "provider-a", "transaction-0").Return(nil, nil)
		h.transactions.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, operation *wagering.Transaction) error {
				pending = operation
				return nil
			})
		h.expectEvents(1)
		h.allowWageringMetrics()

		// When
		result, err := processUseCase(h).Execute(context.Background(), input)

		// Then
		if err != nil {
			t.Fatalf("the operation should have been parked, got %v", err)
		}
		if result.Status != wagering.PendingReference {
			t.Fatalf("status = %s, want PENDING_REFERENCE", result.Status)
		}
		if !pending.NextAttemptAt().Equal(clockReading.Add(referenceBackoff)) {
			t.Errorf("next attempt = %s, want %s", pending.NextAttemptAt(), clockReading.Add(referenceBackoff))
		}
		if !pending.ReferenceDeadlineAt().Equal(clockReading.Add(referenceTTL)) {
			t.Errorf("deadline = %s, want %s", pending.ReferenceDeadlineAt(), clockReading.Add(referenceTTL))
		}
	})

	t.Run("Given a message already handled/When it arrives again/Then the inbox answers with the stored outcome", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		settled := transactionWith(t, nil)
		input := wagerInput(t, account, func(i *dto.ProcessWagerInput) {
			i.Origin = wagering.Queue
			i.MessageID = "message-1"
			i.RawBodyHash = "2222222222222222222222222222222222222222222222222222222222222222"
		})
		handled := inboxMessage(t, input.MessageID, input.RawBodyHash, settled.ID(), messaging.OutcomeProcessed)

		h.writes(1)
		h.inbox.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(false, nil)
		h.inbox.EXPECT().Find(gomock.Any(), usecase.ConsumerName, "message-1").Return(&handled, nil)
		h.transactions.EXPECT().FindByID(gomock.Any(), settled.ID()).Return(settled, nil)
		h.allowWageringMetrics()

		// When
		result, err := processUseCase(h).Execute(context.Background(), input)

		// Then
		if err != nil {
			t.Fatalf("the redelivery should be answered, got %v", err)
		}
		if !result.IdempotentReplay || result.InboxOutcome != messaging.OutcomeProcessed {
			t.Errorf("result = replay %v with outcome %s", result.IdempotentReplay, result.InboxOutcome)
		}
	})

	t.Run("Given a message id reused with another body/When it arrives/Then the mismatch is reported", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		input := wagerInput(t, account, func(i *dto.ProcessWagerInput) {
			i.Origin = wagering.Queue
			i.MessageID = "message-1"
			i.RawBodyHash = "2222222222222222222222222222222222222222222222222222222222222222"
		})
		handled := inboxMessage(t, input.MessageID,
			"3333333333333333333333333333333333333333333333333333333333333333",
			uuid.Must(uuid.NewV7()), messaging.OutcomeProcessed)

		h.writes(1)
		h.inbox.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(false, nil)
		h.inbox.EXPECT().Find(gomock.Any(), usecase.ConsumerName, "message-1").Return(&handled, nil)
		h.allowWageringMetrics()

		// When
		_, err := processUseCase(h).Execute(context.Background(), input)

		// Then
		if !errors.Is(err, messaging.ErrPayloadMismatch) {
			t.Fatalf("error = %v, want the inbox mismatch", err)
		}
	})

	t.Run("Given an input outside the contract/When it is executed/Then nothing is persisted", func(t *testing.T) {
		scenarios := []struct {
			name   string
			mutate func(*dto.ProcessWagerInput)
			field  string
		}{
			{"provider missing", func(i *dto.ProcessWagerInput) { i.ProviderID = "" }, "providerId"},
			{"key missing", func(i *dto.ProcessWagerInput) { i.IdempotencyKey = "" }, "Idempotency-Key"},
			{"kind unknown", func(i *dto.ProcessWagerInput) { i.Kind = "TRANSFER" }, "kind"},
			{"opening over the wire", func(i *dto.ProcessWagerInput) { i.Kind = wagering.Opening }, "kind"},
			{"bet with a reference", func(i *dto.ProcessWagerInput) { i.ReferenceExternalID = "transaction-0" }, "referenceExternalTransactionId"},
			{"rollback without a reference", func(i *dto.ProcessWagerInput) { i.Kind = wagering.Rollback }, "referenceExternalTransactionId"},
			{"loss carrying money", func(i *dto.ProcessWagerInput) { i.Kind = wagering.Loss }, "money.amount"},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				h := newHarness(t)
				account := openWalletWith(t, brl(t, "1000.00"), 4)

				// When
				_, err := processUseCase(h).Execute(context.Background(), wagerInput(t, account, scenario.mutate))

				// Then
				var problems *validation.Error
				if !errors.As(err, &problems) {
					t.Fatalf("error = %v, want a validation error", err)
				}
				if !reports(problems, scenario.field) {
					t.Errorf("reported fields = %v, want %s among them", problems.Fields, scenario.field)
				}
			})
		}
	})

	t.Run("Given a queue message named with the queue field/When the key is missing/Then the field is named as the queue does", func(t *testing.T) {
		// Given
		h := newHarness(t)
		account := openWalletWith(t, brl(t, "1000.00"), 4)
		input := wagerInput(t, account, func(i *dto.ProcessWagerInput) {
			i.Origin = wagering.Queue
			i.IdempotencyKey = ""
		})

		// When
		_, err := processUseCase(h).Execute(context.Background(), input)

		// Then
		var problems *validation.Error
		if !errors.As(err, &problems) {
			t.Fatalf("error = %v, want a validation error", err)
		}
		if !reports(problems, "data.idempotencyKey") {
			t.Errorf("reported fields = %v, want data.idempotencyKey", problems.Fields)
		}
	})
}

func inboxMessage(t *testing.T, messageID string, payloadHash string, transactionID uuid.UUID, outcome messaging.Outcome) messaging.InboxMessage {
	t.Helper()

	message, err := messaging.RehydrateInboxMessage(messaging.InboxSnapshot{
		ConsumerName:  usecase.ConsumerName,
		MessageID:     messageID,
		PayloadHash:   payloadHash,
		TransactionID: transactionID,
		Outcome:       outcome,
		ReceivedAt:    clockReading,
		CompletedAt:   clockReading,
	})
	if err != nil {
		t.Fatalf("the inbox message could not be rehydrated: %v", err)
	}
	return message
}

func reports(problems *validation.Error, field string) bool {
	for _, reported := range problems.Fields {
		if reported.Field == field {
			return true
		}
	}
	return false
}
