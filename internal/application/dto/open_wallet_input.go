package dto

import (
	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
)

type OpenWalletInput struct {
	PlayerID       uuid.UUID   `json:"playerId" validate:"required"`
	InitialBalance money.Money `json:"initialBalance" validate:"-"`
	CorrelationID  string      `json:"-"`
}

func (i OpenWalletInput) Validate() error {
	problems := &validation.Error{}
	problems.Collect(validation.Struct(i), nil)

	switch {
	case !i.InitialBalance.Valid():
		problems.Add("initialBalance.amount", "must be a decimal string with two places and a supported currency")
	case i.InitialBalance.IsNegative():
		problems.Add("initialBalance.amount", "must not be negative")
	}
	return problems.OrNil()
}
