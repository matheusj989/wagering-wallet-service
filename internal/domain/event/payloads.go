package event

import (
	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

type settledCore struct {
	TransactionID uuid.UUID   `json:"transactionId"`
	WalletID      uuid.UUID   `json:"walletId"`
	PlayerID      uuid.UUID   `json:"playerId"`
	Kind          string      `json:"kind"`
	Money         money.Money `json:"money"`
}

type providerMetadata struct {
	ProviderID                     string     `json:"providerId"`
	ExternalTransactionID          string     `json:"externalTransactionId"`
	RoundID                        string     `json:"roundId"`
	GameID                         string     `json:"gameId"`
	ReferenceExternalTransactionID *string    `json:"referenceExternalTransactionId"`
	ReferenceTransactionID         *uuid.UUID `json:"referenceTransactionId"`
}

type OpeningProcessed struct {
	settledCore
	Balance     money.Money `json:"balance"`
	ProcessedAt Timestamp   `json:"processedAt"`
}

type Processed struct {
	settledCore
	Balance money.Money `json:"balance"`
	providerMetadata
	ProcessedAt Timestamp `json:"processedAt"`
}

type Rejected struct {
	settledCore
	FailureCode string      `json:"failureCode"`
	Balance     money.Money `json:"balance"`
	providerMetadata
	RejectedAt Timestamp `json:"rejectedAt"`
}

type BalanceChanged struct {
	WalletID      uuid.UUID   `json:"walletId"`
	PlayerID      uuid.UUID   `json:"playerId"`
	TransactionID uuid.UUID   `json:"transactionId"`
	Kind          string      `json:"kind"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
}

type PendingReference struct {
	settledCore
	providerMetadata
	NextAttemptAt Timestamp `json:"nextAttemptAt"`
	DeadlineAt    Timestamp `json:"deadlineAt"`
}

func NewProcessed(eventID uuid.UUID, operation *wagering.Transaction, balance money.Money) (Envelope, error) {
	core := coreOf(operation)
	if operation.Origin() == wagering.Internal {
		return newEnvelope(eventID, TypeProcessed, AggregateTransaction, operation.ID(),
			operation.CorrelationID(), operation.ID(), operation.CompletedAt(),
			OpeningProcessed{
				settledCore: core,
				Balance:     balance,
				ProcessedAt: Timestamp(operation.CompletedAt()),
			})
	}
	return newEnvelope(eventID, TypeProcessed, AggregateTransaction, operation.ID(),
		operation.CorrelationID(), operation.ID(), operation.CompletedAt(),
		Processed{
			settledCore:      core,
			Balance:          balance,
			providerMetadata: metadataOf(operation),
			ProcessedAt:      Timestamp(operation.CompletedAt()),
		})
}

func NewRejected(eventID uuid.UUID, operation *wagering.Transaction, balance money.Money) (Envelope, error) {
	return newEnvelope(eventID, TypeRejected, AggregateTransaction, operation.ID(),
		operation.CorrelationID(), operation.ID(), operation.CompletedAt(),
		Rejected{
			settledCore:      coreOf(operation),
			FailureCode:      operation.FailureCode().String(),
			Balance:          balance,
			providerMetadata: metadataOf(operation),
			RejectedAt:       Timestamp(operation.CompletedAt()),
		})
}

func NewBalanceChanged(eventID uuid.UUID, operation *wagering.Transaction, entry wallet.LedgerEntry) (Envelope, error) {
	return newEnvelope(eventID, TypeBalanceChanged, AggregateWallet, entry.WalletID(),
		operation.CorrelationID(), operation.ID(), entry.CreatedAt(),
		BalanceChanged{
			WalletID:      entry.WalletID(),
			PlayerID:      operation.PlayerID(),
			TransactionID: entry.TransactionID(),
			Kind:          operation.Kind().String(),
			Direction:     entry.Direction().String(),
			Money:         entry.Amount(),
			BalanceBefore: entry.BalanceBefore(),
			BalanceAfter:  entry.BalanceAfter(),
			WalletVersion: entry.WalletVersion(),
		})
}

func NewPendingReference(eventID uuid.UUID, operation *wagering.Transaction) (Envelope, error) {
	return newEnvelope(eventID, TypePendingReference, AggregateTransaction, operation.ID(),
		operation.CorrelationID(), operation.ID(), operation.UpdatedAt(),
		PendingReference{
			settledCore:      coreOf(operation),
			providerMetadata: metadataOf(operation),
			NextAttemptAt:    Timestamp(operation.NextAttemptAt()),
			DeadlineAt:       Timestamp(operation.ReferenceDeadlineAt()),
		})
}

func coreOf(operation *wagering.Transaction) settledCore {
	return settledCore{
		TransactionID: operation.ID(),
		WalletID:      operation.WalletID(),
		PlayerID:      operation.PlayerID(),
		Kind:          operation.Kind().String(),
		Money:         operation.Amount(),
	}
}

func metadataOf(operation *wagering.Transaction) providerMetadata {
	metadata := providerMetadata{
		ProviderID:            operation.ProviderID(),
		ExternalTransactionID: operation.ExternalTransactionID(),
		RoundID:               operation.RoundID(),
		GameID:                operation.GameID(),
	}
	if reference := operation.ReferenceExternalID(); reference != "" {
		value := reference
		metadata.ReferenceExternalTransactionID = &value
	}
	if resolved := operation.ReferenceTransactionID(); resolved != uuid.Nil {
		value := resolved
		metadata.ReferenceTransactionID = &value
	}
	return metadata
}
