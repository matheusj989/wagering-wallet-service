package wagering

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

type Outcome int

const (
	OutcomeProcess Outcome = iota + 1
	OutcomeReject
	OutcomeWaitReference
)

type Movement struct {
	Direction wallet.Direction
	Amount    money.Money
}

func (m Movement) Present() bool {
	return m.Direction.Valid()
}

type Decision struct {
	Outcome     Outcome
	Movement    Movement
	FailureCode FailureCode
	ReferenceID uuid.UUID
}

type Reference struct {
	Transaction     *Transaction
	AlreadyReversed bool
}

func Decide(account *wallet.Wallet, operation *Transaction, reference *Reference, now time.Time) (Decision, error) {
	if account == nil || operation == nil {
		return Decision{}, ErrMissingIdentifier
	}
	if account.ID() != operation.WalletID() {
		return Decision{}, ErrMissingIdentifier
	}

	if !account.OwnedBy(operation.PlayerID()) {
		return reject(WalletPlayerMismatch), nil
	}
	if operation.Amount().Currency() != account.Currency() {
		return reject(CurrencyMismatch), nil
	}

	switch operation.Kind() {
	case Bet:
		return decideBet(account, operation)
	case Win:
		return decideWin(account, operation, reference)
	case Loss:
		return Decision{Outcome: OutcomeProcess}, nil
	case Refund, Rollback:
		return decideReversal(account, operation, reference, now)
	default:
		return Decision{}, ErrInvalidKind
	}
}

func decideBet(account *wallet.Wallet, operation *Transaction) (Decision, error) {
	affordable, err := account.Balance().Cmp(operation.Amount())
	if err != nil {
		return Decision{}, err
	}
	if affordable < 0 {
		return reject(InsufficientFunds), nil
	}
	return process(wallet.Debit, operation.Amount(), uuid.Nil), nil
}

func decideWin(account *wallet.Wallet, operation *Transaction, reference *Reference) (Decision, error) {
	resolved := uuid.Nil
	if operation.ReferenceExternalID() != "" && reference != nil && reference.Transaction != nil {
		original := reference.Transaction
		if original.Kind() != Bet {
			return reject(ReferenceKindNotAllowed), nil
		}
		if !sameRound(operation, original) {
			return reject(ReferenceMismatch), nil
		}
		resolved = original.ID()
	}

	if _, err := account.Balance().Add(operation.Amount()); err != nil {
		if errors.Is(err, money.ErrOverflow) {
			return reject(BalanceLimitExceeded), nil
		}
		return Decision{}, err
	}
	return process(wallet.Credit, operation.Amount(), resolved), nil
}

func decideReversal(account *wallet.Wallet, operation *Transaction, reference *Reference, now time.Time) (Decision, error) {
	expired := deadlineReached(operation, now)

	if reference == nil || reference.Transaction == nil {
		if expired {
			return reject(ReferenceNotFound), nil
		}
		return Decision{Outcome: OutcomeWaitReference}, nil
	}

	original := reference.Transaction
	switch original.Status() {
	case Pending, PendingReference:
		if expired {
			return reject(ReferenceNotProcessed), nil
		}
		return Decision{Outcome: OutcomeWaitReference}, nil
	case Rejected, Failed:
		return reject(ReferenceNotProcessed), nil
	}

	if !reversible(operation.Kind(), original.Kind()) {
		return reject(ReferenceKindNotAllowed), nil
	}
	if !sameRound(operation, original) || !operation.Amount().Equal(original.Amount()) {
		return reject(ReferenceMismatch), nil
	}
	if reference.AlreadyReversed {
		return reject(AlreadyReversed), nil
	}

	direction := reversalDirection(operation.Kind(), original.Kind())
	if direction == wallet.Debit {
		affordable, err := account.Balance().Cmp(operation.Amount())
		if err != nil {
			return Decision{}, err
		}
		if affordable < 0 {
			return reject(ReversalInsufficientFunds), nil
		}
	} else if _, err := account.Balance().Add(operation.Amount()); err != nil {
		if errors.Is(err, money.ErrOverflow) {
			return reject(BalanceLimitExceeded), nil
		}
		return Decision{}, err
	}

	return process(direction, operation.Amount(), original.ID()), nil
}

func reversible(reversal Kind, original Kind) bool {
	switch reversal {
	case Refund:
		return original == Bet
	case Rollback:
		return original == Bet || original == Win || original == Refund
	default:
		return false
	}
}

func reversalDirection(reversal Kind, original Kind) wallet.Direction {
	if reversal == Refund {
		return wallet.Credit
	}
	if original == Bet {
		return wallet.Credit
	}
	return wallet.Debit
}

func sameRound(operation *Transaction, original *Transaction) bool {
	return operation.ProviderID() == original.ProviderID() &&
		operation.PlayerID() == original.PlayerID() &&
		operation.WalletID() == original.WalletID() &&
		operation.RoundID() == original.RoundID() &&
		operation.Amount().Currency() == original.Amount().Currency()
}

func deadlineReached(operation *Transaction, now time.Time) bool {
	deadline := operation.ReferenceDeadlineAt()
	return !deadline.IsZero() && !now.Before(deadline)
}

func process(direction wallet.Direction, amount money.Money, referenceID uuid.UUID) Decision {
	return Decision{
		Outcome:     OutcomeProcess,
		Movement:    Movement{Direction: direction, Amount: amount},
		ReferenceID: referenceID,
	}
}

func reject(code FailureCode) Decision {
	return Decision{Outcome: OutcomeReject, FailureCode: code}
}
