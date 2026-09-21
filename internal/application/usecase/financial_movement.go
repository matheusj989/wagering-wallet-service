package usecase

import (
	"context"
	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
	"time"
)

// applyFinancialMovement is shared by immediate settlement and reference retry.
// Its caller supplies a transactional registry, so ledger, balance, version and
// status always commit or roll back together.
func applyFinancialMovement(ctx context.Context, registry repositories.Registry, ids port.IDGenerator, account *wallet.Wallet, operation *wagering.Transaction, decision wagering.Decision, now time.Time) (*wallet.LedgerEntry, error) {
	expectedVersion := account.Version()
	move := account.Credit
	if decision.Movement.Direction == wallet.Debit {
		move = account.Debit
	}
	entry, err := move(operation.ID(), decision.Movement.Amount, ids.New(), now)
	if err != nil {
		return nil, err
	}
	if err := registry.Ledger().Append(ctx, entry); err != nil {
		return nil, err
	}
	if err := registry.Wallets().Update(ctx, account, expectedVersion); err != nil {
		return nil, err
	}
	if err := operation.MarkProcessed(account.Balance(), decision.ReferenceID, now); err != nil {
		return nil, err
	}
	return &entry, nil
}
