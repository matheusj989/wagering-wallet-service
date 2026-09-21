package money_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
)

func mustParse(t *testing.T, amount string, currency string) money.Money {
	t.Helper()
	value, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("Parse(%q, %q) failed: %v", amount, currency, err)
	}
	return value
}

func TestParse(t *testing.T) {
	t.Run("Given a canonical amount/When it is parsed/Then value and currency are exact", func(t *testing.T) {
		scenarios := []struct {
			name     string
			amount   string
			currency string
			minor    int64
		}{
			{"zero", "0.00", "BRL", 0},
			{"cents only", "0.07", "BRL", 7},
			{"whole units", "25.00", "BRL", 2500},
			{"dollars", "25.00", "USD", 2500},
			{"largest representable", "92233720368547758.07", "BRL", math.MaxInt64},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given, When
				value, err := money.Parse(scenario.amount, scenario.currency)

				// Then
				if err != nil {
					t.Fatalf("expected %q to parse, got %v", scenario.amount, err)
				}
				if value.Minor() != scenario.minor {
					t.Errorf("Minor() = %d, want %d", value.Minor(), scenario.minor)
				}
				if string(value.Currency()) != scenario.currency {
					t.Errorf("Currency() = %q, want %q", value.Currency(), scenario.currency)
				}
				if value.String() != scenario.amount {
					t.Errorf("String() = %q, want %q", value.String(), scenario.amount)
				}
			})
		}
	})

	t.Run("Given an amount outside the canonical form/When it is parsed/Then it is rejected without rounding", func(t *testing.T) {
		amounts := []string{
			"", "25", "25.0", "25.000", "025.00", "-1.00", "+1.00", "1e2", "1E2",
			"NaN", "Infinity", " 25.00", "25.00 ", "25,00", ".50", "25.", "1_000.00",
			"92233720368547758.08", "99999999999999999.99",
		}

		for _, amount := range amounts {
			t.Run(amount, func(t *testing.T) {
				// Given, When
				_, err := money.Parse(amount, "BRL")

				// Then
				if err == nil {
					t.Fatalf("expected %q to be rejected", amount)
				}
				if !errors.Is(err, money.ErrInvalidAmount) && !errors.Is(err, money.ErrOverflow) {
					t.Errorf("error for %q = %v, want ErrInvalidAmount or ErrOverflow", amount, err)
				}
			})
		}
	})

	t.Run("Given a currency outside the supported list/When it is parsed/Then it is rejected", func(t *testing.T) {
		for _, currency := range []string{"brl", "BRLX", "ABC", "JPY", "", "BR"} {
			t.Run(currency, func(t *testing.T) {
				// Given, When
				_, err := money.Parse("25.00", currency)

				// Then
				if !errors.Is(err, money.ErrInvalidCurrency) {
					t.Errorf("error for %q = %v, want ErrInvalidCurrency", currency, err)
				}
			})
		}
	})
}

func TestArithmetic(t *testing.T) {
	t.Run("Given two amounts in the same currency/When they are added or subtracted/Then the result is exact", func(t *testing.T) {
		// Given
		left := mustParse(t, "20.00", "BRL")
		right := mustParse(t, "80.00", "BRL")

		// When
		sum, sumErr := left.Add(right)
		difference, differenceErr := left.Sub(right)

		// Then
		if sumErr != nil || differenceErr != nil {
			t.Fatalf("unexpected errors: %v / %v", sumErr, differenceErr)
		}
		if sum.String() != "100.00" {
			t.Errorf("Add = %q, want \"100.00\"", sum.String())
		}
		if difference.String() != "-60.00" || !difference.IsNegative() {
			t.Errorf("Sub = %q (negative %v), want \"-60.00\" and true", difference.String(), difference.IsNegative())
		}
	})

	t.Run("Given amounts in different currencies/When they are combined/Then the currency mismatch is reported", func(t *testing.T) {
		// Given
		real := mustParse(t, "10.00", "BRL")
		dollar := mustParse(t, "10.00", "USD")

		// When
		_, addErr := real.Add(dollar)
		_, subErr := real.Sub(dollar)
		_, cmpErr := real.Cmp(dollar)

		// Then
		for name, err := range map[string]error{"Add": addErr, "Sub": subErr, "Cmp": cmpErr} {
			if !errors.Is(err, money.ErrCurrencyMismatch) {
				t.Errorf("%s error = %v, want ErrCurrencyMismatch", name, err)
			}
		}
	})

	t.Run("Given the uninitialised zero value/When it is used/Then every operation rejects it", func(t *testing.T) {
		// Given
		var uninitialised money.Money
		valid := mustParse(t, "1.00", "BRL")

		// When, Then
		if uninitialised.Valid() {
			t.Error("zero value should not be valid")
		}
		if _, err := uninitialised.Add(valid); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Errorf("Add error = %v, want ErrInvalidCurrency", err)
		}
		if _, err := valid.Sub(uninitialised); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Errorf("Sub error = %v, want ErrInvalidCurrency", err)
		}
		if _, err := uninitialised.Neg(); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Errorf("Neg error = %v, want ErrInvalidCurrency", err)
		}
		if _, err := uninitialised.MarshalJSON(); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Errorf("MarshalJSON error = %v, want ErrInvalidCurrency", err)
		}
	})

	t.Run("Given results outside int64 minor units/When they are computed/Then overflow is reported", func(t *testing.T) {
		// Given
		maximum := mustParse(t, "92233720368547758.07", "BRL")
		cent := mustParse(t, "0.01", "BRL")
		minimum, err := money.FromMinor(math.MinInt64, money.BRL)
		if err != nil {
			t.Fatalf("FromMinor failed: %v", err)
		}

		// When, Then
		if _, err := maximum.Add(maximum); !errors.Is(err, money.ErrOverflow) {
			t.Errorf("Add overflow = %v, want ErrOverflow", err)
		}
		if _, err := maximum.Add(cent); !errors.Is(err, money.ErrOverflow) {
			t.Errorf("Add one cent past the limit = %v, want ErrOverflow", err)
		}
		if _, err := minimum.Sub(cent); !errors.Is(err, money.ErrOverflow) {
			t.Errorf("Sub overflow = %v, want ErrOverflow", err)
		}
		if _, err := minimum.Neg(); !errors.Is(err, money.ErrOverflow) {
			t.Errorf("Neg of the smallest value = %v, want ErrOverflow", err)
		}
	})

	t.Run("Given the smallest representable value/When it is formatted/Then the sign and digits are preserved", func(t *testing.T) {
		// Given
		minimum, err := money.FromMinor(math.MinInt64, money.BRL)
		if err != nil {
			t.Fatalf("FromMinor failed: %v", err)
		}

		// When
		formatted := minimum.String()

		// Then
		if formatted != "-92233720368547758.08" {
			t.Errorf("String() = %q, want \"-92233720368547758.08\"", formatted)
		}
	})

	t.Run("Given two amounts/When they are compared/Then the ordering is reported", func(t *testing.T) {
		// Given
		smaller := mustParse(t, "20.00", "BRL")
		larger := mustParse(t, "80.00", "BRL")

		// When
		below, belowErr := smaller.Cmp(larger)
		above, aboveErr := larger.Cmp(smaller)
		same, sameErr := smaller.Cmp(mustParse(t, "20.00", "BRL"))

		// Then
		if belowErr != nil || aboveErr != nil || sameErr != nil {
			t.Fatalf("unexpected errors: %v / %v / %v", belowErr, aboveErr, sameErr)
		}
		if below != -1 || above != 1 || same != 0 {
			t.Errorf("Cmp results = %d, %d, %d, want -1, 1, 0", below, above, same)
		}
	})
}

func TestImmutability(t *testing.T) {
	t.Run("Given two valid amounts/When operations succeed or fail/Then the operands keep their values", func(t *testing.T) {
		// Given
		left := mustParse(t, "20.00", "BRL")
		right := mustParse(t, "80.00", "BRL")
		maximum := mustParse(t, "92233720368547758.07", "BRL")
		dollar := mustParse(t, "1.00", "USD")

		// When
		_, _ = left.Add(right)
		_, _ = left.Sub(right)
		_, _ = left.Neg()
		_, _ = left.Cmp(right)
		_, _ = maximum.Add(maximum)
		_, _ = left.Add(dollar)

		// Then
		if left.Minor() != 2000 || left.Currency() != money.BRL {
			t.Errorf("left changed to %s %s", left.String(), left.Currency())
		}
		if right.Minor() != 8000 || right.Currency() != money.BRL {
			t.Errorf("right changed to %s %s", right.String(), right.Currency())
		}
		if maximum.Minor() != math.MaxInt64 {
			t.Errorf("maximum changed to %s", maximum.String())
		}
	})
}

func TestJSON(t *testing.T) {
	t.Run("Given an amount/When it is serialised/Then amount is a string in canonical form", func(t *testing.T) {
		// Given
		value := mustParse(t, "975.00", "BRL")

		// When
		encoded, err := json.Marshal(value)

		// Then
		if err != nil {
			t.Fatalf("Marshal failed: %v", err)
		}
		if string(encoded) != `{"amount":"975.00","currency":"BRL"}` {
			t.Errorf("Marshal = %s, want {\"amount\":\"975.00\",\"currency\":\"BRL\"}", encoded)
		}
	})

	t.Run("Given a valid document/When it is decoded/Then the amount round-trips", func(t *testing.T) {
		// Given
		document := []byte(`{"amount":"25.00","currency":"USD"}`)

		// When
		var value money.Money
		err := json.Unmarshal(document, &value)

		// Then
		if err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}
		if value.Minor() != 2500 || value.Currency() != money.USD {
			t.Errorf("decoded %s %s, want 25.00 USD", value.String(), value.Currency())
		}
	})

	t.Run("Given an unusable document/When it is decoded/Then it is rejected", func(t *testing.T) {
		scenarios := []struct {
			name     string
			document string
		}{
			{"amount as number", `{"amount":25.00,"currency":"BRL"}`},
			{"amount missing", `{"currency":"BRL"}`},
			{"amount null", `{"amount":null,"currency":"BRL"}`},
			{"amount not canonical", `{"amount":"25","currency":"BRL"}`},
			{"currency missing", `{"amount":"25.00"}`},
			{"currency unsupported", `{"amount":"25.00","currency":"JPY"}`},
			{"unknown field", `{"amount":"25.00","currency":"BRL","scale":2}`},
			{"not an object", `"25.00"`},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given, When
				var value money.Money
				err := json.Unmarshal([]byte(scenario.document), &value)

				// Then
				if err == nil {
					t.Fatalf("expected %s to be rejected, got %s", scenario.document, value.String())
				}
				if value.Valid() {
					t.Errorf("value should stay unset after a failed decode, got %s", value.String())
				}
			})
		}
	})
}
