package wagering

type Kind string

const (
	Opening  Kind = "OPENING"
	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
)

func (k Kind) Valid() bool {
	switch k {
	case Opening, Bet, Win, Loss, Refund, Rollback:
		return true
	default:
		return false
	}
}

func (k Kind) External() bool {
	return k.Valid() && k != Opening
}

func (k Kind) Reversal() bool {
	return k == Refund || k == Rollback
}

func (k Kind) RequiresReference() bool {
	return k.Reversal()
}

func (k Kind) ForbidsReference() bool {
	return k == Bet || k == Loss
}

func (k Kind) MovesBalance() bool {
	return k != Loss
}

func (k Kind) String() string {
	return string(k)
}

type Status string

const (
	Pending          Status = "PENDING"
	PendingReference Status = "PENDING_REFERENCE"
	Processed        Status = "PROCESSED"
	Rejected         Status = "REJECTED"
	Failed           Status = "FAILED"
)

func (s Status) Valid() bool {
	switch s {
	case Pending, PendingReference, Processed, Rejected, Failed:
		return true
	default:
		return false
	}
}

func (s Status) Terminal() bool {
	return s == Processed || s == Rejected || s == Failed
}

func (s Status) String() string {
	return string(s)
}

type Origin string

const (
	Internal Origin = "INTERNAL"
	HTTP     Origin = "HTTP"
	Queue    Origin = "SQS"
)

func (o Origin) Valid() bool {
	return o == Internal || o == HTTP || o == Queue
}

func (o Origin) External() bool {
	return o == HTTP || o == Queue
}

func (o Origin) String() string {
	return string(o)
}

type FailureCode string

const (
	InsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	ReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	BalanceLimitExceeded      FailureCode = "BALANCE_LIMIT_EXCEEDED"
	ReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	ReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	ReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	ReferenceKindNotAllowed   FailureCode = "REFERENCE_KIND_NOT_ALLOWED"
	AlreadyReversed           FailureCode = "ALREADY_REVERSED"
	CurrencyMismatch          FailureCode = "CURRENCY_MISMATCH"
	WalletPlayerMismatch      FailureCode = "WALLET_PLAYER_MISMATCH"
	PermanentFailure          FailureCode = "PERMANENT_FAILURE"
)

func (f FailureCode) Valid() bool {
	switch f {
	case InsufficientFunds, ReversalInsufficientFunds, BalanceLimitExceeded,
		ReferenceNotFound, ReferenceNotProcessed, ReferenceMismatch,
		ReferenceKindNotAllowed, AlreadyReversed, CurrencyMismatch,
		WalletPlayerMismatch, PermanentFailure:
		return true
	default:
		return false
	}
}

func (f FailureCode) String() string {
	return string(f)
}
