//go:build integration

package integration_test

import (
	"testing"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestHarness(t *testing.T) {
	t.Run("Given the shared infrastructure/When a test asks for its own world/Then database, queues, tokens and processes are ready", func(t *testing.T) {
		// Given
		env := testenv.New(t)

		// When
		apps := env.StartApp(1)
		token := env.Auth.Token(testenv.ClientInternal)

		// Then
		if len(apps) != 1 || apps[0].BaseURL == "" {
			t.Fatalf("expected one running instance, got %#v", apps)
		}
		if token == "" {
			t.Fatal("expected a real token from the identity provider")
		}
		claims := env.Auth.Claims(token)
		if claims["iss"] != "http://localhost:8180/realms/wallet" {
			t.Errorf("issuer = %v, want the fixed public issuer", claims["iss"])
		}
		expected := []string{"inbox_messages", "outbox_events", "wager_transactions", "wallet_ledger_entries", "wallets"}
		for _, table := range expected {
			if found := env.DB.Count("SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename = $1", table); found != 1 {
				t.Errorf("table %s is missing from the test database", table)
			}
		}
		status, body := apps[0].Request("GET", "/health/ready", "", nil)
		if status != 200 {
			t.Errorf("readiness = %d, want 200 (%v)", status, body)
		}
	})
}
