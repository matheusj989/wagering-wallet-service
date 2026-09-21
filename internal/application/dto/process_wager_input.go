package dto

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

type ProcessWagerInput struct {
	Origin                wagering.Origin `json:"-"`
	ProviderID            string          `json:"providerId" validate:"required,max=255,opaque"`
	ExternalTransactionID string          `json:"externalTransactionId" validate:"required,max=255,opaque"`
	IdempotencyKey        string          `json:"idempotencyKey" validate:"required,max=255,opaque"`
	PlayerID              uuid.UUID       `json:"playerId" validate:"required"`
	WalletID              uuid.UUID       `json:"walletId" validate:"required"`
	RoundID               string          `json:"roundId" validate:"required,max=255,opaque"`
	GameID                string          `json:"gameId" validate:"required,max=255,opaque"`
	Kind                  wagering.Kind   `json:"kind" validate:"required,wager_kind"`
	Amount                money.Money     `json:"money" validate:"-"`
	ReferenceExternalID   string          `json:"referenceExternalTransactionId" validate:"omitempty,max=255,opaque"`
	CorrelationID         string          `json:"-"`

	MessageID   string `json:"-"`
	RawBodyHash string `json:"-"`
}

func (i ProcessWagerInput) Validate() error {
	problems := &validation.Error{}
	problems.Collect(validation.Struct(i), map[string]string{"idempotencyKey": i.idempotencyKeyField()})

	if !i.Amount.Valid() {
		problems.Add("money.amount", "must be a decimal string with two places and a supported currency")
	}
	if i.Kind.External() {
		i.checkReferencePolicy(problems)
		i.checkAmountPolicy(problems)
	}
	return problems.OrNil()
}

func (i ProcessWagerInput) FromQueue() bool {
	return i.MessageID != ""
}

func (i ProcessWagerInput) idempotencyKeyField() string {
	if i.Origin == wagering.Queue {
		return "data.idempotencyKey"
	}
	return "Idempotency-Key"
}

func (i ProcessWagerInput) checkReferencePolicy(problems *validation.Error) {
	if i.Kind.RequiresReference() && i.ReferenceExternalID == "" {
		problems.Add("referenceExternalTransactionId", fmt.Sprintf("is required for %s", i.Kind))
	}
	if i.Kind.ForbidsReference() && i.ReferenceExternalID != "" {
		problems.Add("referenceExternalTransactionId", fmt.Sprintf("must not be sent for %s", i.Kind))
	}
}

func (i ProcessWagerInput) checkAmountPolicy(problems *validation.Error) {
	if !i.Amount.Valid() {
		return
	}
	if i.Kind == wagering.Loss && !i.Amount.IsZero() {
		problems.Add("money.amount", "must be \"0.00\" for LOSS")
		return
	}
	if i.Kind != wagering.Loss && !i.Amount.IsPositive() {
		problems.Add("money.amount", fmt.Sprintf("must be greater than zero for %s", i.Kind))
	}
}
