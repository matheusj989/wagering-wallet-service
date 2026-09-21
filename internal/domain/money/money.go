package money

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrInvalidAmount    = errors.New("money: amount must be a decimal string with exactly two places")
	ErrInvalidCurrency  = errors.New("money: currency is not supported")
	ErrCurrencyMismatch = errors.New("money: operands must share the same currency")
	ErrOverflow         = errors.New("money: result does not fit in int64 minor units")
	ErrNegative         = errors.New("money: amount must not be negative")
)

type Currency string

const (
	BRL Currency = "BRL"
	USD Currency = "USD"
)

var supportedCurrencies = map[Currency]struct{}{BRL: {}, USD: {}}

func ParseCurrency(value string) (Currency, error) {
	currency := Currency(value)
	if _, ok := supportedCurrencies[currency]; !ok {
		return "", ErrInvalidCurrency
	}
	return currency, nil
}

func (c Currency) Supported() bool {
	_, ok := supportedCurrencies[c]
	return ok
}

func (c Currency) String() string {
	return string(c)
}

var canonicalAmount = regexp.MustCompile(`^(0|[1-9][0-9]{0,16})\.[0-9]{2}$`)

type Money struct {
	minor    int64
	currency Currency
}

func Parse(amount string, currency string) (Money, error) {
	parsedCurrency, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	minor, err := parseMinorUnits(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: parsedCurrency}, nil
}

func FromMinor(minor int64, currency Currency) (Money, error) {
	if !currency.Supported() {
		return Money{}, ErrInvalidCurrency
	}
	return Money{minor: minor, currency: currency}, nil
}

func Zero(currency Currency) (Money, error) {
	return FromMinor(0, currency)
}

func (m Money) Minor() int64 {
	return m.minor
}

func (m Money) Currency() Currency {
	return m.currency
}

func (m Money) IsZero() bool {
	return m.minor == 0
}

func (m Money) IsNegative() bool {
	return m.minor < 0
}

func (m Money) IsPositive() bool {
	return m.minor > 0
}

func (m Money) Valid() bool {
	return m.currency.Supported()
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.comparableWith(other); err != nil {
		return Money{}, err
	}
	sum, err := addExact(m.minor, other.minor)
	if err != nil {
		return Money{}, err
	}
	return Money{minor: sum, currency: m.currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if err := m.comparableWith(other); err != nil {
		return Money{}, err
	}
	difference, err := subExact(m.minor, other.minor)
	if err != nil {
		return Money{}, err
	}
	return Money{minor: difference, currency: m.currency}, nil
}

func (m Money) Neg() (Money, error) {
	if !m.Valid() {
		return Money{}, ErrInvalidCurrency
	}
	if m.minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

func (m Money) Cmp(other Money) (int, error) {
	if err := m.comparableWith(other); err != nil {
		return 0, err
	}
	switch {
	case m.minor < other.minor:
		return -1, nil
	case m.minor > other.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) Equal(other Money) bool {
	return m.minor == other.minor && m.currency == other.currency
}

func (m Money) String() string {
	units, cents := splitMinorUnits(m.minor)
	var builder strings.Builder
	if m.minor < 0 {
		builder.WriteByte('-')
	}
	builder.WriteString(strconv.FormatUint(units, 10))
	builder.WriteByte('.')
	if cents < 10 {
		builder.WriteByte('0')
	}
	builder.WriteString(strconv.FormatUint(cents, 10))
	return builder.String()
}

func (m Money) MarshalJSON() ([]byte, error) {
	if !m.Valid() {
		return nil, ErrInvalidCurrency
	}
	amount, err := json.Marshal(m.String())
	if err != nil {
		return nil, err
	}
	currency, err := json.Marshal(string(m.currency))
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	buffer.WriteString(`{"amount":`)
	buffer.Write(amount)
	buffer.WriteString(`,"currency":`)
	buffer.Write(currency)
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func (m *Money) UnmarshalJSON(data []byte) error {
	var wire struct {
		Amount   json.RawMessage `json:"amount"`
		Currency string          `json:"currency"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return ErrInvalidAmount
	}

	var amount string
	if err := json.Unmarshal(wire.Amount, &amount); err != nil {
		return ErrInvalidAmount
	}

	parsed, err := Parse(amount, wire.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

func (m Money) comparableWith(other Money) error {
	if !m.Valid() || !other.Valid() {
		return ErrInvalidCurrency
	}
	if m.currency != other.currency {
		return ErrCurrencyMismatch
	}
	return nil
}

func parseMinorUnits(amount string) (int64, error) {
	if !canonicalAmount.MatchString(amount) {
		return 0, ErrInvalidAmount
	}
	whole, fraction, _ := strings.Cut(amount, ".")

	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, ErrOverflow
	}
	cents, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, ErrInvalidAmount
	}
	if units > (math.MaxInt64-cents)/100 {
		return 0, ErrOverflow
	}
	return units*100 + cents, nil
}

func splitMinorUnits(minor int64) (units uint64, cents uint64) {
	magnitude := uint64(minor)
	if minor < 0 {
		magnitude = uint64(-(minor + 1)) + 1
	}
	return magnitude / 100, magnitude % 100
}

func addExact(a, b int64) (int64, error) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func subExact(a, b int64) (int64, error) {
	difference := a - b
	if (b < 0 && difference < a) || (b > 0 && difference > a) {
		return 0, ErrOverflow
	}
	return difference, nil
}
