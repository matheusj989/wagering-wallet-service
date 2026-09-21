package wagering_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

var (
	now  = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	hash = strings.Repeat("a", 64)
)

func brl(t *testing.T, value string) money.Money {
	t.Helper()
	parsed, err := money.Parse(value, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", value, err)
	}
	return parsed
}

func command(t *testing.T, kind wagering.Kind, amount string) wagering.Command {
	t.Helper()
	return wagering.Command{
		Origin:                wagering.HTTP,
		Kind:                  kind,
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PayloadHash:           hash,
		PlayerID:              uuid.Must(uuid.NewV7()),
		WalletID:              uuid.Must(uuid.NewV7()),
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Amount:                brl(t, amount),
		CorrelationID:         "correlation-1",
	}
}

func TestNewExternal(t *testing.T) {
	t.Run("Given a valid operation/When it is created/Then it starts as PENDING with its metadata", func(t *testing.T) {
		// Given
		input := command(t, wagering.Bet, "25.00")

		// When
		operation, err := wagering.NewExternal(input, uuid.Must(uuid.NewV7()), now)

		// Then
		if err != nil {
			t.Fatalf("NewExternal failed: %v", err)
		}
		if operation.Status() != wagering.Pending {
			t.Errorf("status = %s, want PENDING", operation.Status())
		}
		if operation.HasResultBalance() || !operation.CompletedAt().IsZero() {
			t.Error("a new operation must not carry a result yet")
		}
		if operation.ProviderID() != "provider-a" || operation.PayloadHash() != hash {
			t.Error("provider metadata should be preserved")
		}
	})

	t.Run("Given the reference policy per kind/When an operation is created/Then only the allowed shape is accepted", func(t *testing.T) {
		scenarios := []struct {
			name      string
			kind      wagering.Kind
			amount    string
			reference string
			want      error
		}{
			{"bet without reference", wagering.Bet, "25.00", "", nil},
			{"bet with reference", wagering.Bet, "25.00", "transaction-100", wagering.ErrReferenceForbidden},
			{"loss with zero", wagering.Loss, "0.00", "", nil},
			{"loss with amount", wagering.Loss, "5.00", "", wagering.ErrInvalidAmount},
			{"loss with reference", wagering.Loss, "0.00", "transaction-100", wagering.ErrReferenceForbidden},
			{"win without reference", wagering.Win, "100.00", "", nil},
			{"win with reference", wagering.Win, "100.00", "transaction-100", nil},
			{"win with zero", wagering.Win, "0.00", "", wagering.ErrInvalidAmount},
			{"refund with reference", wagering.Refund, "25.00", "transaction-100", nil},
			{"refund without reference", wagering.Refund, "25.00", "", wagering.ErrReferenceRequired},
			{"rollback without reference", wagering.Rollback, "25.00", "", wagering.ErrReferenceRequired},
			{"bet with zero", wagering.Bet, "0.00", "", wagering.ErrInvalidAmount},
			{"opening is not external", wagering.Opening, "25.00", "", wagering.ErrInvalidKind},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				input := command(t, scenario.kind, scenario.amount)
				input.ReferenceExternalID = scenario.reference

				// When
				operation, err := wagering.NewExternal(input, uuid.Must(uuid.NewV7()), now)

				// Then
				if scenario.want == nil {
					if err != nil {
						t.Fatalf("expected the operation to be accepted, got %v", err)
					}
					if operation.ReferenceExternalID() != scenario.reference {
						t.Errorf("reference = %q, want %q", operation.ReferenceExternalID(), scenario.reference)
					}
					return
				}
				if !errors.Is(err, scenario.want) {
					t.Fatalf("error = %v, want %v", err, scenario.want)
				}
				if operation != nil {
					t.Error("no operation should be returned on failure")
				}
			})
		}
	})

	t.Run("Given missing metadata/When an operation is created/Then it is rejected", func(t *testing.T) {
		scenarios := []struct {
			name   string
			mutate func(*wagering.Command)
			want   error
		}{
			{"provider missing", func(c *wagering.Command) { c.ProviderID = "" }, wagering.ErrMissingField},
			{"external id missing", func(c *wagering.Command) { c.ExternalTransactionID = "" }, wagering.ErrMissingField},
			{"idempotency key missing", func(c *wagering.Command) { c.IdempotencyKey = "" }, wagering.ErrMissingField},
			{"round missing", func(c *wagering.Command) { c.RoundID = "" }, wagering.ErrMissingField},
			{"game missing", func(c *wagering.Command) { c.GameID = "" }, wagering.ErrMissingField},
			{"correlation missing", func(c *wagering.Command) { c.CorrelationID = "" }, wagering.ErrMissingField},
			{"hash not hexadecimal", func(c *wagering.Command) { c.PayloadHash = "not-a-hash" }, wagering.ErrInvalidPayloadHash},
			{"hash uppercase", func(c *wagering.Command) { c.PayloadHash = strings.ToUpper(hash) }, wagering.ErrInvalidPayloadHash},
			{"wallet missing", func(c *wagering.Command) { c.WalletID = uuid.Nil }, wagering.ErrMissingIdentifier},
			{"player missing", func(c *wagering.Command) { c.PlayerID = uuid.Nil }, wagering.ErrMissingIdentifier},
			{"internal origin", func(c *wagering.Command) { c.Origin = wagering.Internal }, wagering.ErrInvalidOrigin},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				input := command(t, wagering.Bet, "25.00")
				scenario.mutate(&input)

				// When
				_, err := wagering.NewExternal(input, uuid.Must(uuid.NewV7()), now)

				// Then
				if !errors.Is(err, scenario.want) {
					t.Errorf("error = %v, want %v", err, scenario.want)
				}
			})
		}
	})
}

func TestNewOpening(t *testing.T) {
	t.Run("Given an internal opening/When it is created/Then it is born PROCESSED without provider metadata", func(t *testing.T) {
		// Given
		walletID := uuid.Must(uuid.NewV7())
		playerID := uuid.Must(uuid.NewV7())

		// When
		operation, err := wagering.NewOpening(uuid.Must(uuid.NewV7()), walletID, playerID, brl(t, "1000.00"), "correlation-1", now)

		// Then
		if err != nil {
			t.Fatalf("NewOpening failed: %v", err)
		}
		if operation.Status() != wagering.Processed || operation.Origin() != wagering.Internal {
			t.Errorf("opening = %s/%s, want PROCESSED/INTERNAL", operation.Status(), operation.Origin())
		}
		if operation.ProviderID() != "" || operation.ExternalTransactionID() != "" || operation.RoundID() != "" {
			t.Error("an opening must not carry provider metadata")
		}
		if operation.ResultBalance().String() != "1000.00" || operation.CompletedAt().IsZero() {
			t.Error("an opening should settle with the initial balance")
		}
	})

	t.Run("Given a zero or invalid amount/When the opening is created/Then it is rejected", func(t *testing.T) {
		// Given, When
		_, zeroErr := wagering.NewOpening(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), brl(t, "0.00"), "c", now)
		_, currencyErr := wagering.NewOpening(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), money.Money{}, "c", now)

		// Then
		if !errors.Is(zeroErr, wagering.ErrInvalidAmount) || !errors.Is(currencyErr, wagering.ErrInvalidAmount) {
			t.Errorf("errors = %v / %v, want ErrInvalidAmount", zeroErr, currencyErr)
		}
	})
}

func TestTransitions(t *testing.T) {
	newPending := func(t *testing.T, kind wagering.Kind, amount string) *wagering.Transaction {
		t.Helper()
		input := command(t, kind, amount)
		if kind.RequiresReference() {
			input.ReferenceExternalID = "transaction-100"
		}
		operation, err := wagering.NewExternal(input, uuid.Must(uuid.NewV7()), now)
		if err != nil {
			t.Fatalf("NewExternal failed: %v", err)
		}
		return operation
	}

	t.Run("Given a pending operation/When it settles/Then the status and the result are recorded", func(t *testing.T) {
		// Given
		operation := newPending(t, wagering.Bet, "25.00")

		// When
		err := operation.MarkProcessed(brl(t, "975.00"), uuid.Nil, now)

		// Then
		if err != nil {
			t.Fatalf("MarkProcessed failed: %v", err)
		}
		if operation.Status() != wagering.Processed || operation.ResultBalance().String() != "975.00" {
			t.Errorf("operation = %s with %s", operation.Status(), operation.ResultBalance().String())
		}
		if operation.CompletedAt().IsZero() {
			t.Error("a settled operation should carry completedAt")
		}
	})

	t.Run("Given a terminal operation/When a transition is attempted/Then it is refused", func(t *testing.T) {
		scenarios := []struct {
			name   string
			settle func(*wagering.Transaction) error
		}{
			{"processed", func(o *wagering.Transaction) error {
				return o.MarkProcessed(brl(t, "1.00"), uuid.Nil, now)
			}},
			{"rejected", func(o *wagering.Transaction) error {
				return o.MarkRejected(wagering.InsufficientFunds, brl(t, "1.00"), now)
			}},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				operation := newPending(t, wagering.Bet, "25.00")
				if err := scenario.settle(operation); err != nil {
					t.Fatalf("settle failed: %v", err)
				}
				statusBefore := operation.Status()

				// When
				processedErr := operation.MarkProcessed(brl(t, "2.00"), uuid.Nil, now)
				rejectedErr := operation.MarkRejected(wagering.InsufficientFunds, brl(t, "2.00"), now)
				failedErr := operation.MarkFailed(now)
				pendingErr := operation.MarkPendingReference(now, now.Add(time.Minute), now)

				// Then
				for name, err := range map[string]error{
					"MarkProcessed": processedErr, "MarkRejected": rejectedErr,
					"MarkFailed": failedErr, "MarkPendingReference": pendingErr,
				} {
					if !errors.Is(err, wagering.ErrTransitionNotAllowed) {
						t.Errorf("%s error = %v, want ErrTransitionNotAllowed", name, err)
					}
				}
				if operation.Status() != statusBefore {
					t.Errorf("status changed to %s", operation.Status())
				}
			})
		}
	})

	t.Run("Given a reversal waiting for its reference/When it is rescheduled/Then attempts grow and the status holds", func(t *testing.T) {
		// Given
		operation := newPending(t, wagering.Rollback, "25.00")
		deadline := now.Add(15 * time.Minute)
		if err := operation.MarkPendingReference(now.Add(time.Second), deadline, now); err != nil {
			t.Fatalf("MarkPendingReference failed: %v", err)
		}

		// When
		first := operation.Reschedule(now.Add(2*time.Second), now)
		second := operation.Reschedule(now.Add(4*time.Second), now)

		// Then
		if first != nil || second != nil {
			t.Fatalf("Reschedule failed: %v / %v", first, second)
		}
		if operation.Status() != wagering.PendingReference || operation.ReferenceAttempts() != 2 {
			t.Errorf("operation = %s with %d attempts, want PENDING_REFERENCE with 2", operation.Status(), operation.ReferenceAttempts())
		}
		if !operation.ReferenceDeadlineAt().Equal(deadline) {
			t.Error("the deadline should not move when rescheduling")
		}
	})

	t.Run("Given a reversal waiting for its reference/When the worker gives up/Then only that path reaches FAILED", func(t *testing.T) {
		// Given
		pending := newPending(t, wagering.Rollback, "25.00")
		if err := pending.MarkPendingReference(now, now.Add(time.Minute), now); err != nil {
			t.Fatalf("MarkPendingReference failed: %v", err)
		}
		fresh := newPending(t, wagering.Bet, "25.00")

		// When
		pendingErr := pending.MarkFailed(now)
		freshErr := fresh.MarkFailed(now)

		// Then
		if pendingErr != nil {
			t.Fatalf("MarkFailed on a pending reference failed: %v", pendingErr)
		}
		if pending.Status() != wagering.Failed || pending.FailureCode() != wagering.PermanentFailure {
			t.Errorf("operation = %s/%s, want FAILED/PERMANENT_FAILURE", pending.Status(), pending.FailureCode())
		}
		if pending.HasResultBalance() {
			t.Error("FAILED must not carry a result balance")
		}
		if !errors.Is(freshErr, wagering.ErrTransitionNotAllowed) {
			t.Errorf("PENDING to FAILED error = %v, want ErrTransitionNotAllowed", freshErr)
		}
	})

	t.Run("Given a settled reversal/When PERMANENT_FAILURE is used as a rejection/Then it is refused", func(t *testing.T) {
		// Given
		operation := newPending(t, wagering.Bet, "25.00")

		// When
		err := operation.MarkRejected(wagering.PermanentFailure, brl(t, "10.00"), now)

		// Then
		if !errors.Is(err, wagering.ErrInvalidFailureCode) {
			t.Errorf("error = %v, want ErrInvalidFailureCode", err)
		}
	})

	t.Run("Given a reversal/When it is processed without a resolved reference/Then it is refused", func(t *testing.T) {
		// Given
		operation := newPending(t, wagering.Refund, "25.00")

		// When
		err := operation.MarkProcessed(brl(t, "1000.00"), uuid.Nil, now)

		// Then
		if !errors.Is(err, wagering.ErrReferenceRequired) {
			t.Errorf("error = %v, want ErrReferenceRequired", err)
		}
	})
}

func TestSettlementInvariants(t *testing.T) {
	for _, scenario := range []string{"different currency", "forbidden reference", "self reference"} {
		t.Run("Given "+scenario+"/When processing is requested/Then the entity rejects it without mutation", func(t *testing.T) {
			// Given
			operation, err := wagering.NewExternal(command(t, wagering.Bet, "25.00"), uuid.New(), now)
			if err != nil {
				t.Fatal(err)
			}
			balance := brl(t, "75.00")
			reference := uuid.Nil
			switch scenario {
			case "different currency":
				balance, err = money.Parse("75.00", "USD")
				if err != nil {
					t.Fatal(err)
				}
			case "forbidden reference":
				reference = uuid.New()
			case "self reference":
				reference = operation.ID()
			}
			// When
			err = operation.MarkProcessed(balance, reference, now)
			// Then
			if err == nil || operation.Status() != wagering.Pending || operation.HasResultBalance() {
				t.Fatal("invalid transition mutated the operation")
			}
		})
	}
}
