package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	sqlrepositories "github.com/matheusj989/wagering-wallet-service/internal/infrastructure/postgres/repositories"
)

type Settings struct {
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	RetryAttempts    int
}

// UnitOfWork is the transaction boundary. Do opens one transaction, hands the
// caller the repositories bound to it, and commits when the closure returns nil
// or rolls back when it returns an error. Nothing outside this type decides when
// to commit, which is why a rejected operation is still a successful commit: the
// rejection is a fact worth persisting.
type UnitOfWork struct {
	client   *Client
	settings Settings
}

func NewUnitOfWork(client *Client, settings Settings) *UnitOfWork {
	if settings.RetryAttempts < 1 {
		settings.RetryAttempts = 1
	}
	return &UnitOfWork{client: client, settings: settings}
}

func (u *UnitOfWork) Do(ctx context.Context, fn func(context.Context, repositories.Registry) error) error {
	options := pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite}

	var lastErr error
	for attempt := 1; attempt <= u.settings.RetryAttempts; attempt++ {
		lastErr = u.run(ctx, options, fn)
		if lastErr == nil {
			return nil
		}
		if !sqlrepositories.Retryable(lastErr) || attempt == u.settings.RetryAttempts {
			return lastErr
		}
		if err := pause(ctx); err != nil {
			return err
		}
	}
	return lastErr
}

func (u *UnitOfWork) Read(ctx context.Context, fn func(context.Context, repositories.Registry) error) error {
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	return u.run(ctx, options, fn)
}

func (u *UnitOfWork) run(ctx context.Context, options pgx.TxOptions, fn func(context.Context, repositories.Registry) error) (resultErr error) {
	transaction, err := u.client.pool.BeginTx(ctx, options)
	if err != nil {
		return sqlrepositories.Classify(err)
	}

	committed := false
	commitStarted := false
	defer func() {
		recovered := recover()
		var rollbackErr error
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
			defer cancel()
			rollbackErr = transaction.Rollback(rollbackCtx)
		}
		if recovered != nil {
			switch {
			case commitStarted:
				resultErr = repositories.ErrCommitOutcomeUnknown
			case rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed):
				resultErr = fmt.Errorf("%w: rollback after panic could not be confirmed", repositories.ErrTransient)
			default:
				resultErr = repositories.ErrRecoveredPanic
			}
		}
	}()

	if err := u.applyTimeouts(ctx, transaction, options.AccessMode); err != nil {
		return sqlrepositories.Classify(err)
	}

	if err := fn(ctx, sqlrepositories.NewRegistry(transaction)); err != nil {
		return err
	}

	commitStarted = true
	if err := transaction.Commit(ctx); err != nil {
		return sqlrepositories.ClassifyCommit(err)
	}
	committed = true
	return nil
}

func (u *UnitOfWork) applyTimeouts(ctx context.Context, transaction pgx.Tx, mode pgx.TxAccessMode) error {
	statement := fmt.Sprintf("SET LOCAL statement_timeout = %d", u.settings.StatementTimeout.Milliseconds())
	if _, err := transaction.Exec(ctx, statement); err != nil {
		return err
	}
	if mode == pgx.ReadOnly {
		return nil
	}
	lock := fmt.Sprintf("SET LOCAL lock_timeout = %d", u.settings.LockTimeout.Milliseconds())
	_, err := transaction.Exec(ctx, lock)
	return err
}

const rollbackTimeout = 5 * time.Second

func pause(ctx context.Context) error {
	delay := time.Duration(minimumPause+rand.IntN(pauseSpread)) * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

const (
	minimumPause = 10
	pauseSpread  = 41
)

var _ repositories.UnitOfWork = (*UnitOfWork)(nil)
