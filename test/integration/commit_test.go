//go:build integration

package integration_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestUncertainCommit(t *testing.T) {
	t.Run("Given a commit the database already applied/When its answer is lost/Then the service admits the uncertainty and the retry replays", func(t *testing.T) {
		// Given
		env := testenv.New(t)
		proxy := env.StartDatabaseProxy()
		apps := env.StartApp(1,
			testenv.WithSetting("DATABASE_URL", proxy.URL()),
			testenv.WithoutOutboxPublisher(),
			testenv.WithoutReferenceWorker(),
			testenv.WithoutConsumer())

		w := &world{t: t, env: env, apps: apps, playerID: newPlayerID(), round: "round-commit"}
		w.openWallet("1000.00")
		external := w.nextID("bet")
		key := "key-" + external
		request := w.wager("BET", external, "25.00", "")

		// When
		proxy.CutAfter("COMMIT")
		status, body := w.submit(key, request)

		// Then
		requireStatus(t, "bet whose commit answer was lost", status, 503, body)
		requireField(t, "bet whose commit answer was lost", body, "code", "COMMIT_OUTCOME_UNKNOWN")
		if proxy.Cuts() != 1 {
			t.Fatalf("the proxy cut %d answers, want exactly one", proxy.Cuts())
		}

		// the database kept the operation even though the service could not tell
		if settled := env.DB.Count(
			"SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1 AND status = 'PROCESSED'",
			external); settled != 1 {
			t.Fatalf("the operation should be durable after the commit, found %d", settled)
		}

		// repeating the very same call is the documented recovery
		testenv.Eventually(t, 20*time.Second, "the pool recovers from the cut connection", func() (bool, string) {
			retryStatus, retry := w.submit(key, request)
			if retryStatus != 201 {
				return false, fmt.Sprintf("the retry answered %d: %v", retryStatus, retry)
			}
			if !boolean(t, retry, "idempotentReplay") {
				t.Fatal("the retry of an uncertain commit must be a replay, never a second movement")
			}
			if amountOf(t, retry, "balance") != "975.00" {
				t.Fatalf("replay balance = %s, want 975.00", amountOf(t, retry, "balance"))
			}
			return true, ""
		})

		if debits := env.DB.Count(
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.walletID); debits != 1 {
			t.Errorf("debits = %d, want a single one", debits)
		}
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given an attempt that never reached commit/When the connection drops/Then nothing is persisted and the retry processes once", func(t *testing.T) {
		// Given
		env := testenv.New(t)
		proxy := env.StartDatabaseProxy()
		apps := env.StartApp(1,
			testenv.WithSetting("DATABASE_URL", proxy.URL()),
			testenv.WithoutOutboxPublisher(),
			testenv.WithoutReferenceWorker(),
			testenv.WithoutConsumer())

		w := &world{t: t, env: env, apps: apps, playerID: newPlayerID(), round: "round-abort"}
		w.openWallet("1000.00")
		external := w.nextID("bet")
		key := "key-" + external
		request := w.wager("BET", external, "25.00", "")

		// When
		proxy.CutAfter("INSERT")
		status, body := w.submit(key, request)

		// Then
		if status != 503 {
			t.Fatalf("an attempt cut before commit should answer 503, got %d: %v", status, body)
		}
		requireField(t, "attempt cut before commit", body, "code", "SERVICE_UNAVAILABLE")
		if rows := env.DB.Count("SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", external); rows != 0 {
			t.Fatalf("an aborted attempt persisted %d rows, want none", rows)
		}

		// the same call now goes through as a brand new operation
		testenv.Eventually(t, 20*time.Second, "the retry processes the operation", func() (bool, string) {
			retryStatus, retry := w.submit(key, request)
			if retryStatus != 201 {
				return false, fmt.Sprintf("the retry answered %d: %v", retryStatus, retry)
			}
			if boolean(t, retry, "idempotentReplay") {
				t.Fatal("nothing was persisted, so the retry is a new operation")
			}
			return true, ""
		})
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given an opening whose commit answer was lost/When the same wallet is opened again/Then the conflict hands back the existing wallet", func(t *testing.T) {
		// Given
		env := testenv.New(t)
		proxy := env.StartDatabaseProxy()
		apps := env.StartApp(1,
			testenv.WithSetting("DATABASE_URL", proxy.URL()),
			testenv.WithoutOutboxPublisher(),
			testenv.WithoutReferenceWorker(),
			testenv.WithoutConsumer())
		player := newPlayerID()

		// When
		proxy.CutAfter("COMMIT")
		status, body := apps[0].Request("POST", "/wallets", env.Auth.Token(testenv.ClientInternal), map[string]any{
			"playerId":       player,
			"initialBalance": money("500.00"),
		})

		// Then
		requireStatus(t, "opening whose commit answer was lost", status, 503, body)
		requireField(t, "opening whose commit answer was lost", body, "code", "COMMIT_OUTCOME_UNKNOWN")

		var walletID string
		testenv.Eventually(t, 20*time.Second, "the repeated opening reports the existing wallet", func() (bool, string) {
			retryStatus, retry := apps[0].Request("POST", "/wallets", env.Auth.Token(testenv.ClientInternal), map[string]any{
				"playerId":       player,
				"initialBalance": money("500.00"),
			})
			if retryStatus != 409 {
				return false, fmt.Sprintf("the repeated opening answered %d: %v", retryStatus, retry)
			}
			walletID = optionalText(retry, "walletId")
			return walletID != "", "the conflict did not name the wallet"
		})

		readStatus, opened := apps[0].Request("GET", "/wallets/"+walletID, env.Auth.Token(testenv.ClientInternal), nil)
		requireStatus(t, "reading the recovered wallet", readStatus, 200, opened)
		if amountOf(t, opened, "balance") != "500.00" {
			t.Errorf("balance = %s, want the initial 500.00 applied once", amountOf(t, opened, "balance"))
		}
		if openings := env.DB.Count(
			"SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING'", walletID); openings != 1 {
			t.Errorf("openings = %d, the initial balance must not be applied twice", openings)
		}
	})
}
