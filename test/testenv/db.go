//go:build integration

package testenv

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB gives tests the runtime pool (the wallet_app role, exactly what the service
// uses) and, only for schema regression tests, a pool owning the tables.
type DB struct {
	t     *testing.T
	suite *suite
	pool  *pgxpool.Pool
	owner *pgxpool.Pool
	name  string
}

func newDB(t *testing.T, booted *suite, database string) *DB {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	configuration, err := pgxpool.ParseConfig(booted.appDSN(database))
	if err != nil {
		t.Fatalf("test pool could not be configured: %v", err)
	}
	configuration.MaxConns = 5

	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		t.Fatalf("test pool could not be created: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("test database is not reachable: %v", err)
	}
	return &DB{t: t, suite: booted, pool: pool, name: database}
}

func (d *DB) Pool() *pgxpool.Pool {
	return d.pool
}

// Owner opens a pool that owns the tables. Only schema regression tests need it;
// every behavioural test must go through Pool, which has the runtime privileges.
func (d *DB) Owner() *pgxpool.Pool {
	d.t.Helper()
	if d.owner != nil {
		return d.owner
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, d.suite.ownerDSN(d.name))
	if err != nil {
		d.t.Fatalf("owner pool could not be created: %v", err)
	}
	d.t.Cleanup(pool.Close)
	d.owner = pool
	return pool
}

func (d *DB) URL() string {
	return d.suite.appDSN(d.name)
}

func (d *DB) Value(query string, arguments ...any) string {
	d.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var value string
	if err := d.pool.QueryRow(ctx, query, arguments...).Scan(&value); err != nil {
		d.t.Fatalf("query %q failed: %v", query, err)
	}
	return value
}

func (d *DB) Count(query string, arguments ...any) int64 {
	d.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var value int64
	if err := d.pool.QueryRow(ctx, query, arguments...).Scan(&value); err != nil {
		d.t.Fatalf("query %q failed: %v", query, err)
	}
	return value
}

func (d *DB) Text(query string, arguments ...any) string {
	d.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var value string
	if err := d.pool.QueryRow(ctx, query, arguments...).Scan(&value); err != nil {
		d.t.Fatalf("query %q failed: %v", query, err)
	}
	return value
}

// Attempt runs a script as the runtime role inside one transaction and reports the
// error, so a regression test can state which protection refused the write. The
// script may carry several statements, which is how the schema is exercised.
func (d *DB) Attempt(script string) error {
	d.t.Helper()
	return attempt(d.pool, script)
}

// AttemptAsOwner runs the same kind of script as the role that owns the tables, to
// prove the protections hold even there.
func (d *DB) AttemptAsOwner(script string) error {
	d.t.Helper()
	return attempt(d.Owner(), script)
}

func attempt(pool *pgxpool.Pool, script string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	transaction, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if _, err := transaction.Exec(ctx, script, pgx.QueryExecModeSimpleProtocol); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

// WithoutWalletGuards turns the wallet protections off for a moment so a test can
// forge the divergence reconciliation is supposed to catch. Nothing else may use it.
func (d *DB) WithoutWalletGuards(forge func(owner *pgxpool.Pool) error) {
	d.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	owner := d.Owner()
	if _, err := owner.Exec(ctx, "ALTER TABLE wallets DISABLE TRIGGER USER"); err != nil {
		d.t.Fatalf("wallet guards could not be turned off: %v", err)
	}
	defer func() {
		if _, err := owner.Exec(ctx, "ALTER TABLE wallets ENABLE TRIGGER USER"); err != nil {
			d.t.Fatalf("wallet guards could not be turned back on: %v", err)
		}
	}()

	if err := forge(owner); err != nil {
		d.t.Fatalf("the divergence could not be forced: %v", err)
	}
}
