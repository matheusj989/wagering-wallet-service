//go:build integration

package testenv

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var databaseCounter atomic.Int64

// Env is the isolated world of a single test: its own database created from the
// migrated template, its own FIFO queues, real tokens and real service processes.
type Env struct {
	t        *testing.T
	suite    *suite
	database string

	DB     *DB
	Queues *Queues
	Auth   *Auth
}

func New(t *testing.T) *Env {
	t.Helper()
	if current == nil {
		t.Fatal("testenv.Run must be called from TestMain before any test uses the environment")
	}

	env := &Env{t: t, suite: current}
	env.database = env.createDatabase()
	env.DB = newDB(t, current, env.database)
	env.Queues = newQueues(t, current, uniqueSuffix())
	env.Auth = newAuth(t, current.keycloakURL())
	return env
}

func (e *Env) createDatabase() string {
	e.t.Helper()
	name := fmt.Sprintf("wallet_test_%d_%d", time.Now().UnixNano()%1_000_000, databaseCounter.Add(1))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := e.suite.admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDatabase)); err != nil {
		e.t.Fatalf("test database could not be created: %v", err)
	}

	e.t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if _, err := e.suite.admin.Exec(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name)); err != nil {
			e.t.Logf("test database %s could not be dropped: %v", name, err)
		}
	})
	return name
}

func (e *Env) DatabaseName() string {
	return e.database
}

func uniqueSuffix() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano()%1_000_000_000, databaseCounter.Add(1))
}

// Eventually polls until the condition holds or the deadline passes, and fails the
// test with the last diagnosis instead of sleeping for a fixed time.
func Eventually(t *testing.T, timeout time.Duration, description string, condition func() (bool, string)) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	diagnosis := "the condition was never evaluated"

	for time.Now().Before(deadline) {
		satisfied, detail := condition()
		if satisfied {
			return
		}
		if detail != "" {
			diagnosis = detail
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s: %s", description, timeout, diagnosis)
}

func sanitise(text string) string {
	cleaned := strings.ReplaceAll(text, appPassword, "***")
	return strings.ReplaceAll(cleaned, ownerPassword, "***")
}

// ScratchDatabase hands a migration test an empty database of its own, so the up,
// down and up cycle never touches the databases the behavioural tests use.
func (e *Env) ScratchDatabase() (migrateURL string, adminURL string) {
	e.t.Helper()

	name := fmt.Sprintf("wallet_scratch_%d_%d", time.Now().UnixNano()%1_000_000, databaseCounter.Add(1))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := e.suite.admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		e.t.Fatalf("scratch database could not be created: %v", err)
	}
	e.t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if _, err := e.suite.admin.Exec(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name)); err != nil {
			e.t.Logf("scratch database %s could not be dropped: %v", name, err)
		}
	})

	return e.suite.migrateDSN(name), e.suite.ownerDSN(name)
}

// MigrationsPath is where the versioned migrations live, for the tests that apply
// and revert them.
func (e *Env) MigrationsPath() string {
	return filepath.Join(e.suite.root, "migrations")
}
