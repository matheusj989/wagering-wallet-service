package repositories

import (
	contracts "github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
)

// Registry hands the use case the five repositories bound to the same transaction,
// so everything written inside one unit of work lands or rolls back together.
type Registry struct {
	wallets      *WalletRepository
	ledger       *LedgerRepository
	transactions *WagerTransactionRepository
	inbox        *InboxRepository
	outbox       *OutboxRepository
}

func NewRegistry(queries Querier) *Registry {
	return &Registry{
		wallets:      NewWalletRepository(queries),
		ledger:       NewLedgerRepository(queries),
		transactions: NewWagerTransactionRepository(queries),
		inbox:        NewInboxRepository(queries),
		outbox:       NewOutboxRepository(queries),
	}
}

func (r *Registry) Wallets() contracts.Wallet                { return r.wallets }
func (r *Registry) Ledger() contracts.Ledger                 { return r.ledger }
func (r *Registry) Transactions() contracts.WagerTransaction { return r.transactions }
func (r *Registry) Inbox() contracts.Inbox                   { return r.inbox }
func (r *Registry) Outbox() contracts.Outbox                 { return r.outbox }

var _ contracts.Registry = (*Registry)(nil)
