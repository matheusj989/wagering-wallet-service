package repositories

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

const transactionColumns = `t.id, t.origin, t.kind, t.status, t.wallet_id, t.player_id, t.amount_minor, t.currency,
	t.provider_id, t.external_transaction_id, t.idempotency_key, t.payload_hash, t.round_id, t.game_id,
	t.reference_external_transaction_id, t.reference_transaction_id, t.failure_code, t.result_balance_minor,
	t.reference_attempts, t.next_attempt_at, t.reference_deadline_at, t.correlation_id,
	t.created_at, t.updated_at, t.completed_at, w.currency`

const transactionSource = ` FROM wager_transactions t JOIN wallets w ON w.id = t.wallet_id`

type WagerTransactionRepository struct {
	queries Querier
}

func NewWagerTransactionRepository(queries Querier) *WagerTransactionRepository {
	return &WagerTransactionRepository{queries: queries}
}

func (r *WagerTransactionRepository) Insert(ctx context.Context, operation *wagering.Transaction) (bool, error) {
	const statement = `
		INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
			provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
			reference_external_transaction_id, reference_transaction_id, failure_code, result_balance_minor,
			reference_attempts, next_attempt_at, reference_deadline_at, correlation_id,
			created_at, updated_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)
		ON CONFLICT DO NOTHING
		RETURNING id`

	var inserted uuid.UUID
	err := r.queries.QueryRow(ctx, statement,
		operation.ID(), operation.Origin().String(), operation.Kind().String(), operation.Status().String(),
		operation.WalletID(), operation.PlayerID(), operation.Amount().Minor(), operation.Amount().Currency().String(),
		nullString(operation.ProviderID()), nullString(operation.ExternalTransactionID()),
		nullString(operation.IdempotencyKey()), nullString(operation.PayloadHash()),
		nullString(operation.RoundID()), nullString(operation.GameID()),
		nullString(operation.ReferenceExternalID()), nullUUID(operation.ReferenceTransactionID()),
		nullString(operation.FailureCode().String()), nullMoney(operation.ResultBalance()),
		operation.ReferenceAttempts(), nullTime(operation.NextAttemptAt()), nullTime(operation.ReferenceDeadlineAt()),
		operation.CorrelationID(), operation.CreatedAt(), operation.UpdatedAt(), nullTime(operation.CompletedAt()),
	).Scan(&inserted)

	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		if hasCode(err, pgerrcode.ForeignKeyViolation) && constraintIs(err, "wager_transactions_wallet_id_fkey") {
			return false, wallet.ErrNotFound
		}
		return false, Classify(err)
	}
	return true, nil
}

func (r *WagerTransactionRepository) Update(ctx context.Context, operation *wagering.Transaction) error {
	const statement = `
		UPDATE wager_transactions
		   SET status = $1, failure_code = $2, result_balance_minor = $3, reference_transaction_id = $4,
		       reference_attempts = $5, next_attempt_at = $6, reference_deadline_at = $7,
		       completed_at = $8, updated_at = $9
		 WHERE id = $10`

	tag, err := r.queries.Exec(ctx, statement,
		operation.Status().String(), nullString(operation.FailureCode().String()), nullMoney(operation.ResultBalance()),
		nullUUID(operation.ReferenceTransactionID()), operation.ReferenceAttempts(),
		nullTime(operation.NextAttemptAt()), nullTime(operation.ReferenceDeadlineAt()),
		nullTime(operation.CompletedAt()), operation.UpdatedAt(), operation.ID())
	if err != nil {
		return Classify(err)
	}
	if tag.RowsAffected() == 0 {
		return wagering.ErrNotFound
	}
	return nil
}

func (r *WagerTransactionRepository) FindByID(ctx context.Context, id uuid.UUID) (*wagering.Transaction, error) {
	return r.findOne(ctx, `SELECT `+transactionColumns+transactionSource+` WHERE t.id = $1`, id)
}

func (r *WagerTransactionRepository) FindByIDForUpdate(ctx context.Context, id uuid.UUID) (*wagering.Transaction, error) {
	return r.findOne(ctx, `SELECT `+transactionColumns+transactionSource+` WHERE t.id = $1 FOR UPDATE OF t`, id)
}

func (r *WagerTransactionRepository) CountPending(ctx context.Context) (int64, error) {
	var count int64
	err := r.queries.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE'`).Scan(&count)
	return count, Classify(err)
}

func (r *WagerTransactionRepository) FindByIdempotencyKey(ctx context.Context, providerID string, key string) (*wagering.Transaction, error) {
	return r.findOne(ctx, `SELECT `+transactionColumns+transactionSource+
		` WHERE t.provider_id = $1 AND t.idempotency_key = $2`, providerID, key)
}

func (r *WagerTransactionRepository) FindByExternalID(ctx context.Context, providerID string, externalTransactionID string) (*wagering.Transaction, error) {
	return r.findOne(ctx, `SELECT `+transactionColumns+transactionSource+
		` WHERE t.provider_id = $1 AND t.external_transaction_id = $2`, providerID, externalTransactionID)
}

func (r *WagerTransactionRepository) ClaimDuePending(ctx context.Context, now time.Time, limit int) ([]*wagering.Transaction, error) {
	const statement = `
		SELECT ` + transactionColumns + transactionSource + `
		 WHERE t.status = 'PENDING_REFERENCE' AND t.next_attempt_at <= $1
		 ORDER BY t.next_attempt_at, t.id
		   FOR UPDATE OF t SKIP LOCKED
		 LIMIT $2`

	rows, err := r.queries.Query(ctx, statement, now, limit)
	if err != nil {
		return nil, Classify(err)
	}
	defer rows.Close()

	claimed := make([]*wagering.Transaction, 0, limit)
	for rows.Next() {
		operation, err := scanTransaction(rows)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, Classify(err)
	}
	return claimed, nil
}

func (r *WagerTransactionRepository) WakeUpWaitingFor(ctx context.Context, providerID string, externalTransactionID string, now time.Time) (int64, error) {
	const statement = `
		UPDATE wager_transactions
		   SET next_attempt_at = $3, updated_at = $3
		 WHERE id IN (
		       SELECT id FROM wager_transactions
		        WHERE status = 'PENDING_REFERENCE'
		          AND provider_id = $1
		          AND reference_external_transaction_id = $2
		          AND next_attempt_at > $3
		          FOR UPDATE SKIP LOCKED)`

	if providerID == "" || externalTransactionID == "" {
		return 0, nil
	}
	tag, err := r.queries.Exec(ctx, statement, providerID, externalTransactionID, now)
	if err != nil {
		return 0, Classify(err)
	}
	return tag.RowsAffected(), nil
}

func (r *WagerTransactionRepository) HasSuccessfulReversal(ctx context.Context, referenceTransactionID uuid.UUID) (bool, error) {
	const statement = `
		SELECT EXISTS (
		       SELECT 1 FROM wager_transactions
		        WHERE reference_transaction_id = $1
		          AND kind IN ('REFUND', 'ROLLBACK')
		          AND status = 'PROCESSED')`

	var exists bool
	if err := r.queries.QueryRow(ctx, statement, referenceTransactionID).Scan(&exists); err != nil {
		return false, Classify(err)
	}
	return exists, nil
}

func (r *WagerTransactionRepository) findOne(ctx context.Context, statement string, arguments ...any) (*wagering.Transaction, error) {
	operation, err := scanTransaction(r.queries.QueryRow(ctx, statement, arguments...))
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return operation, nil
}

func scanTransaction(row scanner) (*wagering.Transaction, error) {
	var (
		snapshot               wagering.Snapshot
		origin                 string
		kind                   string
		status                 string
		amountMinor            int64
		currencyCode           string
		providerID             *string
		externalTransactionID  *string
		idempotencyKey         *string
		payloadHash            *string
		roundID                *string
		gameID                 *string
		referenceExternalID    *string
		referenceTransactionID *uuid.UUID
		failureCode            *string
		resultBalanceMinor     *int64
		nextAttemptAt          *time.Time
		referenceDeadlineAt    *time.Time
		completedAt            *time.Time
		walletCurrencyCode     string
	)

	err := row.Scan(&snapshot.ID, &origin, &kind, &status, &snapshot.WalletID, &snapshot.PlayerID,
		&amountMinor, &currencyCode, &providerID, &externalTransactionID, &idempotencyKey, &payloadHash,
		&roundID, &gameID, &referenceExternalID, &referenceTransactionID, &failureCode, &resultBalanceMinor,
		&snapshot.ReferenceAttempts, &nextAttemptAt, &referenceDeadlineAt, &snapshot.CorrelationID,
		&snapshot.CreatedAt, &snapshot.UpdatedAt, &completedAt, &walletCurrencyCode)
	if err != nil {
		if isNoRows(err) {
			return nil, err
		}
		return nil, Classify(err)
	}

	currency, err := money.ParseCurrency(currencyCode)
	if err != nil {
		return nil, err
	}
	amount, err := money.FromMinor(amountMinor, currency)
	if err != nil {
		return nil, err
	}

	snapshot.Origin = wagering.Origin(origin)
	snapshot.Kind = wagering.Kind(kind)
	snapshot.Status = wagering.Status(status)
	snapshot.Amount = amount
	snapshot.ProviderID = text(providerID)
	snapshot.ExternalTransactionID = text(externalTransactionID)
	snapshot.IdempotencyKey = text(idempotencyKey)
	snapshot.PayloadHash = text(payloadHash)
	snapshot.RoundID = text(roundID)
	snapshot.GameID = text(gameID)
	snapshot.ReferenceExternalID = text(referenceExternalID)
	snapshot.FailureCode = wagering.FailureCode(text(failureCode))
	snapshot.NextAttemptAt = moment(nextAttemptAt)
	snapshot.ReferenceDeadlineAt = moment(referenceDeadlineAt)
	snapshot.CompletedAt = moment(completedAt)
	if referenceTransactionID != nil {
		snapshot.ReferenceTransactionID = *referenceTransactionID
	}

	if resultBalanceMinor != nil {
		walletCurrency, err := money.ParseCurrency(walletCurrencyCode)
		if err != nil {
			return nil, err
		}
		resultBalance, err := money.FromMinor(*resultBalanceMinor, walletCurrency)
		if err != nil {
			return nil, err
		}
		snapshot.ResultBalance = resultBalance
	}

	return wagering.Rehydrate(snapshot)
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func moment(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func nullString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullUUID(value uuid.UUID) *uuid.UUID {
	if value == uuid.Nil {
		return nil
	}
	return &value
}

func nullTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func nullMoney(value money.Money) *int64 {
	if !value.Valid() {
		return nil
	}
	minor := value.Minor()
	return &minor
}
