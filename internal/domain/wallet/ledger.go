package wallet

import (
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
)

type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

func (d Direction) Valid() bool {
	return d == Debit || d == Credit
}

func (d Direction) String() string {
	return string(d)
}

type LedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	walletVersion int64
	createdAt     time.Time
}

func NewLedgerEntry(
	id uuid.UUID,
	walletID uuid.UUID,
	transactionID uuid.UUID,
	direction Direction,
	amount money.Money,
	balanceBefore money.Money,
	balanceAfter money.Money,
	walletVersion int64,
	createdAt time.Time,
) (LedgerEntry, error) {
	if id == uuid.Nil || walletID == uuid.Nil || transactionID == uuid.Nil {
		return LedgerEntry{}, ErrMissingIdentifier
	}
	if !direction.Valid() {
		return LedgerEntry{}, ErrInvalidDirection
	}
	if !amount.Valid() || !amount.IsPositive() {
		return LedgerEntry{}, ErrInvalidAmount
	}
	if !balanceBefore.Valid() || !balanceAfter.Valid() {
		return LedgerEntry{}, money.ErrInvalidCurrency
	}
	if balanceBefore.Currency() != amount.Currency() || balanceAfter.Currency() != amount.Currency() {
		return LedgerEntry{}, money.ErrCurrencyMismatch
	}
	if balanceBefore.IsNegative() || balanceAfter.IsNegative() {
		return LedgerEntry{}, ErrNegativeBalance
	}
	if walletVersion < 1 {
		return LedgerEntry{}, ErrInvalidWalletVersion
	}
	if createdAt.IsZero() {
		return LedgerEntry{}, ErrMissingTimestamp
	}

	expected, err := applyDirection(balanceBefore, direction, amount)
	if err != nil {
		return LedgerEntry{}, err
	}
	if !expected.Equal(balanceAfter) {
		return LedgerEntry{}, ErrInconsistentLedgerEntry
	}

	return LedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		walletVersion: walletVersion,
		createdAt:     createdAt.UTC(),
	}, nil
}

func (e LedgerEntry) ID() uuid.UUID            { return e.id }
func (e LedgerEntry) WalletID() uuid.UUID      { return e.walletID }
func (e LedgerEntry) TransactionID() uuid.UUID { return e.transactionID }
func (e LedgerEntry) Direction() Direction     { return e.direction }
func (e LedgerEntry) Amount() money.Money      { return e.amount }
func (e LedgerEntry) BalanceBefore() money.Money {
	return e.balanceBefore
}
func (e LedgerEntry) BalanceAfter() money.Money { return e.balanceAfter }
func (e LedgerEntry) WalletVersion() int64      { return e.walletVersion }
func (e LedgerEntry) CreatedAt() time.Time      { return e.createdAt }

func applyDirection(balance money.Money, direction Direction, amount money.Money) (money.Money, error) {
	if direction == Credit {
		return balance.Add(amount)
	}
	return balance.Sub(amount)
}

type LedgerTotals struct {
	Balance money.Money
	Entries int64
}
