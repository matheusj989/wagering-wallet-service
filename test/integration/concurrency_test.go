//go:build integration

package integration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestConcurrency(t *testing.T) {
	t.Run("Given the same bet sent fifty times at once/When three instances race/Then there is one debit and forty nine replays", func(t *testing.T) {
		// Given
		w := newWorld(t, 3)
		w.openWallet("1000.00")
		external := w.nextID("bet")
		key := "key-" + external
		request := w.wager("BET", external, "25.00", "")

		// When
		type answer struct {
			status int
			body   map[string]any
		}
		answers := make([]answer, 50)
		var senders sync.WaitGroup
		for index := range answers {
			senders.Add(1)
			go func() {
				defer senders.Done()
				status, body := w.submitOn(w.app(index%3), testenv.ClientProviderA, key, request)
				answers[index] = answer{status: status, body: body}
			}()
		}
		senders.Wait()

		// Then
		replays, processed := 0, 0
		for _, got := range answers {
			if got.status != 201 {
				t.Fatalf("every answer should be 201, got %d: %v", got.status, got.body)
			}
			if boolean(t, got.body, "idempotentReplay") {
				replays++
			} else {
				processed++
			}
			if amountOf(t, got.body, "balance") != "975.00" {
				t.Errorf("balance = %s, want 975.00 in every answer", amountOf(t, got.body, "balance"))
			}
		}
		if processed != 1 || replays != 49 {
			t.Errorf("answers = %d processed and %d replays, want 1 and 49", processed, replays)
		}
		if rows := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE idempotency_key = $1", key); rows != 1 {
			t.Errorf("transactions with the key = %d, want 1", rows)
		}
		if debits := w.env.DB.Count(
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.walletID); debits != 1 {
			t.Errorf("debits = %d, want 1", debits)
		}
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given a wallet with one hundred/When two bets of eighty race on three instances/Then one wins and the other is rejected", func(t *testing.T) {
		// Given
		w := newWorld(t, 3)
		w.openWallet("100.00")
		first := w.nextID("bet")
		second := w.nextID("bet")

		// When
		statuses := make([]int, 2)
		bodies := make([]map[string]any, 2)
		var senders sync.WaitGroup
		senders.Add(2)
		go func() {
			defer senders.Done()
			statuses[0], bodies[0] = w.submitOn(w.app(0), testenv.ClientProviderA, "key-"+first, w.wager("BET", first, "80.00", ""))
		}()
		go func() {
			defer senders.Done()
			statuses[1], bodies[1] = w.submitOn(w.app(1), testenv.ClientProviderA, "key-"+second, w.wager("BET", second, "80.00", ""))
		}()
		senders.Wait()

		// Then
		accepted, refused := 0, 0
		for index, status := range statuses {
			switch status {
			case 201:
				accepted++
			case 422:
				refused++
				requireField(t, "the losing bet", bodies[index], "failureCode", "INSUFFICIENT_FUNDS")
			default:
				t.Fatalf("unexpected answer %d: %v", status, bodies[index])
			}
		}
		if accepted != 1 || refused != 1 {
			t.Fatalf("answers = %d accepted and %d refused, want one of each", accepted, refused)
		}

		balance, version := w.walletState()
		if balance != "20.00" || version != 2 {
			t.Errorf("wallet = %s at version %d, want 20.00 at version 2", balance, version)
		}
		if debits := w.env.DB.Count(
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.walletID); debits != 1 {
			t.Errorf("debits = %d, want 1", debits)
		}

		// resending both operations must not change the outcome
		w.submitOn(w.app(2), testenv.ClientProviderA, "key-"+first, w.wager("BET", first, "80.00", ""))
		w.submitOn(w.app(2), testenv.ClientProviderA, "key-"+second, w.wager("BET", second, "80.00", ""))
		w.requireConsistent("20.00", 2)
	})

	t.Run("Given twenty wallets/When they are bet on at the same time/Then they advance in parallel", func(t *testing.T) {
		// Given
		w := newWorld(t, 3)
		wallets := make([]string, 20)
		for index := range wallets {
			status, body := w.app(0).Request("POST", "/wallets", w.internalToken(), map[string]any{
				"playerId":       uuid.Must(uuid.NewV7()).String(),
				"initialBalance": money("100.00"),
			})
			requireStatus(t, "opening a wallet", status, 201, body)
			wallets[index] = text(t, body, "id")
		}

		// When
		started := time.Now()
		statuses := make([]int, len(wallets))
		var senders sync.WaitGroup
		for index, walletID := range wallets {
			senders.Add(1)
			go func() {
				defer senders.Done()
				external := w.nextID("bet")
				request := map[string]any{
					"providerId":            "provider-a",
					"externalTransactionId": external,
					"playerId":              w.env.DB.Value("SELECT player_id::text FROM wallets WHERE id = $1", walletID),
					"walletId":              walletID,
					"roundId":               w.round,
					"gameId":                "fortune-chimp",
					"kind":                  "BET",
					"money":                 money("25.00"),
				}
				statuses[index], _ = w.submitOn(w.app(index%3), testenv.ClientProviderA, "key-"+external, request)
			}()
		}
		senders.Wait()
		elapsed := time.Since(started)

		// Then
		for index, status := range statuses {
			if status != 201 {
				t.Errorf("wallet %d answered %d, want 201", index, status)
			}
		}
		if elapsed > 10*time.Second {
			t.Errorf("twenty independent wallets took %s, they should not queue behind each other", elapsed)
		}
		for _, walletID := range wallets {
			if balance := w.env.DB.Value("SELECT balance_minor::text FROM wallets WHERE id = $1", walletID); balance != "7500" {
				t.Errorf("wallet %s ended at %s, want 7500", walletID, balance)
			}
		}
	})

	t.Run("Given a full write limiter/When another write arrives/Then it is shed with Retry-After", func(t *testing.T) {
		// Given
		w := newWorld(t, 1,
			testenv.WithSetting("HTTP_WRITE_CONCURRENCY", "1"),
			testenv.WithSetting("HTTP_WRITE_QUEUE_TIMEOUT", "150ms"),
			testenv.WithSetting("DB_LOCK_TIMEOUT", "4s"))
		w.openWallet("100000.00")

		holder, err := w.env.DB.Pool().Begin(context.Background())
		if err != nil {
			t.Fatalf("the blocking transaction could not start: %v", err)
		}
		if _, err := holder.Exec(context.Background(),
			"SELECT balance_minor FROM wallets WHERE id = $1 FOR NO KEY UPDATE", w.walletID); err != nil {
			t.Fatalf("the wallet could not be locked: %v", err)
		}
		defer func() { _ = holder.Rollback(context.Background()) }()

		// When
		statuses := make([]int, 12)
		retryAfter := make([]string, 12)
		var senders sync.WaitGroup
		for index := range statuses {
			senders.Add(1)
			go func() {
				defer senders.Done()
				external := w.nextID("bet")
				status, _, header := w.app(0).Call("POST", "/wagering/transactions", w.providerToken(testenv.ClientProviderA),
					w.wager("BET", external, "1.00", ""), "Idempotency-Key", "key-"+external)
				statuses[index] = status
				retryAfter[index] = header.Get("Retry-After")
			}()
		}
		senders.Wait()

		// Then
		shed := 0
		for index, status := range statuses {
			if status == 503 {
				shed++
				if retryAfter[index] != "1" {
					t.Errorf("a shed request answered without Retry-After: %q", retryAfter[index])
				}
			}
		}
		if shed == 0 {
			t.Fatalf("with a single slot and twelve writers at least one request should be shed, got %v", statuses)
		}
		if metrics := w.app(0).Metrics(); !metrics.Has("http_requests_shed_total", 1) {
			t.Errorf("shed requests should be counted:\n%s", metrics.Lines("http_requests_shed_total"))
		}
	})

	t.Run("Given a wallet held by another transaction/When a bet waits too long/Then it gives up without persisting", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithSetting("DB_LOCK_TIMEOUT", "300ms"))
		w.openWallet("100.00")

		holder, err := w.env.DB.Pool().Begin(context.Background())
		if err != nil {
			t.Fatalf("the blocking transaction could not start: %v", err)
		}
		if _, err := holder.Exec(context.Background(),
			"SELECT balance_minor FROM wallets WHERE id = $1 FOR NO KEY UPDATE", w.walletID); err != nil {
			t.Fatalf("the wallet could not be locked: %v", err)
		}

		// When
		external := w.nextID("bet")
		status, body := w.submit("key-"+external, w.wager("BET", external, "10.00", ""))
		_ = holder.Rollback(context.Background())

		// Then
		requireStatus(t, "bet behind a lock", status, 503, body)
		requireField(t, "bet behind a lock", body, "code", "SERVICE_UNAVAILABLE")
		if rows := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE idempotency_key = $1", "key-"+external); rows != 0 {
			t.Errorf("the refused attempt persisted %d rows, want none", rows)
		}
		if metrics := w.app(0).Metrics(); !metrics.Has("wallet_concurrency_conflicts_total", 1, "reason=lock_timeout") {
			t.Errorf("the lock timeout should be counted:\n%s", metrics.Lines("wallet_concurrency_conflicts_total"))
		}
		w.requireConsistent("100.00", 1)
	})
}
