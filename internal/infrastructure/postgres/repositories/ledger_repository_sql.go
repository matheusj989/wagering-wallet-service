package repositories

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

const ledgerColumns = `id, wallet_id, transaction_id, direction, amount_minor, currency,
	balance_before_minor, balance_after_minor, wallet_version, created_at`

type LedgerRepository struct {
	queries Querier
}

func NewLedgerRepository(queries Querier) *LedgerRepository {
	return &LedgerRepository{queries: queries}
}

func (r *LedgerRepository) Append(ctx context.Context, entry wallet.LedgerEntry) error {
	const statement = `
		INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
			balance_before_minor, balance_after_minor, wallet_version, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := r.queries.Exec(ctx, statement,
		entry.ID(), entry.WalletID(), entry.TransactionID(), entry.Direction().String(),
		entry.Amount().Minor(), entry.Amount().Currency().String(),
		entry.BalanceBefore().Minor(), entry.BalanceAfter().Minor(),
		entry.WalletVersion(), entry.CreatedAt())
	if err != nil {
		return Classify(err)
	}
	return nil
}

func (r *LedgerRepository) ListAfterVersion(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) ([]wallet.LedgerEntry, error) {
	const statement = `
		SELECT ` + ledgerColumns + `
		  FROM wallet_ledger_entries
		 WHERE wallet_id = $1 AND wallet_version > $2
		 ORDER BY wallet_version
		 LIMIT $3`

	rows, err := r.queries.Query(ctx, statement, walletID, afterVersion, limit)
	if err != nil {
		return nil, Classify(err)
	}
	defer rows.Close()

	entries := make([]wallet.LedgerEntry, 0, limit)
	for rows.Next() {
		entry, err := scanLedgerEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, Classify(err)
	}
	return entries, nil
}

func (r *LedgerRepository) Totals(ctx context.Context, walletID uuid.UUID, currency money.Currency) (wallet.LedgerTotals, error) {
	const statement = `
		SELECT COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0),
		       COUNT(*)
		  FROM wallet_ledger_entries
		 WHERE wallet_id = $1`

	var (
		balanceMinor int64
		entries      int64
	)
	if err := r.queries.QueryRow(ctx, statement, walletID).Scan(&balanceMinor, &entries); err != nil {
		return wallet.LedgerTotals{}, Classify(err)
	}

	balance, err := money.FromMinor(balanceMinor, currency)
	if err != nil {
		return wallet.LedgerTotals{}, err
	}
	return wallet.LedgerTotals{Balance: balance, Entries: entries}, nil
}

type scanner interface {
	Scan(destination ...any) error
}

func scanLedgerEntry(row scanner) (wallet.LedgerEntry, error) {
	var (
		id            uuid.UUID
		walletID      uuid.UUID
		transactionID uuid.UUID
		direction     string
		amountMinor   int64
		currencyCode  string
		beforeMinor   int64
		afterMinor    int64
		walletVersion int64
		createdAt     time.Time
	)

	if err := row.Scan(&id, &walletID, &transactionID, &direction, &amountMinor, &currencyCode,
		&beforeMinor, &afterMinor, &walletVersion, &createdAt); err != nil {
		return wallet.LedgerEntry{}, Classify(err)
	}

	currency, err := money.ParseCurrency(currencyCode)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	amount, err := money.FromMinor(amountMinor, currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	before, err := money.FromMinor(beforeMinor, currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	after, err := money.FromMinor(afterMinor, currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	return wallet.NewLedgerEntry(id, walletID, transactionID, wallet.Direction(direction),
		amount, before, after, walletVersion, createdAt)
}
