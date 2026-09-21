package dto

import (
	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
)

type ListLedgerInput struct {
	WalletID     uuid.UUID `json:"walletId" validate:"required"`
	AfterVersion int64     `json:"afterVersion" validate:"gte=0"`
	Limit        int       `json:"limit" validate:"gt=0,lte=100"`
}

func (i ListLedgerInput) Validate() error {
	problems := &validation.Error{}
	problems.Collect(validation.Struct(i), nil)
	return problems.OrNil()
}
