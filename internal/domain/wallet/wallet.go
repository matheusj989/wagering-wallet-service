package wallet

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
)

var (
	ErrMissingIdentifier       = errors.New("wallet: identifier must not be empty")
	ErrMissingTimestamp        = errors.New("wallet: timestamp must not be empty")
	ErrInvalidAmount           = errors.New("wallet: amount must be positive")
	ErrNegativeBalance         = errors.New("wallet: balance must not be negative")
	ErrInsufficientFunds       = errors.New("wallet: balance is lower than the requested debit")
	ErrInvalidDirection        = errors.New("wallet: direction must be DEBIT or CREDIT")
	ErrInvalidWalletVersion    = errors.New("wallet: version must be one or greater")
	ErrInconsistentLedgerEntry = errors.New("wallet: entry does not match balance before and after")
)

const initialVersion int64 = 1

type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func Open(
	id uuid.UUID,
	playerID uuid.UUID,
	initialBalance money.Money,
	openingTransactionID uuid.UUID,
	openingEntryID uuid.UUID,
	now time.Time,
) (*Wallet, *LedgerEntry, error) {
	if id == uuid.Nil || playerID == uuid.Nil {
		return nil, nil, ErrMissingIdentifier
	}
	if !initialBalance.Valid() {
		return nil, nil, money.ErrInvalidCurrency
	}
	if initialBalance.IsNegative() {
		return nil, nil, ErrNegativeBalance
	}
	if now.IsZero() {
		return nil, nil, ErrMissingTimestamp
	}

	opened := &Wallet{
		id:        id,
		playerID:  playerID,
		balance:   initialBalance,
		version:   initialVersion,
		createdAt: now.UTC(),
		updatedAt: now.UTC(),
	}

	if initialBalance.IsZero() {
		return opened, nil, nil
	}

	zero, err := money.Zero(initialBalance.Currency())
	if err != nil {
		return nil, nil, err
	}
	entry, err := NewLedgerEntry(
		openingEntryID,
		id,
		openingTransactionID,
		Credit,
		initialBalance,
		zero,
		initialBalance,
		initialVersion,
		now,
	)
	if err != nil {
		return nil, nil, err
	}
	return opened, &entry, nil
}

func Rehydrate(
	id uuid.UUID,
	playerID uuid.UUID,
	balance money.Money,
	version int64,
	createdAt time.Time,
	updatedAt time.Time,
) (*Wallet, error) {
	if id == uuid.Nil || playerID == uuid.Nil {
		return nil, ErrMissingIdentifier
	}
	if !balance.Valid() {
		return nil, money.ErrInvalidCurrency
	}
	if balance.IsNegative() {
		return nil, ErrNegativeBalance
	}
	if version < initialVersion {
		return nil, ErrInvalidWalletVersion
	}
	if createdAt.IsZero() || updatedAt.IsZero() {
		return nil, ErrMissingTimestamp
	}

	return &Wallet{
		id:        id,
		playerID:  playerID,
		balance:   balance,
		version:   version,
		createdAt: createdAt.UTC(),
		updatedAt: updatedAt.UTC(),
	}, nil
}

func (w *Wallet) ID() uuid.UUID            { return w.id }
func (w *Wallet) PlayerID() uuid.UUID      { return w.playerID }
func (w *Wallet) Balance() money.Money     { return w.balance }
func (w *Wallet) Currency() money.Currency { return w.balance.Currency() }
func (w *Wallet) Version() int64           { return w.version }
func (w *Wallet) CreatedAt() time.Time     { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time     { return w.updatedAt }

func (w *Wallet) OwnedBy(playerID uuid.UUID) bool {
	return w.playerID == playerID
}

func (w *Wallet) Debit(transactionID uuid.UUID, amount money.Money, entryID uuid.UUID, now time.Time) (LedgerEntry, error) {
	return w.move(Debit, transactionID, amount, entryID, now)
}

func (w *Wallet) Credit(transactionID uuid.UUID, amount money.Money, entryID uuid.UUID, now time.Time) (LedgerEntry, error) {
	return w.move(Credit, transactionID, amount, entryID, now)
}

func (w *Wallet) move(direction Direction, transactionID uuid.UUID, amount money.Money, entryID uuid.UUID, now time.Time) (LedgerEntry, error) {
	if transactionID == uuid.Nil || entryID == uuid.Nil {
		return LedgerEntry{}, ErrMissingIdentifier
	}
	if now.IsZero() {
		return LedgerEntry{}, ErrMissingTimestamp
	}
	if !amount.Valid() || !amount.IsPositive() {
		return LedgerEntry{}, ErrInvalidAmount
	}
	if amount.Currency() != w.balance.Currency() {
		return LedgerEntry{}, money.ErrCurrencyMismatch
	}

	updatedBalance, err := applyDirection(w.balance, direction, amount)
	if err != nil {
		return LedgerEntry{}, err
	}
	if updatedBalance.IsNegative() {
		return LedgerEntry{}, ErrInsufficientFunds
	}

	entry, err := NewLedgerEntry(
		entryID,
		w.id,
		transactionID,
		direction,
		amount,
		w.balance,
		updatedBalance,
		w.version+1,
		now,
	)
	if err != nil {
		return LedgerEntry{}, err
	}

	w.balance = updatedBalance
	w.version++
	w.updatedAt = now.UTC()
	return entry, nil
}
