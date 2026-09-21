package dto

import (
	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
)

type Reconciliation struct {
	WalletID          uuid.UUID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	Difference        money.Money
	Consistent        bool
	CheckedEntries    int64
}
