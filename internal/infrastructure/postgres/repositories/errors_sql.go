package repositories

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	contracts "github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
)

var errRetryable = errors.New("postgres: the transaction aborted and can be replayed")

// Classify turns a driver error raised while the transaction was still open into
// the vocabulary the application understands. Nothing here happened after COMMIT,
// so every failure means the attempt did not persist.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgerrcode.SerializationFailure, pgerrcode.DeadlockDetected:
			return fmt.Errorf("%w: %w: %s", contracts.ErrTransient, errRetryable, pgErr.Message)
		case pgerrcode.LockNotAvailable:
			return fmt.Errorf("%w: %s", contracts.ErrLockTimeout, pgErr.Message)
		case pgerrcode.QueryCanceled, pgerrcode.AdminShutdown, pgerrcode.CrashShutdown, pgerrcode.CannotConnectNow:
			return fmt.Errorf("%w: %s", contracts.ErrTransient, pgErr.Message)
		default:
			return err
		}
	}
	return fmt.Errorf("%w: %w", contracts.ErrTransient, err)
}

// ClassifyCommit is the one place that may not assume a rollback. Only an error
// reported by the server proves the transaction aborted; losing the connection
// while waiting for the answer leaves the outcome unknown, and the caller has to
// recover through idempotency instead of retrying blindly.
func ClassifyCommit(err error) error {
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return Classify(err)
	}
	return fmt.Errorf("%w: %w", contracts.ErrCommitOutcomeUnknown, err)
}

func Retryable(err error) bool {
	return errors.Is(err, errRetryable)
}

func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == code
	}
	return false
}

func constraintIs(err error, name string) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName == name
	}
	return false
}

var errNoRows = pgx.ErrNoRows

func isNoRows(err error) bool {
	return errors.Is(err, errNoRows)
}
