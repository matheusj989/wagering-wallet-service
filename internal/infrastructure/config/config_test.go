package config_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/config"
)

func TestLoad(t *testing.T) {
	t.Run("Given only the required variables/When configuration is loaded/Then documented defaults are applied", func(t *testing.T) {
		// Given
		useBaseline(t)

		// When
		cfg, err := config.Load()

		// Then
		if err != nil {
			t.Fatalf("expected configuration to load, got %v", err)
		}
		if cfg.HTTP.Addr != ":8080" {
			t.Errorf("HTTP.Addr = %q, want \":8080\"", cfg.HTTP.Addr)
		}
		if cfg.HTTP.WriteConcurrency != 12 {
			t.Errorf("HTTP.WriteConcurrency = %d, want 12", cfg.HTTP.WriteConcurrency)
		}
		if cfg.Database.MaxConns != 20 || cfg.Database.MinConns != 2 {
			t.Errorf("pool = %d/%d, want 20/2", cfg.Database.MaxConns, cfg.Database.MinConns)
		}
		if cfg.Consumer.Workers != 4 || !cfg.Consumer.Enabled {
			t.Errorf("consumer = %d workers, enabled %v, want 4 and true", cfg.Consumer.Workers, cfg.Consumer.Enabled)
		}
		if cfg.Reference.TTL != 15*time.Minute {
			t.Errorf("Reference.TTL = %s, want 15m", cfg.Reference.TTL)
		}
		if cfg.App.InstanceID == "" {
			t.Error("App.InstanceID should fall back to host and pid")
		}
	})

	t.Run("Given values for every variable/When configuration is loaded/Then the environment overrides the defaults", func(t *testing.T) {
		// Given
		useBaseline(t)
		t.Setenv("HTTP_ADDR", "127.0.0.1:0")
		t.Setenv("INSTANCE_ID", "api-1")
		t.Setenv("LOG_LEVEL", "debug")
		t.Setenv("DB_POOL_MAX_CONNS", "10")
		t.Setenv("DB_POOL_MIN_CONNS", "0")
		t.Setenv("DB_POOL_RESERVED", "1")
		t.Setenv("HTTP_WRITE_CONCURRENCY", "6")
		t.Setenv("SQS_CONSUMER_WORKERS", "2")
		t.Setenv("OUTBOX_PUBLISHER_ENABLED", "false")
		t.Setenv("REFERENCE_TTL", "5s")
		t.Setenv("REFERENCE_BACKOFF_BASE", "200ms")

		// When
		cfg, err := config.Load()

		// Then
		if err != nil {
			t.Fatalf("expected configuration to load, got %v", err)
		}
		if cfg.HTTP.Addr != "127.0.0.1:0" || cfg.App.InstanceID != "api-1" || cfg.App.LogLevel != "debug" {
			t.Errorf("overrides not applied: %+v", cfg.App)
		}
		if cfg.Database.MaxConns != 10 || cfg.Database.MinConns != 0 {
			t.Errorf("pool = %d/%d, want 10/0", cfg.Database.MaxConns, cfg.Database.MinConns)
		}
		if cfg.Outbox.Enabled {
			t.Error("Outbox.Enabled should be false")
		}
		if budget := cfg.ConnectionBudget(); budget.Demand() != 10 || !budget.Fits() {
			t.Errorf("budget = %s, want it to fit the smaller pool", budget)
		}
		if cfg.Reference.TTL != 5*time.Second {
			t.Errorf("Reference.TTL = %s, want 5s", cfg.Reference.TTL)
		}
	})

	t.Run("Given a variable set to nothing/When configuration is loaded/Then the default is used", func(t *testing.T) {
		// Given
		useBaseline(t)
		t.Setenv("INSTANCE_ID", "")
		t.Setenv("LOG_LEVEL", "  ")
		t.Setenv("HTTP_WRITE_CONCURRENCY", "")

		// When
		cfg, err := config.Load()

		// Then
		if err != nil {
			t.Fatalf("an empty value means the variable was not set, got %v", err)
		}
		if cfg.App.InstanceID == "" {
			t.Error("InstanceID should fall back to host and pid")
		}
		if cfg.App.LogLevel != "info" || cfg.HTTP.WriteConcurrency != 12 {
			t.Errorf("defaults not applied: level %q, writes %d", cfg.App.LogLevel, cfg.HTTP.WriteConcurrency)
		}
	})

	t.Run("Given an invalid value/When configuration is loaded/Then the error names the variable", func(t *testing.T) {
		scenarios := []struct {
			name     string
			variable string
			value    string
		}{
			{"integer expected", "DB_POOL_MAX_CONNS", "abc"},
			{"positive integer expected", "HTTP_WRITE_CONCURRENCY", "0"},
			{"negative integer rejected", "SQS_CONSUMER_WORKERS", "-1"},
			{"duration expected", "SHUTDOWN_TIMEOUT", "quinze"},
			{"boolean expected", "SQS_CONSUMER_ENABLED", "maybe"},
			{"known log level expected", "LOG_LEVEL", "verbose"},
			{"absolute url expected", "SQS_ENDPOINT", "localhost:4566"},
			{"required value missing", "SQS_WAGER_QUEUE_URL", ""},
			{"message deadline below visibility", "SQS_MESSAGE_DEADLINE", "45s"},
			{"lock timeout below statement timeout", "DB_LOCK_TIMEOUT", "30s"},
			{"pool bounds", "DB_POOL_MIN_CONNS", "999"},
			{"budget above the pool", "HTTP_WRITE_CONCURRENCY", "20"},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				useBaseline(t)
				t.Setenv(scenario.variable, scenario.value)

				// When
				cfg, err := config.Load()

				// Then
				if err == nil {
					t.Fatalf("expected %s=%q to be rejected, got %+v", scenario.variable, scenario.value, cfg)
				}
				if !strings.Contains(err.Error(), scenario.variable) {
					t.Errorf("error %q should name %s", err.Error(), scenario.variable)
				}
			})
		}
	})

	t.Run("Given several invalid values/When configuration is loaded/Then every problem is reported at once", func(t *testing.T) {
		// Given
		useBaseline(t)
		t.Setenv("DB_POOL_MAX_CONNS", "abc")
		t.Setenv("SQS_WAIT_TIME", "sempre")

		// When
		_, err := config.Load()

		// Then
		if err == nil {
			t.Fatal("expected configuration to be rejected")
		}
		for _, variable := range []string{"DB_POOL_MAX_CONNS", "SQS_WAIT_TIME"} {
			if !strings.Contains(err.Error(), variable) {
				t.Errorf("error %q should name %s", err.Error(), variable)
			}
		}
	})
}

func useBaseline(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if isConfigured(key) {
			t.Setenv(key, "")
			os.Unsetenv(key)
		}
	}
	t.Setenv("SQS_WAGER_QUEUE_URL", "http://localhost:4566/000000000000/wager-transactions.fifo")
	t.Setenv("SQS_WAGER_DLQ_URL", "http://localhost:4566/000000000000/wager-transactions-dlq.fifo")
	t.Setenv("SQS_EVENTS_QUEUE_URL", "http://localhost:4566/000000000000/wallet-events.fifo")
}

func isConfigured(key string) bool {
	prefixes := []string{"APP_", "INSTANCE_", "LOG_", "HTTP_", "SHUTDOWN_", "DATABASE_", "DB_", "AWS_", "SQS_", "OUTBOX_", "REFERENCE_", "OIDC_"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func TestConnectionBudget(t *testing.T) {
	t.Run("Given the defaults/When the budget is calculated/Then every component fits the pool exactly", func(t *testing.T) {
		// Given
		useBaseline(t)

		// When
		cfg, err := config.Load()

		// Then
		if err != nil {
			t.Fatalf("the defaults should load, got %v", err)
		}
		budget := cfg.ConnectionBudget()
		if budget.HTTPWrites != 12 || budget.Consumer != 4 || budget.Jobs != 2 || budget.Reserved != 2 {
			t.Errorf("budget = %s", budget)
		}
		if budget.Demand() != 20 || !budget.Fits() {
			t.Errorf("budget = %s, want 20 of 20", budget)
		}
	})

	t.Run("Given a component that is turned off/When the budget is calculated/Then its slice is released", func(t *testing.T) {
		scenarios := []struct {
			name     string
			variable string
			demand   int
		}{
			{"consumer off", "SQS_CONSUMER_ENABLED", 16},
			{"outbox publisher off", "OUTBOX_PUBLISHER_ENABLED", 19},
			{"reference worker off", "REFERENCE_WORKER_ENABLED", 19},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				useBaseline(t)
				t.Setenv(scenario.variable, "false")

				// When
				cfg, err := config.Load()

				// Then
				if err != nil {
					t.Fatalf("the configuration should load, got %v", err)
				}
				if demand := cfg.ConnectionBudget().Demand(); demand != scenario.demand {
					t.Errorf("demand = %d, want %d", demand, scenario.demand)
				}
			})
		}
	})

	t.Run("Given a pool smaller than the demand/When configuration is loaded/Then the error shows the account", func(t *testing.T) {
		// Given
		useBaseline(t)
		t.Setenv("DB_POOL_MAX_CONNS", "10")

		// When
		_, err := config.Load()

		// Then
		if err == nil {
			t.Fatal("a pool below the declared demand should be refused")
		}
		for _, expected := range []string{"DB_POOL_MAX_CONNS", "12 writes", "4 consumer", "2 jobs", "2 reserved", "20 of 10"} {
			if !strings.Contains(err.Error(), expected) {
				t.Errorf("error = %v, want it to mention %q", err, expected)
			}
		}
	})

	t.Run("Given a smaller pool and smaller components/When configuration is loaded/Then it is accepted", func(t *testing.T) {
		// Given
		useBaseline(t)
		t.Setenv("DB_POOL_MAX_CONNS", "10")
		t.Setenv("HTTP_WRITE_CONCURRENCY", "4")
		t.Setenv("SQS_CONSUMER_WORKERS", "2")

		// When
		cfg, err := config.Load()

		// Then
		if err != nil {
			t.Fatalf("a budget that fits should load, got %v", err)
		}
		if demand := cfg.ConnectionBudget().Demand(); demand != 10 {
			t.Errorf("demand = %d, want 10", demand)
		}
	})
}
