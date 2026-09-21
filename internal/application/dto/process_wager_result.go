package dto

import (
	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

type ProcessWagerResult struct {
	TransactionID    uuid.UUID
	Status           wagering.Status
	Balance          money.Money
	FailureCode      wagering.FailureCode
	IdempotentReplay bool
	InboxOutcome     messaging.Outcome
}
