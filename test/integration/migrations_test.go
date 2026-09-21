//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestMigrations(t *testing.T) {
	t.Run("Given an empty database/When the migration is applied, reverted and applied again/Then the schema is the same and nothing is left behind", func(t *testing.T) {
		// Given
		env := testenv.New(t)
		migrateURL, adminURL := env.ScratchDatabase()

		ctx := context.Background()
		admin, err := pgxpool.New(ctx, adminURL)
		if err != nil {
			t.Fatalf("the scratch database is not reachable: %v", err)
		}
		t.Cleanup(admin.Close)

		count := func(query string) int64 {
			t.Helper()
			var found int64
			if err := admin.QueryRow(ctx, query).Scan(&found); err != nil {
				t.Fatalf("query %q failed: %v", query, err)
			}
			return found
		}

		const (
			tables    = "SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'"
			functions = "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public'"
			triggers  = "SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND NOT t.tgisinternal"
			indexes   = "SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND tablename <> 'schema_migrations'"
		)

		// When
		migrations, err := migrate.New("file://"+env.MigrationsPath(), migrateURL)
		if err != nil {
			t.Fatalf("the migrations could not be loaded: %v", err)
		}
		t.Cleanup(func() { _, _ = migrations.Close() })

		if err := migrations.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			t.Fatalf("the migration could not be applied: %v", err)
		}
		applied := map[string]int64{"tables": count(tables), "functions": count(functions), "triggers": count(triggers), "indexes": count(indexes)}

		if err := migrations.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			t.Fatalf("the migration could not be reverted: %v", err)
		}
		reverted := map[string]int64{"tables": count(tables), "functions": count(functions), "triggers": count(triggers)}

		if err := migrations.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			t.Fatalf("the migration could not be applied again: %v", err)
		}
		reapplied := map[string]int64{"tables": count(tables), "functions": count(functions), "triggers": count(triggers), "indexes": count(indexes)}

		// Then
		if applied["tables"] != 5 {
			t.Errorf("tables after the migration = %d, want 5", applied["tables"])
		}
		if applied["functions"] == 0 || applied["triggers"] == 0 || applied["indexes"] == 0 {
			t.Errorf("the migration should create functions, triggers and indexes, got %v", applied)
		}
		for name, remaining := range reverted {
			if remaining != 0 {
				t.Errorf("%s left behind after the revert = %d, want none", name, remaining)
			}
		}
		for name, expected := range applied {
			if reapplied[name] != expected {
				t.Errorf("%s after applying again = %d, want %d", name, reapplied[name], expected)
			}
		}
	})

	t.Run("Given the running database/When its version and data directory are read/Then they match the deployment", func(t *testing.T) {
		// Given
		env := testenv.New(t)

		// When
		version := env.DB.Value("SHOW server_version")
		var directory string
		if err := env.DB.Owner().QueryRow(context.Background(), "SHOW data_directory").Scan(&directory); err != nil {
			t.Fatalf("the data directory could not be read: %v", err)
		}

		// Then
		if !strings.HasPrefix(version, "18.") {
			t.Errorf("server version = %q, want the 18 series", version)
		}
		if directory != "/var/lib/postgresql/18/docker" {
			t.Errorf("data directory = %q, want the path the volume mounts", directory)
		}
	})
}

func TestComposition(t *testing.T) {
	t.Run("Given the real dependencies/When an instance starts and stops/Then every component reports its own shutdown", func(t *testing.T) {
		// Given
		env := testenv.New(t)
		apps := env.StartApp(1)

		// When
		status, body := apps[0].Request("GET", "/health/ready", "", nil)
		requireStatus(t, "readiness before stopping", status, 200, body)
		apps[0].Stop()

		// Then
		output := apps[0].Output()
		for _, expected := range []string{
			"http server listening",
			"http server stopped",
			"consumer stopped",
			"job stopped",
			"scheduler stopped",
			"database pool closed",
		} {
			if !strings.Contains(output, expected) {
				t.Errorf("the shutdown log should mention %q:\n%s", expected, tail(output, 25))
			}
		}
		if strings.Index(output, "http server stopped") > strings.Index(output, "database pool closed") {
			t.Error("the http server has to stop before the database pool closes")
		}
		if apps[0].Running() {
			t.Error("the instance should have stopped")
		}
	})

	t.Run("Given a dependency that is not reachable/When the instance starts/Then it refuses to come up", func(t *testing.T) {
		// Given
		env := testenv.New(t)

		// When
		outcome := env.StartFailingApp(testenv.WithSetting("DATABASE_URL",
			"postgres://wallet_app:wallet_app@127.0.0.1:1/nothing?sslmode=disable"))

		// Then
		if outcome.ExitCode == 0 {
			t.Errorf("the process should not start without its database, it exited with %d", outcome.ExitCode)
		}
		if !strings.Contains(outcome.Output, "database is not reachable") {
			t.Errorf("the failure should name the dependency:\n%s", tail(outcome.Output, 15))
		}
	})

	t.Run("Given a queue that does not exist/When the instance starts/Then it refuses to come up", func(t *testing.T) {
		// Given
		env := testenv.New(t)

		// When
		outcome := env.StartFailingApp(testenv.WithSetting("SQS_WAGER_QUEUE_URL",
			strings.Replace(env.Queues.Wager, ".fifo", "-missing.fifo", 1)))

		// Then
		if outcome.ExitCode == 0 {
			t.Errorf("the process should not start without its queue, it exited with %d", outcome.ExitCode)
		}
		if !strings.Contains(outcome.Output, "queue wager-transactions is not reachable") {
			t.Errorf("the failure should name the queue:\n%s", tail(outcome.Output, 15))
		}
	})

	t.Run("Given a pool smaller than what the components hold/When the instance starts/Then it refuses with the account", func(t *testing.T) {
		// Given
		env := testenv.New(t)

		// When
		outcome := env.StartFailingApp(testenv.WithSetting("DB_POOL_MAX_CONNS", "8"))

		// Then
		if outcome.ExitCode == 0 {
			t.Errorf("the process should not start over its pool, it exited with %d", outcome.ExitCode)
		}
		for _, expected := range []string{"DB_POOL_MAX_CONNS", "writes", "consumer", "jobs", "reserved", "of 8"} {
			if !strings.Contains(outcome.Output, expected) {
				t.Errorf("the failure should show the account, missing %q:\n%s", expected, tail(outcome.Output, 15))
			}
		}
	})

	t.Run("Given a pool that fits smaller components/When the instance starts/Then it comes up and logs the budget", func(t *testing.T) {
		// Given
		env := testenv.New(t)

		// When
		app := env.StartApp(1,
			testenv.WithSetting("DB_POOL_MAX_CONNS", "8"),
			testenv.WithSetting("HTTP_WRITE_CONCURRENCY", "3"),
			testenv.WithSetting("SQS_CONSUMER_WORKERS", "1"),
		)[0]

		// Then
		status, body := app.Request("GET", "/health/ready", "", nil)
		requireStatus(t, "readiness with a smaller pool", status, 200, body)
		if !strings.Contains(app.Output(), `"poolSize":8`) {
			t.Errorf("the startup log should carry the budget:\n%s", tail(app.Output(), 15))
		}
	})

	t.Run("Given the workers switched off/When the instance starts/Then only the api answers", func(t *testing.T) {
		// Given
		env := testenv.New(t)

		// When
		apps := env.StartApp(1,
			testenv.WithoutConsumer(),
			testenv.WithoutOutboxPublisher(),
			testenv.WithoutReferenceWorker())

		// Then
		status, body := apps[0].Request("GET", "/health/ready", "", nil)
		requireStatus(t, "readiness with workers off", status, 200, body)
		if strings.Contains(apps[0].Output(), "worker started") {
			t.Error("no worker should start when they are switched off")
		}
		apps[0].Stop()
	})
}
