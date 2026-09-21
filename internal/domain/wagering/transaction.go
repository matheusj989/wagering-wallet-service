package wagering

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
)

var (
	ErrMissingIdentifier    = errors.New("wagering: identifier must not be empty")
	ErrMissingTimestamp     = errors.New("wagering: timestamp must not be empty")
	ErrMissingField         = errors.New("wagering: required field is empty")
	ErrInvalidKind          = errors.New("wagering: kind is not a valid operation type")
	ErrInvalidOrigin        = errors.New("wagering: origin is not valid for this operation")
	ErrInvalidStatus        = errors.New("wagering: status is not valid")
	ErrInvalidFailureCode   = errors.New("wagering: failure code is not in the catalogue")
	ErrInvalidAmount        = errors.New("wagering: amount does not follow the policy for this kind")
	ErrInvalidPayloadHash   = errors.New("wagering: payload hash must be 64 lowercase hexadecimal characters")
	ErrReferenceRequired    = errors.New("wagering: this kind requires a reference")
	ErrReferenceForbidden   = errors.New("wagering: this kind must not carry a reference")
	ErrTransitionNotAllowed = errors.New("wagering: transition is not allowed from the current status")
	ErrInvalidSnapshot      = errors.New("wagering: persisted state is inconsistent")
	ErrNotFound             = errors.New("wagering: not found")
)

var payloadHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Transaction struct {
	id       uuid.UUID
	origin   Origin
	kind     Kind
	status   Status
	walletID uuid.UUID
	playerID uuid.UUID
	amount   money.Money

	providerID             string
	externalTransactionID  string
	idempotencyKey         string
	payloadHash            string
	roundID                string
	gameID                 string
	referenceExternalID    string
	referenceTransactionID uuid.UUID

	failureCode   FailureCode
	resultBalance money.Money

	referenceAttempts   int
	nextAttemptAt       time.Time
	referenceDeadlineAt time.Time

	correlationID string
	createdAt     time.Time
	updatedAt     time.Time
	completedAt   time.Time
}

type Command struct {
	Origin                Origin
	Kind                  Kind
	ProviderID            string
	ExternalTransactionID string
	IdempotencyKey        string
	PayloadHash           string
	PlayerID              uuid.UUID
	WalletID              uuid.UUID
	RoundID               string
	GameID                string
	Amount                money.Money
	ReferenceExternalID   string
	CorrelationID         string
}

func NewExternal(command Command, id uuid.UUID, now time.Time) (*Transaction, error) {
	if id == uuid.Nil || command.WalletID == uuid.Nil || command.PlayerID == uuid.Nil {
		return nil, ErrMissingIdentifier
	}
	if now.IsZero() {
		return nil, ErrMissingTimestamp
	}
	if !command.Origin.External() {
		return nil, ErrInvalidOrigin
	}
	if !command.Kind.External() {
		return nil, ErrInvalidKind
	}
	for _, field := range []string{
		command.ProviderID, command.ExternalTransactionID, command.IdempotencyKey,
		command.RoundID, command.GameID, command.CorrelationID,
	} {
		if strings.TrimSpace(field) == "" {
			return nil, ErrMissingField
		}
	}
	if !payloadHashPattern.MatchString(command.PayloadHash) {
		return nil, ErrInvalidPayloadHash
	}
	if err := checkAmountPolicy(command.Kind, command.Amount); err != nil {
		return nil, err
	}
	if command.Kind.RequiresReference() && command.ReferenceExternalID == "" {
		return nil, ErrReferenceRequired
	}
	if command.Kind.ForbidsReference() && command.ReferenceExternalID != "" {
		return nil, ErrReferenceForbidden
	}

	return &Transaction{
		id:                    id,
		origin:                command.Origin,
		kind:                  command.Kind,
		status:                Pending,
		walletID:              command.WalletID,
		playerID:              command.PlayerID,
		amount:                command.Amount,
		providerID:            command.ProviderID,
		externalTransactionID: command.ExternalTransactionID,
		idempotencyKey:        command.IdempotencyKey,
		payloadHash:           command.PayloadHash,
		roundID:               command.RoundID,
		gameID:                command.GameID,
		referenceExternalID:   command.ReferenceExternalID,
		correlationID:         command.CorrelationID,
		createdAt:             now.UTC(),
		updatedAt:             now.UTC(),
	}, nil
}

func NewOpening(id uuid.UUID, walletID uuid.UUID, playerID uuid.UUID, amount money.Money, correlationID string, now time.Time) (*Transaction, error) {
	if id == uuid.Nil || walletID == uuid.Nil || playerID == uuid.Nil {
		return nil, ErrMissingIdentifier
	}
	if now.IsZero() {
		return nil, ErrMissingTimestamp
	}
	if strings.TrimSpace(correlationID) == "" {
		return nil, ErrMissingField
	}
	if !amount.Valid() || !amount.IsPositive() {
		return nil, ErrInvalidAmount
	}

	return &Transaction{
		id:            id,
		origin:        Internal,
		kind:          Opening,
		status:        Processed,
		walletID:      walletID,
		playerID:      playerID,
		amount:        amount,
		resultBalance: amount,
		correlationID: correlationID,
		createdAt:     now.UTC(),
		updatedAt:     now.UTC(),
		completedAt:   now.UTC(),
	}, nil
}

type Snapshot struct {
	ID                     uuid.UUID
	Origin                 Origin
	Kind                   Kind
	Status                 Status
	WalletID               uuid.UUID
	PlayerID               uuid.UUID
	Amount                 money.Money
	ProviderID             string
	ExternalTransactionID  string
	IdempotencyKey         string
	PayloadHash            string
	RoundID                string
	GameID                 string
	ReferenceExternalID    string
	ReferenceTransactionID uuid.UUID
	FailureCode            FailureCode
	ResultBalance          money.Money
	ReferenceAttempts      int
	NextAttemptAt          time.Time
	ReferenceDeadlineAt    time.Time
	CorrelationID          string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	CompletedAt            time.Time
}

func Rehydrate(snapshot Snapshot) (*Transaction, error) {
	if snapshot.ID == uuid.Nil || snapshot.WalletID == uuid.Nil || snapshot.PlayerID == uuid.Nil {
		return nil, ErrMissingIdentifier
	}
	if !snapshot.Origin.Valid() {
		return nil, ErrInvalidOrigin
	}
	if !snapshot.Kind.Valid() {
		return nil, ErrInvalidKind
	}
	if !snapshot.Status.Valid() {
		return nil, ErrInvalidStatus
	}
	if snapshot.FailureCode != "" && !snapshot.FailureCode.Valid() {
		return nil, ErrInvalidFailureCode
	}
	if snapshot.CreatedAt.IsZero() || snapshot.UpdatedAt.IsZero() {
		return nil, ErrMissingTimestamp
	}
	if err := snapshot.validate(); err != nil {
		return nil, err
	}

	return &Transaction{
		id:                     snapshot.ID,
		origin:                 snapshot.Origin,
		kind:                   snapshot.Kind,
		status:                 snapshot.Status,
		walletID:               snapshot.WalletID,
		playerID:               snapshot.PlayerID,
		amount:                 snapshot.Amount,
		providerID:             snapshot.ProviderID,
		externalTransactionID:  snapshot.ExternalTransactionID,
		idempotencyKey:         snapshot.IdempotencyKey,
		payloadHash:            snapshot.PayloadHash,
		roundID:                snapshot.RoundID,
		gameID:                 snapshot.GameID,
		referenceExternalID:    snapshot.ReferenceExternalID,
		referenceTransactionID: snapshot.ReferenceTransactionID,
		failureCode:            snapshot.FailureCode,
		resultBalance:          snapshot.ResultBalance,
		referenceAttempts:      snapshot.ReferenceAttempts,
		nextAttemptAt:          snapshot.NextAttemptAt,
		referenceDeadlineAt:    snapshot.ReferenceDeadlineAt,
		correlationID:          snapshot.CorrelationID,
		createdAt:              snapshot.CreatedAt,
		updatedAt:              snapshot.UpdatedAt,
		completedAt:            snapshot.CompletedAt,
	}, nil
}

func (s Snapshot) validate() error {
	if s.Kind == Opening {
		if s.Origin != Internal || s.Status != Processed || s.ProviderID != "" || s.ExternalTransactionID != "" ||
			s.IdempotencyKey != "" || s.PayloadHash != "" || s.RoundID != "" || s.GameID != "" ||
			s.ReferenceExternalID != "" || s.ReferenceTransactionID != uuid.Nil {
			return ErrInvalidSnapshot
		}
		if _, err := NewOpening(s.ID, s.WalletID, s.PlayerID, s.Amount, s.CorrelationID, s.CreatedAt); err != nil {
			return err
		}
		if s.ResultBalance != s.Amount {
			return ErrInvalidSnapshot
		}
	} else {
		_, err := NewExternal(Command{Origin: s.Origin, Kind: s.Kind, ProviderID: s.ProviderID,
			ExternalTransactionID: s.ExternalTransactionID, IdempotencyKey: s.IdempotencyKey,
			PayloadHash: s.PayloadHash, PlayerID: s.PlayerID, WalletID: s.WalletID, RoundID: s.RoundID,
			GameID: s.GameID, Amount: s.Amount, ReferenceExternalID: s.ReferenceExternalID,
			CorrelationID: s.CorrelationID}, s.ID, s.CreatedAt)
		if err != nil {
			return err
		}
	}
	if s.ReferenceAttempts < 0 || s.ReferenceTransactionID == s.ID ||
		(s.ReferenceTransactionID != uuid.Nil && (s.Kind.ForbidsReference() || s.ReferenceExternalID == "")) {
		return ErrInvalidSnapshot
	}
	if s.Status.Terminal() == s.CompletedAt.IsZero() {
		return ErrInvalidSnapshot
	}
	if s.Status == PendingReference {
		if !s.Kind.Reversal() || s.NextAttemptAt.IsZero() || s.ReferenceDeadlineAt.IsZero() {
			return ErrInvalidSnapshot
		}
	} else if !s.NextAttemptAt.IsZero() || !s.ReferenceDeadlineAt.IsZero() {
		return ErrInvalidSnapshot
	}
	switch s.Status {
	case Processed, Rejected:
		if !s.ResultBalance.Valid() || s.ResultBalance.IsNegative() {
			return ErrInvalidAmount
		}
		if s.Status == Processed {
			if s.FailureCode != "" || s.ResultBalance.Currency() != s.Amount.Currency() ||
				(s.Kind.Reversal() && s.ReferenceTransactionID == uuid.Nil) {
				return ErrInvalidSnapshot
			}
		} else if !s.FailureCode.Valid() || s.FailureCode == PermanentFailure {
			return ErrInvalidFailureCode
		}
	case Failed:
		if !s.Kind.Reversal() || s.FailureCode != PermanentFailure || s.ResultBalance.Valid() {
			return ErrInvalidSnapshot
		}
	default:
		if s.FailureCode != "" || s.ResultBalance.Valid() || s.ReferenceTransactionID != uuid.Nil {
			return ErrInvalidSnapshot
		}
	}
	return nil
}

func (t *Transaction) ID() uuid.UUID                     { return t.id }
func (t *Transaction) Origin() Origin                    { return t.origin }
func (t *Transaction) Kind() Kind                        { return t.kind }
func (t *Transaction) Status() Status                    { return t.status }
func (t *Transaction) WalletID() uuid.UUID               { return t.walletID }
func (t *Transaction) PlayerID() uuid.UUID               { return t.playerID }
func (t *Transaction) Amount() money.Money               { return t.amount }
func (t *Transaction) ProviderID() string                { return t.providerID }
func (t *Transaction) ExternalTransactionID() string     { return t.externalTransactionID }
func (t *Transaction) IdempotencyKey() string            { return t.idempotencyKey }
func (t *Transaction) PayloadHash() string               { return t.payloadHash }
func (t *Transaction) RoundID() string                   { return t.roundID }
func (t *Transaction) GameID() string                    { return t.gameID }
func (t *Transaction) ReferenceExternalID() string       { return t.referenceExternalID }
func (t *Transaction) ReferenceTransactionID() uuid.UUID { return t.referenceTransactionID }
func (t *Transaction) FailureCode() FailureCode          { return t.failureCode }
func (t *Transaction) ResultBalance() money.Money        { return t.resultBalance }
func (t *Transaction) ReferenceAttempts() int            { return t.referenceAttempts }
func (t *Transaction) NextAttemptAt() time.Time          { return t.nextAttemptAt }
func (t *Transaction) ReferenceDeadlineAt() time.Time    { return t.referenceDeadlineAt }
func (t *Transaction) CorrelationID() string             { return t.correlationID }
func (t *Transaction) CreatedAt() time.Time              { return t.createdAt }
func (t *Transaction) UpdatedAt() time.Time              { return t.updatedAt }
func (t *Transaction) CompletedAt() time.Time            { return t.completedAt }

func (t *Transaction) Terminal() bool {
	return t.status.Terminal()
}

func (t *Transaction) HasResultBalance() bool {
	return t.resultBalance.Valid()
}

func (t *Transaction) MarkProcessed(resultBalance money.Money, referenceTransactionID uuid.UUID, now time.Time) error {
	if err := t.canSettle(now); err != nil {
		return err
	}
	if !resultBalance.Valid() || resultBalance.IsNegative() || resultBalance.Currency() != t.amount.Currency() {
		return ErrInvalidAmount
	}
	if t.kind.Reversal() && referenceTransactionID == uuid.Nil {
		return ErrReferenceRequired
	}

	if referenceTransactionID == t.id || (referenceTransactionID != uuid.Nil && (t.kind.ForbidsReference() || t.referenceExternalID == "")) {
		return ErrReferenceForbidden
	}

	t.status = Processed
	t.resultBalance = resultBalance
	if referenceTransactionID != uuid.Nil {
		t.referenceTransactionID = referenceTransactionID
	}
	t.completedAt = now.UTC()
	t.updatedAt = now.UTC()
	t.clearSchedule()
	return nil
}

func (t *Transaction) MarkRejected(code FailureCode, resultBalance money.Money, now time.Time) error {
	if err := t.canSettle(now); err != nil {
		return err
	}
	if !code.Valid() || code == PermanentFailure {
		return ErrInvalidFailureCode
	}
	if !resultBalance.Valid() || resultBalance.IsNegative() {
		return ErrInvalidAmount
	}

	t.status = Rejected
	t.failureCode = code
	t.resultBalance = resultBalance
	t.completedAt = now.UTC()
	t.updatedAt = now.UTC()
	t.clearSchedule()
	return nil
}

func (t *Transaction) MarkPendingReference(nextAttemptAt time.Time, deadlineAt time.Time, now time.Time) error {
	if now.IsZero() || nextAttemptAt.IsZero() || deadlineAt.IsZero() {
		return ErrMissingTimestamp
	}
	if t.status != Pending {
		return ErrTransitionNotAllowed
	}
	if !t.kind.Reversal() {
		return ErrInvalidKind
	}

	t.status = PendingReference
	t.nextAttemptAt = nextAttemptAt.UTC()
	t.referenceDeadlineAt = deadlineAt.UTC()
	t.referenceAttempts = 0
	t.updatedAt = now.UTC()
	return nil
}

func (t *Transaction) Reschedule(nextAttemptAt time.Time, now time.Time) error {
	if now.IsZero() || nextAttemptAt.IsZero() {
		return ErrMissingTimestamp
	}
	if t.status != PendingReference {
		return ErrTransitionNotAllowed
	}

	t.referenceAttempts++
	t.nextAttemptAt = nextAttemptAt.UTC()
	t.updatedAt = now.UTC()
	return nil
}

func (t *Transaction) MarkFailed(now time.Time) error {
	if now.IsZero() {
		return ErrMissingTimestamp
	}
	if t.status != PendingReference {
		return ErrTransitionNotAllowed
	}

	t.status = Failed
	t.failureCode = PermanentFailure
	t.completedAt = now.UTC()
	t.updatedAt = now.UTC()
	t.clearSchedule()
	return nil
}

func (t *Transaction) canSettle(now time.Time) error {
	if now.IsZero() {
		return ErrMissingTimestamp
	}
	if t.status != Pending && t.status != PendingReference {
		return ErrTransitionNotAllowed
	}
	return nil
}

func (t *Transaction) clearSchedule() {
	t.nextAttemptAt = time.Time{}
	t.referenceDeadlineAt = time.Time{}
}

func checkAmountPolicy(kind Kind, amount money.Money) error {
	if !amount.Valid() {
		return money.ErrInvalidCurrency
	}
	if kind == Loss {
		if !amount.IsZero() {
			return ErrInvalidAmount
		}
		return nil
	}
	if !amount.IsPositive() {
		return ErrInvalidAmount
	}
	return nil
}
