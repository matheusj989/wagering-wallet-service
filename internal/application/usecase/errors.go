package usecase

import (
	"fmt"

	"github.com/google/uuid"
)

type ConflictKind string

const (
	ConflictIdempotencyKey ConflictKind = "IDEMPOTENCY_KEY_CONFLICT"
	ConflictExternalID     ConflictKind = "EXTERNAL_TRANSACTION_ID_CONFLICT"
)

type ConflictError struct {
	Kind          ConflictKind
	TransactionID uuid.UUID
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("wagering: %s (existing transaction %s)", e.Kind, e.TransactionID)
}

type WalletConflictError struct {
	WalletID uuid.UUID
}

func (e *WalletConflictError) Error() string {
	return fmt.Sprintf("wallet: player already has a wallet (%s)", e.WalletID)
}
