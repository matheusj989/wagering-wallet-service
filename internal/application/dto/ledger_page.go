package dto

import "github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"

type LedgerPage struct {
	Entries    []wallet.LedgerEntry
	NextCursor int64
	HasMore    bool
}
