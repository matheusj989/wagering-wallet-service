package dto_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

func brl(t *testing.T, amount string) money.Money {
	t.Helper()

	value, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", amount, err)
	}
	return value
}

func validInput(t *testing.T) dto.ProcessWagerInput {
	t.Helper()

	return dto.ProcessWagerInput{
		Origin:                wagering.HTTP,
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-1",
		IdempotencyKey:        "provider-a:transaction-1",
		PlayerID:              uuid.Must(uuid.NewV7()),
		WalletID:              uuid.Must(uuid.NewV7()),
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  wagering.Bet,
		Amount:                brl(t, "25.00"),
		CorrelationID:         "correlation-1",
	}
}

func fieldsOf(t *testing.T, err error) []string {
	t.Helper()

	var problems *validation.Error
	if !errors.As(err, &problems) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	names := make([]string, 0, len(problems.Fields))
	for _, field := range problems.Fields {
		names = append(names, field.Field)
	}
	return names
}

func TestProcessWagerInput(t *testing.T) {
	t.Run("Given a complete input/When it is validated/Then it is accepted", func(t *testing.T) {
		// Given, When
		err := validInput(t).Validate()

		// Then
		if err != nil {
			t.Fatalf("the input should be accepted, got %v", err)
		}
	})

	t.Run("Given a field outside the contract/When it is validated/Then the answer names that field", func(t *testing.T) {
		scenarios := []struct {
			name   string
			mutate func(*dto.ProcessWagerInput)
			field  string
		}{
			{"provider missing", func(i *dto.ProcessWagerInput) { i.ProviderID = "" }, "providerId"},
			{"provider padded with spaces", func(i *dto.ProcessWagerInput) { i.ProviderID = " provider-a " }, "providerId"},
			{"provider with a control character", func(i *dto.ProcessWagerInput) { i.ProviderID = "provider\na" }, "providerId"},
			{"provider too long", func(i *dto.ProcessWagerInput) { i.ProviderID = strings.Repeat("a", 256) }, "providerId"},
			{"external id missing", func(i *dto.ProcessWagerInput) { i.ExternalTransactionID = "" }, "externalTransactionId"},
			{"player missing", func(i *dto.ProcessWagerInput) { i.PlayerID = uuid.Nil }, "playerId"},
			{"wallet missing", func(i *dto.ProcessWagerInput) { i.WalletID = uuid.Nil }, "walletId"},
			{"round missing", func(i *dto.ProcessWagerInput) { i.RoundID = "" }, "roundId"},
			{"game missing", func(i *dto.ProcessWagerInput) { i.GameID = "" }, "gameId"},
			{"kind unknown", func(i *dto.ProcessWagerInput) { i.Kind = "TRANSFER" }, "kind"},
			{"key too long", func(i *dto.ProcessWagerInput) { i.IdempotencyKey = strings.Repeat("k", 256) }, "Idempotency-Key"},
			{"reference too long", func(i *dto.ProcessWagerInput) {
				i.Kind = wagering.Refund
				i.ReferenceExternalID = strings.Repeat("r", 256)
			}, "referenceExternalTransactionId"},
			{"money not decoded", func(i *dto.ProcessWagerInput) { i.Amount = money.Money{} }, "money.amount"},
			{"bet with no money", func(i *dto.ProcessWagerInput) { i.Amount = brl(t, "0.00") }, "money.amount"},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				input := validInput(t)
				scenario.mutate(&input)

				// When
				err := input.Validate()

				// Then
				fields := fieldsOf(t, err)
				found := false
				for _, field := range fields {
					if field == scenario.field {
						found = true
					}
				}
				if !found {
					t.Errorf("reported fields = %v, want %s among them", fields, scenario.field)
				}
			})
		}
	})

	t.Run("Given a message from the queue/When the key is missing/Then the field is named as the queue names it", func(t *testing.T) {
		// Given
		input := validInput(t)
		input.Origin = wagering.Queue
		input.IdempotencyKey = ""

		// When
		err := input.Validate()

		// Then
		if fields := fieldsOf(t, err); len(fields) != 1 || fields[0] != "data.idempotencyKey" {
			t.Errorf("reported fields = %v, want data.idempotencyKey", fields)
		}
	})

	t.Run("Given a message id/When the origin is checked/Then it is treated as coming from the queue", func(t *testing.T) {
		// Given
		input := validInput(t)

		// When
		before := input.FromQueue()
		input.MessageID = "message-1"

		// Then
		if before || !input.FromQueue() {
			t.Errorf("fromQueue = %v before and %v after the message id", before, input.FromQueue())
		}
	})
}

func TestOpenWalletInput(t *testing.T) {
	t.Run("Given an opening outside the contract/When it is validated/Then the field is reported", func(t *testing.T) {
		scenarios := []struct {
			name  string
			input dto.OpenWalletInput
			field string
		}{
			{"player missing", dto.OpenWalletInput{InitialBalance: brl(t, "10.00")}, "playerId"},
			{"balance not decoded", dto.OpenWalletInput{PlayerID: uuid.Must(uuid.NewV7())}, "initialBalance.amount"},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given, When
				err := scenario.input.Validate()

				// Then
				if fields := fieldsOf(t, err); len(fields) == 0 || fields[0] != scenario.field {
					t.Errorf("reported fields = %v, want %s", fields, scenario.field)
				}
			})
		}
	})
}
