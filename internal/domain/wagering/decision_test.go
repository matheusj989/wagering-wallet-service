package wagering_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

type fixture struct {
	account  *wallet.Wallet
	walletID uuid.UUID
	playerID uuid.UUID
}

func walletWith(t *testing.T, balance string) fixture {
	t.Helper()
	walletID := uuid.Must(uuid.NewV7())
	playerID := uuid.Must(uuid.NewV7())
	account, err := wallet.Rehydrate(walletID, playerID, brl(t, balance), 2, now, now)
	if err != nil {
		t.Fatalf("Rehydrate failed: %v", err)
	}
	return fixture{account: account, walletID: walletID, playerID: playerID}
}

func (f fixture) operation(t *testing.T, kind wagering.Kind, amount string, reference string) *wagering.Transaction {
	t.Helper()
	input := command(t, kind, amount)
	input.WalletID = f.walletID
	input.PlayerID = f.playerID
	input.ReferenceExternalID = reference
	operation, err := wagering.NewExternal(input, uuid.Must(uuid.NewV7()), now)
	if err != nil {
		t.Fatalf("NewExternal failed: %v", err)
	}
	return operation
}

func (f fixture) reference(t *testing.T, kind wagering.Kind, status wagering.Status, amount string, round string) *wagering.Transaction {
	t.Helper()
	if kind == wagering.Opening {
		operation, err := wagering.NewOpening(uuid.Must(uuid.NewV7()), f.walletID, f.playerID, brl(t, amount), "correlation-0", now)
		if err != nil {
			t.Fatal(err)
		}
		return operation
	}
	if kind == wagering.Loss {
		amount = "0.00"
	}
	snapshot := wagering.Snapshot{
		ID:                    uuid.Must(uuid.NewV7()),
		Origin:                wagering.HTTP,
		Kind:                  kind,
		Status:                status,
		WalletID:              f.walletID,
		PlayerID:              f.playerID,
		Amount:                brl(t, amount),
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-100",
		IdempotencyKey:        "provider-a:transaction-100",
		PayloadHash:           hash,
		RoundID:               round,
		GameID:                "fortune-chimp",
		CorrelationID:         "correlation-0",
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if kind.Reversal() {
		snapshot.ReferenceExternalID = "earlier-bet"
		if status == wagering.Processed {
			snapshot.ReferenceTransactionID = uuid.Must(uuid.NewV7())
		}
	}
	switch status {
	case wagering.Processed:
		snapshot.ResultBalance = brl(t, amount)
		snapshot.CompletedAt = now
	case wagering.Rejected:
		snapshot.FailureCode = wagering.InsufficientFunds
		snapshot.ResultBalance = brl(t, "0.00")
		snapshot.CompletedAt = now
	case wagering.PendingReference:
		snapshot.NextAttemptAt = now
		snapshot.ReferenceDeadlineAt = now.Add(time.Minute)
	}
	original, err := wagering.Rehydrate(snapshot)
	if err != nil {
		t.Fatalf("Rehydrate failed: %v", err)
	}
	return original
}

func expectProcess(t *testing.T, decision wagering.Decision, direction wallet.Direction, amount string) {
	t.Helper()
	if decision.Outcome != wagering.OutcomeProcess {
		t.Fatalf("outcome = %d, want process (failure %s)", decision.Outcome, decision.FailureCode)
	}
	if direction == "" {
		if decision.Movement.Present() {
			t.Fatalf("expected no movement, got %s", decision.Movement.Direction)
		}
		return
	}
	if decision.Movement.Direction != direction {
		t.Fatalf("direction = %s, want %s", decision.Movement.Direction, direction)
	}
	if decision.Movement.Amount.String() != amount {
		t.Fatalf("amount = %s, want %s", decision.Movement.Amount.String(), amount)
	}
}

func expectReject(t *testing.T, decision wagering.Decision, code wagering.FailureCode) {
	t.Helper()
	if decision.Outcome != wagering.OutcomeReject {
		t.Fatalf("outcome = %d, want reject with %s", decision.Outcome, code)
	}
	if decision.FailureCode != code {
		t.Fatalf("failure code = %s, want %s", decision.FailureCode, code)
	}
	if decision.Movement.Present() {
		t.Fatal("a rejection must not carry a movement")
	}
}

func TestDecideOwnershipAndCurrency(t *testing.T) {
	t.Run("Given a payload for another player/When the operation is decided/Then it is rejected before any type rule", func(t *testing.T) {
		// Given
		f := walletWith(t, "100.00")
		operation := f.operation(t, wagering.Bet, "25.00", "")
		stranger := command(t, wagering.Bet, "25.00")
		stranger.WalletID = f.walletID
		stranger.PlayerID = uuid.Must(uuid.NewV7())
		fromStranger, err := wagering.NewExternal(stranger, uuid.Must(uuid.NewV7()), now)
		if err != nil {
			t.Fatalf("NewExternal failed: %v", err)
		}

		// When
		decision, err := wagering.Decide(f.account, fromStranger, nil, now)

		// Then
		if err != nil {
			t.Fatalf("Decide failed: %v", err)
		}
		expectReject(t, decision, wagering.WalletPlayerMismatch)
		if _, err := wagering.Decide(f.account, operation, nil, now); err != nil {
			t.Fatalf("the owner's operation should still decide: %v", err)
		}
	})

	t.Run("Given an operation in another supported currency/When it is decided/Then it is an auditable rejection", func(t *testing.T) {
		// Given
		f := walletWith(t, "100.00")
		dollars, err := money.Parse("25.00", "USD")
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		input := command(t, wagering.Bet, "25.00")
		input.WalletID = f.walletID
		input.PlayerID = f.playerID
		input.Amount = dollars
		operation, err := wagering.NewExternal(input, uuid.Must(uuid.NewV7()), now)
		if err != nil {
			t.Fatalf("NewExternal failed: %v", err)
		}

		// When
		decision, err := wagering.Decide(f.account, operation, nil, now)

		// Then
		if err != nil {
			t.Fatalf("Decide failed: %v", err)
		}
		expectReject(t, decision, wagering.CurrencyMismatch)
	})
}

func TestDecideBetWinLoss(t *testing.T) {
	t.Run("Given a bet/When it is decided/Then funds decide between debit and rejection", func(t *testing.T) {
		scenarios := []struct {
			name      string
			balance   string
			amount    string
			direction wallet.Direction
			code      wagering.FailureCode
		}{
			{"balance above the bet", "100.00", "25.00", wallet.Debit, ""},
			{"balance exactly the bet", "80.00", "80.00", wallet.Debit, ""},
			{"balance below the bet", "20.00", "80.00", "", wagering.InsufficientFunds},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				f := walletWith(t, scenario.balance)
				operation := f.operation(t, wagering.Bet, scenario.amount, "")

				// When
				decision, err := wagering.Decide(f.account, operation, nil, now)

				// Then
				if err != nil {
					t.Fatalf("Decide failed: %v", err)
				}
				if scenario.code != "" {
					expectReject(t, decision, scenario.code)
					return
				}
				expectProcess(t, decision, scenario.direction, scenario.amount)
			})
		}
	})

	t.Run("Given a loss/When it is decided/Then it settles without movement", func(t *testing.T) {
		// Given
		f := walletWith(t, "975.00")
		operation := f.operation(t, wagering.Loss, "0.00", "")

		// When
		decision, err := wagering.Decide(f.account, operation, nil, now)

		// Then
		if err != nil {
			t.Fatalf("Decide failed: %v", err)
		}
		expectProcess(t, decision, "", "")
	})

	t.Run("Given a win with an optional reference/When it is decided/Then it never waits for the bet", func(t *testing.T) {
		scenarios := []struct {
			name          string
			reference     string
			original      *wagering.Transaction
			referenceKind wagering.Kind
			round         string
			code          wagering.FailureCode
			resolves      bool
		}{
			{name: "no reference informed", reference: ""},
			{name: "reference not found yet", reference: "transaction-100"},
			{name: "reference is the bet of the round", reference: "transaction-100", referenceKind: wagering.Bet, round: "round-987", resolves: true},
			{name: "reference from another round", reference: "transaction-100", referenceKind: wagering.Bet, round: "round-000", code: wagering.ReferenceMismatch},
			{name: "reference is not a bet", reference: "transaction-100", referenceKind: wagering.Win, round: "round-987", code: wagering.ReferenceKindNotAllowed},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				f := walletWith(t, "100.00")
				operation := f.operation(t, wagering.Win, "100.00", scenario.reference)
				var reference *wagering.Reference
				if scenario.referenceKind != "" {
					reference = &wagering.Reference{
						Transaction: f.reference(t, scenario.referenceKind, wagering.Processed, "25.00", scenario.round),
					}
				}

				// When
				decision, err := wagering.Decide(f.account, operation, reference, now)

				// Then
				if err != nil {
					t.Fatalf("Decide failed: %v", err)
				}
				if scenario.code != "" {
					expectReject(t, decision, scenario.code)
					return
				}
				expectProcess(t, decision, wallet.Credit, "100.00")
				if scenario.resolves && decision.ReferenceID == uuid.Nil {
					t.Error("a resolved reference should be reported")
				}
				if !scenario.resolves && decision.ReferenceID != uuid.Nil {
					t.Error("no reference should be reported when it was not resolved")
				}
			})
		}
	})

	t.Run("Given a credit above the representable balance/When it is decided/Then it is a definitive rejection", func(t *testing.T) {
		// Given
		f := walletWith(t, "92233720368547758.07")
		operation := f.operation(t, wagering.Win, "0.01", "")

		// When
		decision, err := wagering.Decide(f.account, operation, nil, now)

		// Then
		if err != nil {
			t.Fatalf("Decide failed: %v", err)
		}
		expectReject(t, decision, wagering.BalanceLimitExceeded)
		if f.account.Balance().String() != "92233720368547758.07" || f.account.Version() != 2 {
			t.Error("the wallet must stay untouched")
		}
	})
}

func TestDecideReversals(t *testing.T) {
	t.Run("Given a reversal whose reference is not usable yet/When it is decided/Then it waits until the deadline", func(t *testing.T) {
		scenarios := []struct {
			name           string
			referenceState wagering.Status
			expired        bool
			outcome        wagering.Outcome
			code           wagering.FailureCode
		}{
			{"reference missing, still in time", "", false, wagering.OutcomeWaitReference, ""},
			{"reference missing, deadline reached", "", true, wagering.OutcomeReject, wagering.ReferenceNotFound},
			{"reference pending, still in time", wagering.PendingReference, false, wagering.OutcomeWaitReference, ""},
			{"reference pending, deadline reached", wagering.PendingReference, true, wagering.OutcomeReject, wagering.ReferenceNotProcessed},
			{"reference rejected", wagering.Rejected, false, wagering.OutcomeReject, wagering.ReferenceNotProcessed},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				f := walletWith(t, "975.00")
				operation := f.operation(t, wagering.Rollback, "25.00", "transaction-100")
				deadline := now.Add(15 * time.Minute)
				if err := operation.MarkPendingReference(now.Add(time.Second), deadline, now); err != nil {
					t.Fatalf("MarkPendingReference failed: %v", err)
				}
				attempt := now
				if scenario.expired {
					attempt = deadline.Add(time.Second)
				}
				var reference *wagering.Reference
				if scenario.referenceState != "" {
					kind := wagering.Bet
					if scenario.referenceState == wagering.PendingReference {
						kind = wagering.Refund
					}
					reference = &wagering.Reference{Transaction: f.reference(t, kind, scenario.referenceState, "25.00", "round-987")}
				}

				// When
				decision, err := wagering.Decide(f.account, operation, reference, attempt)

				// Then
				if err != nil {
					t.Fatalf("Decide failed: %v", err)
				}
				if scenario.outcome == wagering.OutcomeWaitReference {
					if decision.Outcome != wagering.OutcomeWaitReference {
						t.Fatalf("outcome = %d, want wait", decision.Outcome)
					}
					return
				}
				expectReject(t, decision, scenario.code)
			})
		}
	})

	t.Run("Given a processed reference/When the reversal is decided/Then the direction mirrors the original movement", func(t *testing.T) {
		scenarios := []struct {
			name      string
			reversal  wagering.Kind
			original  wagering.Kind
			balance   string
			direction wallet.Direction
			code      wagering.FailureCode
		}{
			{"refund of a bet credits", wagering.Refund, wagering.Bet, "975.00", wallet.Credit, ""},
			{"rollback of a bet credits", wagering.Rollback, wagering.Bet, "975.00", wallet.Credit, ""},
			{"rollback of a win debits", wagering.Rollback, wagering.Win, "975.00", wallet.Debit, ""},
			{"rollback of a refund debits", wagering.Rollback, wagering.Refund, "975.00", wallet.Debit, ""},
			{"refund of a win is not allowed", wagering.Refund, wagering.Win, "975.00", "", wagering.ReferenceKindNotAllowed},
			{"rollback of a loss is not allowed", wagering.Rollback, wagering.Loss, "975.00", "", wagering.ReferenceKindNotAllowed},
			{"rollback of an opening is not allowed", wagering.Rollback, wagering.Opening, "975.00", "", wagering.ReferenceKindNotAllowed},
			{"rollback of a rollback is not allowed", wagering.Rollback, wagering.Rollback, "975.00", "", wagering.ReferenceKindNotAllowed},
			{"rollback of a win without funds", wagering.Rollback, wagering.Win, "20.00", "", wagering.ReversalInsufficientFunds},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				f := walletWith(t, scenario.balance)
				operation := f.operation(t, scenario.reversal, "25.00", "transaction-100")
				reference := &wagering.Reference{
					Transaction: f.reference(t, scenario.original, wagering.Processed, "25.00", "round-987"),
				}

				// When
				decision, err := wagering.Decide(f.account, operation, reference, now)

				// Then
				if err != nil {
					t.Fatalf("Decide failed: %v", err)
				}
				if scenario.code != "" {
					expectReject(t, decision, scenario.code)
					return
				}
				expectProcess(t, decision, scenario.direction, "25.00")
				if decision.ReferenceID != reference.Transaction.ID() {
					t.Error("the resolved reference should be reported")
				}
			})
		}
	})

	t.Run("Given a reversal that disagrees with its reference/When it is decided/Then it is rejected", func(t *testing.T) {
		scenarios := []struct {
			name            string
			amount          string
			round           string
			alreadyReversed bool
			code            wagering.FailureCode
		}{
			{"amount larger than the original", "250.00", "round-987", false, wagering.ReferenceMismatch},
			{"amount smaller than the original", "10.00", "round-987", false, wagering.ReferenceMismatch},
			{"another round", "25.00", "round-000", false, wagering.ReferenceMismatch},
			{"reference already reversed", "25.00", "round-987", true, wagering.AlreadyReversed},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				f := walletWith(t, "975.00")
				operation := f.operation(t, wagering.Rollback, scenario.amount, "transaction-100")
				reference := &wagering.Reference{
					Transaction:     f.reference(t, wagering.Bet, wagering.Processed, "25.00", scenario.round),
					AlreadyReversed: scenario.alreadyReversed,
				}

				// When
				decision, err := wagering.Decide(f.account, operation, reference, now)

				// Then
				if err != nil {
					t.Fatalf("Decide failed: %v", err)
				}
				expectReject(t, decision, scenario.code)
			})
		}
	})

	t.Run("Given a crediting reversal above the limit/When it is decided/Then the balance limit is reported", func(t *testing.T) {
		// Given
		f := walletWith(t, "92233720368547758.07")
		operation := f.operation(t, wagering.Refund, "25.00", "transaction-100")
		reference := &wagering.Reference{
			Transaction: f.reference(t, wagering.Bet, wagering.Processed, "25.00", "round-987"),
		}

		// When
		decision, err := wagering.Decide(f.account, operation, reference, now)

		// Then
		if err != nil {
			t.Fatalf("Decide failed: %v", err)
		}
		expectReject(t, decision, wagering.BalanceLimitExceeded)
	})

	t.Run("Given an unusable reference and a disagreeing payload/When it is decided/Then the reference state is reported first", func(t *testing.T) {
		// Given
		f := walletWith(t, "975.00")
		operation := f.operation(t, wagering.Refund, "250.00", "transaction-100")
		reference := &wagering.Reference{
			Transaction:     f.reference(t, wagering.Win, wagering.Rejected, "25.00", "round-000"),
			AlreadyReversed: true,
		}

		// When
		decision, err := wagering.Decide(f.account, operation, reference, now)

		// Then
		if err != nil {
			t.Fatalf("Decide failed: %v", err)
		}
		expectReject(t, decision, wagering.ReferenceNotProcessed)
	})
}
