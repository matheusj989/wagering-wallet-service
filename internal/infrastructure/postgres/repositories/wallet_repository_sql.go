package repositories

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

const walletColumns = `id, player_id, currency, balance_minor, version, created_at, updated_at`

type WalletRepository struct {
	queries Querier
}

func NewWalletRepository(queries Querier) *WalletRepository {
	return &WalletRepository{queries: queries}
}

func (r *WalletRepository) Create(ctx context.Context, opened *wallet.Wallet) error {
	const statement = `
		INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.queries.Exec(ctx, statement,
		opened.ID(), opened.PlayerID(), opened.Currency().String(),
		opened.Balance().Minor(), opened.Version(), opened.CreatedAt(), opened.UpdatedAt())
	if err != nil {
		if hasCode(err, pgerrcode.UniqueViolation) && constraintIs(err, "wallets_player_currency_uq") {
			return wallet.ErrAlreadyExists
		}
		return Classify(err)
	}
	return nil
}

func (r *WalletRepository) Find(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return r.findOne(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id)
}

func (r *WalletRepository) FindForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	found, err := r.findOne(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR NO KEY UPDATE`, id)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, wallet.ErrNotFound
	}
	return found, nil
}

func (r *WalletRepository) FindByPlayer(ctx context.Context, playerID uuid.UUID, currency money.Currency) (*wallet.Wallet, error) {
	return r.findOne(ctx, `SELECT `+walletColumns+` FROM wallets WHERE player_id = $1 AND currency = $2`,
		playerID, currency.String())
}

func (r *WalletRepository) Update(ctx context.Context, updated *wallet.Wallet, expectedVersion int64) error {
	const statement = `
		UPDATE wallets
		   SET balance_minor = $1, version = $2, updated_at = $3
		 WHERE id = $4 AND version = $5`

	tag, err := r.queries.Exec(ctx, statement,
		updated.Balance().Minor(), updated.Version(), updated.UpdatedAt(), updated.ID(), expectedVersion)
	if err != nil {
		return Classify(err)
	}
	if tag.RowsAffected() == 0 {
		return wallet.ErrVersionChanged
	}
	return nil
}

func (r *WalletRepository) findOne(ctx context.Context, statement string, arguments ...any) (*wallet.Wallet, error) {
	var (
		id           uuid.UUID
		playerID     uuid.UUID
		currencyCode string
		balanceMinor int64
		version      int64
		createdAt    time.Time
		updatedAt    time.Time
	)

	err := r.queries.QueryRow(ctx, statement, arguments...).
		Scan(&id, &playerID, &currencyCode, &balanceMinor, &version, &createdAt, &updatedAt)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, Classify(err)
	}

	currency, err := money.ParseCurrency(currencyCode)
	if err != nil {
		return nil, err
	}
	balance, err := money.FromMinor(balanceMinor, currency)
	if err != nil {
		return nil, err
	}
	return wallet.Rehydrate(id, playerID, balance, version, createdAt, updatedAt)
}
