package http

import (
	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

type walletResponse struct {
	ID        uuid.UUID   `json:"id"`
	PlayerID  uuid.UUID   `json:"playerId"`
	Balance   money.Money `json:"balance"`
	Version   int64       `json:"version"`
	CreatedAt timestamp   `json:"createdAt"`
	UpdatedAt timestamp   `json:"updatedAt"`
}

func walletOf(account *wallet.Wallet) walletResponse {
	return walletResponse{
		ID:        account.ID(),
		PlayerID:  account.PlayerID(),
		Balance:   account.Balance(),
		Version:   account.Version(),
		CreatedAt: at(account.CreatedAt()),
		UpdatedAt: at(account.UpdatedAt()),
	}
}

type ledgerEntryResponse struct {
	ID            uuid.UUID   `json:"id"`
	TransactionID uuid.UUID   `json:"transactionId"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
	CreatedAt     timestamp   `json:"createdAt"`
}

type ledgerResponse struct {
	WalletID   uuid.UUID             `json:"walletId"`
	Entries    []ledgerEntryResponse `json:"entries"`
	NextCursor *string               `json:"nextCursor"`
}

func ledgerOf(walletID uuid.UUID, page dto.LedgerPage, nextCursor *string) ledgerResponse {
	entries := make([]ledgerEntryResponse, 0, len(page.Entries))
	for _, entry := range page.Entries {
		entries = append(entries, ledgerEntryResponse{
			ID:            entry.ID(),
			TransactionID: entry.TransactionID(),
			Direction:     entry.Direction().String(),
			Money:         entry.Amount(),
			BalanceBefore: entry.BalanceBefore(),
			BalanceAfter:  entry.BalanceAfter(),
			WalletVersion: entry.WalletVersion(),
			CreatedAt:     at(entry.CreatedAt()),
		})
	}
	return ledgerResponse{WalletID: walletID, Entries: entries, NextCursor: nextCursor}
}

type reconciliationResponse struct {
	WalletID          uuid.UUID   `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int64       `json:"checkedEntries"`
}

func reconciliationOf(report dto.Reconciliation) reconciliationResponse {
	return reconciliationResponse{
		WalletID:          report.WalletID,
		StoredBalance:     report.StoredBalance,
		CalculatedBalance: report.CalculatedBalance,
		Difference:        report.Difference,
		Consistent:        report.Consistent,
		CheckedEntries:    report.CheckedEntries,
	}
}

type resultResponse struct {
	TransactionID    uuid.UUID    `json:"transactionId"`
	Status           string       `json:"status"`
	Balance          *money.Money `json:"balance,omitempty"`
	FailureCode      *string      `json:"failureCode,omitempty"`
	IdempotentReplay bool         `json:"idempotentReplay"`
}

func resultOf(result dto.ProcessWagerResult) resultResponse {
	response := resultResponse{
		TransactionID:    result.TransactionID,
		Status:           result.Status.String(),
		IdempotentReplay: result.IdempotentReplay,
	}
	if result.Balance.Valid() {
		balance := result.Balance
		response.Balance = &balance
	}
	if result.FailureCode != "" {
		code := result.FailureCode.String()
		response.FailureCode = &code
	}
	return response
}

func statusOf(status wagering.Status) int {
	switch status {
	case wagering.Processed:
		return 201
	case wagering.PendingReference:
		return 202
	default:
		return 422
	}
}

type pendingReferenceResponse struct {
	Attempts      int       `json:"attempts"`
	NextAttemptAt timestamp `json:"nextAttemptAt"`
	DeadlineAt    timestamp `json:"deadlineAt"`
}

type transactionResponse struct {
	TransactionID                  uuid.UUID                 `json:"transactionId"`
	ProviderID                     *string                   `json:"providerId"`
	ExternalTransactionID          *string                   `json:"externalTransactionId"`
	PlayerID                       uuid.UUID                 `json:"playerId"`
	WalletID                       uuid.UUID                 `json:"walletId"`
	RoundID                        *string                   `json:"roundId"`
	GameID                         *string                   `json:"gameId"`
	Kind                           string                    `json:"kind"`
	Money                          money.Money               `json:"money"`
	ReferenceExternalTransactionID *string                   `json:"referenceExternalTransactionId"`
	ReferenceTransactionID         *uuid.UUID                `json:"referenceTransactionId"`
	Status                         string                    `json:"status"`
	FailureCode                    *string                   `json:"failureCode"`
	Balance                        *money.Money              `json:"balance"`
	PendingReference               *pendingReferenceResponse `json:"pendingReference,omitempty"`
	CreatedAt                      timestamp                 `json:"createdAt"`
	UpdatedAt                      timestamp                 `json:"updatedAt"`
	CompletedAt                    *timestamp                `json:"completedAt"`
}

func transactionOf(operation *wagering.Transaction) transactionResponse {
	response := transactionResponse{
		TransactionID:         operation.ID(),
		ProviderID:            optional(operation.ProviderID()),
		ExternalTransactionID: optional(operation.ExternalTransactionID()),
		PlayerID:              operation.PlayerID(),
		WalletID:              operation.WalletID(),
		RoundID:               optional(operation.RoundID()),
		GameID:                optional(operation.GameID()),
		Kind:                  operation.Kind().String(),
		Money:                 operation.Amount(),
		Status:                operation.Status().String(),
		FailureCode:           optional(operation.FailureCode().String()),
		CreatedAt:             at(operation.CreatedAt()),
		UpdatedAt:             at(operation.UpdatedAt()),
		CompletedAt:           atOptional(operation.CompletedAt()),
	}
	if reference := operation.ReferenceExternalID(); reference != "" {
		response.ReferenceExternalTransactionID = &reference
	}
	if resolved := operation.ReferenceTransactionID(); resolved != uuid.Nil {
		response.ReferenceTransactionID = &resolved
	}
	if operation.HasResultBalance() {
		balance := operation.ResultBalance()
		response.Balance = &balance
	}
	if operation.Status() == wagering.PendingReference {
		response.PendingReference = &pendingReferenceResponse{
			Attempts:      operation.ReferenceAttempts(),
			NextAttemptAt: at(operation.NextAttemptAt()),
			DeadlineAt:    at(operation.ReferenceDeadlineAt()),
		}
	}
	return response
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
