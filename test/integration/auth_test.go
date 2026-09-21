//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestAuthentication(t *testing.T) {
	t.Run("Given the imported realm/When a client asks for a token/Then the claims carry the identity the api expects", func(t *testing.T) {
		// Given
		env := testenv.New(t)

		scenarios := []struct {
			client   string
			provider string
			role     string
		}{
			{testenv.ClientProviderA, "provider-a", "wagering-provider"},
			{testenv.ClientProviderB, "provider-b", "wagering-provider"},
			{testenv.ClientInternal, "", "wallet-internal"},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.client, func(t *testing.T) {
				// When
				claims := env.Auth.Claims(env.Auth.Token(scenario.client))

				// Then
				if claims["iss"] != "http://localhost:8180/realms/wallet" {
					t.Errorf("issuer = %v, want the fixed public issuer", claims["iss"])
				}
				if audience, _ := claims["aud"].(string); audience != "wallet-api" {
					t.Errorf("audience = %v, want wallet-api", claims["aud"])
				}
				if provider, _ := claims["provider_id"].(string); provider != scenario.provider {
					t.Errorf("provider_id = %v, want %q", claims["provider_id"], scenario.provider)
				}
				access, _ := claims["realm_access"].(map[string]any)
				roles, _ := access["roles"].([]any)
				found := false
				for _, role := range roles {
					if role == scenario.role {
						found = true
					}
				}
				if !found {
					t.Errorf("roles = %v, want %s", roles, scenario.role)
				}
			})
		}
	})

	t.Run("Given a credential the api cannot trust/When a business endpoint is called/Then it answers unauthenticated", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")
		valid := w.internalToken()

		scenarios := []struct {
			name  string
			token string
		}{
			{"no token", ""},
			{"not a jwt", "abc.def.ghi"},
			{"signed by another key", strings.Join([]string{
				"eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCIsImtpZCI6Im90aGVyIn0",
				"eyJpc3MiOiJodHRwOi8vbG9jYWxob3N0OjgxODAvcmVhbG1zL3dhbGxldCIsImF1ZCI6IndhbGxldC1hcGkifQ",
				"c2lnbmF0dXJl"}, ".")},
			{"truncated token", valid[:len(valid)-6]},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// When
				status, body := w.app(0).Request("GET", "/wallets/"+w.walletID, scenario.token, nil)

				// Then
				requireStatus(t, scenario.name, status, 401, body)
				requireField(t, scenario.name, body, "code", "UNAUTHENTICATED")
			})
		}
	})

	t.Run("Given a token that has expired/When it is used/Then the call is refused", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")
		token, lifetime := w.env.Auth.FreshToken(testenv.ClientShortLived)
		if lifetime > 10*time.Second {
			t.Fatalf("the short lived client should hand out a token of a few seconds, got %s", lifetime)
		}

		// When
		time.Sleep(lifetime + 2*time.Second)
		status, body := w.app(0).Request("GET", "/wagering/transactions/"+w.walletID, token, nil)

		// Then
		requireStatus(t, "expired token", status, 401, body)
		requireField(t, "expired token", body, "code", "UNAUTHENTICATED")
	})

	t.Run("Given the identity provider on an ephemeral port/When a token is verified/Then keys are fetched without the public host", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)

		// When
		status, body := w.app(0).Request("GET", "/health/ready", "", nil)
		opened := w.openWallet("10.00")

		// Then
		requireStatus(t, "readiness", status, 200, body)
		if opened["id"] == nil {
			t.Fatal("a real token validated against the ephemeral keycloak should authorise the call")
		}
		if strings.Contains(w.app(0).Output(), "localhost:8180") {
			t.Error("the service should reach the identity provider through the configured discovery and jwks urls")
		}
	})
}

func TestAuthorization(t *testing.T) {
	t.Run("Given the authorization matrix/When each role calls each route/Then only the documented pairs are allowed", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("bet")
		status, body := w.submit("key-"+external, w.wager("BET", external, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)
		transactionID := text(t, body, "transactionId")

		scenarios := []struct {
			name   string
			client string
			method string
			path   string
			body   map[string]any
			want   int
		}{
			{"provider opens a wallet", testenv.ClientProviderA, "POST", "/wallets", map[string]any{"playerId": w.playerID, "initialBalance": money("1.00")}, 403},
			{"provider reads a wallet", testenv.ClientProviderA, "GET", "/wallets/" + w.walletID, nil, 403},
			{"provider reads the ledger", testenv.ClientProviderA, "GET", "/wallets/" + w.walletID + "/ledger", nil, 403},
			{"provider reconciles", testenv.ClientProviderA, "POST", "/wallets/" + w.walletID + "/reconciliation", nil, 403},
			{"internal reads a wallet", testenv.ClientInternal, "GET", "/wallets/" + w.walletID, nil, 200},
			{"internal reads its own transaction", testenv.ClientInternal, "GET", "/wagering/transactions/" + transactionID, nil, 200},
			{"provider reads its own transaction", testenv.ClientProviderA, "GET", "/wagering/transactions/" + transactionID, nil, 200},
			{"other provider reads the transaction", testenv.ClientProviderB, "GET", "/wagering/transactions/" + transactionID, nil, 404},
			{"internal reads by another provider path", testenv.ClientInternal, "GET", "/providers/provider-a/wagering/transactions/" + external, nil, 200},
			{"provider reads its own provider path", testenv.ClientProviderA, "GET", "/providers/provider-a/wagering/transactions/" + external, nil, 200},
			{"other provider reads that path", testenv.ClientProviderB, "GET", "/providers/provider-a/wagering/transactions/" + external, nil, 403},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// When
				status, body := w.app(0).Request(scenario.method, scenario.path, w.providerToken(scenario.client), scenario.body)

				// Then
				requireStatus(t, scenario.name, status, scenario.want, body)
			})
		}
	})

	t.Run("Given the internal client/When it sends a provider operation/Then it is refused without writing", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")
		external := w.nextID("bet")

		// When
		status, body := w.submitOn(w.app(0), testenv.ClientInternal, "key-"+external, w.wager("BET", external, "10.00", ""))

		// Then
		requireStatus(t, "internal client sending an operation", status, 403, body)
		requireField(t, "internal client sending an operation", body, "code", "FORBIDDEN")
		if rows := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", external); rows != 0 {
			t.Errorf("the refused call persisted %d rows, want none", rows)
		}
	})

	t.Run("Given the body of another provider/When it is replayed by a different credential/Then it is refused before idempotency", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("bet")
		key := "key-" + external
		request := w.wager("BET", external, "25.00", "")
		status, original := w.submit(key, request)
		requireStatus(t, "bet from provider a", status, 201, original)

		// When
		status, body := w.submitOn(w.app(0), testenv.ClientProviderB, key, request)

		// Then
		requireStatus(t, "copied body", status, 403, body)
		requireField(t, "copied body", body, "code", "FORBIDDEN")
		if optionalText(body, "transactionId") != "" {
			t.Error("a refused call must not expose the other provider's transaction")
		}
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given the public endpoints/When they are called without a credential/Then they still answer", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)

		// When
		liveStatus, live := w.app(0).Request("GET", "/health/live", "", nil)
		readyStatus, ready := w.app(0).Request("GET", "/health/ready", "", nil)
		metrics := w.app(0).Metrics()

		// Then
		requireStatus(t, "liveness", liveStatus, 200, live)
		requireField(t, "liveness", live, "status", "UP")
		requireStatus(t, "readiness", readyStatus, 200, ready)
		if !metrics.Has("http_request_duration_seconds_count", 1) {
			t.Errorf("metrics should be public and populated:\n%s", metrics.Lines("http_request_duration_seconds_count"))
		}
	})

	t.Run("Given a refused credential/When the request is logged/Then the token never appears", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("10.00")
		token := w.internalToken()

		// When
		status, body := w.app(0).Request("GET", "/wallets/"+w.walletID, token[:len(token)-4], nil)

		// Then
		requireStatus(t, "tampered token", status, 401, body)
		if strings.Contains(w.app(0).Output(), token[:40]) {
			t.Error("the service log must never carry the credential")
		}
	})
}

func TestSigningKeyRotation(t *testing.T) {
	t.Run("Given a running service with a cached signing key/When Keycloak rotates its RSA key/Then old and new tokens work without restarting", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")
		oldToken, _ := w.env.Auth.FreshToken(testenv.ClientInternal)
		status, body := w.app(0).Request("GET", "/wallets/"+w.walletID, oldToken, nil)
		requireStatus(t, "prime key cache", status, 200, body)
		// When
		w.env.Auth.RotateSigningKey()
		newToken, _ := w.env.Auth.FreshToken(testenv.ClientInternal)
		// Then
		if w.env.Auth.KeyID(oldToken) == w.env.Auth.KeyID(newToken) {
			t.Fatal("Keycloak did not rotate the signing key")
		}
		for _, token := range []string{newToken, oldToken} {
			status, body = w.app(0).Request("GET", "/wallets/"+w.walletID, token, nil)
			requireStatus(t, "token across rotation", status, 200, body)
		}
	})
}
