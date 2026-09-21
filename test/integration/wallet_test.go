//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestOpenWallet(t *testing.T) {
	t.Run("Given a positive initial balance/When the wallet is opened/Then wallet, opening, entry and events land in the same commit", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutOutboxPublisher())

		// When
		opened := w.openWallet("1000.00")

		// Then
		if amountOf(t, opened, "balance") != "1000.00" || number(t, opened, "version") != 1 {
			t.Errorf("wallet = %v, want 1000.00 at version 1", opened)
		}
		if openings := w.env.DB.Count(
			"SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING' AND status = 'PROCESSED' AND origin = 'INTERNAL'",
			w.walletID); openings != 1 {
			t.Errorf("opening transactions = %d, want 1", openings)
		}
		if entries := w.env.DB.Count(
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND wallet_version = 1 AND direction = 'CREDIT' AND balance_before_minor = 0 AND balance_after_minor = 100000",
			w.walletID); entries != 1 {
			t.Errorf("opening entries = %d, want 1", entries)
		}
		if events := w.env.DB.Count(
			"SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND event_type IN ('WagerTransactionProcessed', 'WalletBalanceChanged')",
			w.walletID); events != 2 {
			t.Errorf("outbox events = %d, want 2", events)
		}
		w.requireConsistent("1000.00", 1)
	})

	t.Run("Given a zero initial balance/When the wallet is opened/Then only the wallet exists", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutOutboxPublisher())

		// When
		opened := w.openWallet("0.00")

		// Then
		if amountOf(t, opened, "balance") != "0.00" || number(t, opened, "version") != 1 {
			t.Errorf("wallet = %v, want 0.00 at version 1", opened)
		}
		if rows := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE wallet_id = $1", w.walletID); rows != 0 {
			t.Errorf("transactions = %d, want none", rows)
		}
		if rows := w.env.DB.Count("SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", w.walletID); rows != 0 {
			t.Errorf("ledger entries = %d, want none", rows)
		}
		if rows := w.env.DB.Count("SELECT count(*) FROM outbox_events WHERE partition_key = $1", w.walletID); rows != 0 {
			t.Errorf("events = %d, want none", rows)
		}
	})

	t.Run("Given a wallet that already exists/When the same player and currency are opened again/Then the conflict names the existing wallet", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")

		// When
		status, body := w.app(0).Request("POST", "/wallets", w.internalToken(), map[string]any{
			"playerId":       w.playerID,
			"initialBalance": money("100.00"),
		})

		// Then
		requireStatus(t, "opening a duplicate wallet", status, 409, body)
		requireField(t, "duplicate wallet", body, "code", "WALLET_ALREADY_EXISTS")
		if optionalText(body, "walletId") != w.walletID {
			t.Errorf("walletId = %q, want %q", optionalText(body, "walletId"), w.walletID)
		}
		if wallets := w.env.DB.Count("SELECT count(*) FROM wallets WHERE player_id = $1", w.playerID); wallets != 1 {
			t.Errorf("wallets for the player = %d, want 1", wallets)
		}
	})

	t.Run("Given an unusable request/When a wallet is opened/Then it is refused before any write", func(t *testing.T) {
		scenarios := []struct {
			name string
			body map[string]any
		}{
			{"amount without two places", map[string]any{"playerId": uuid.Must(uuid.NewV7()).String(), "initialBalance": money("25")}},
			{"amount as a number", map[string]any{"playerId": uuid.Must(uuid.NewV7()).String(), "initialBalance": map[string]any{"amount": 25.0, "currency": "BRL"}}},
			{"currency outside the list", map[string]any{"playerId": uuid.Must(uuid.NewV7()).String(), "initialBalance": moneyIn("25.00", "JPY")}},
			{"player is not a uuid", map[string]any{"playerId": "not-a-uuid", "initialBalance": money("25.00")}},
			{"unknown field", map[string]any{"playerId": uuid.Must(uuid.NewV7()).String(), "initialBalance": money("25.00"), "nickname": "x"}},
		}

		w := newWorld(t, 1)
		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given, When
				status, body := w.app(0).Request("POST", "/wallets", w.internalToken(), scenario.body)

				// Then
				requireStatus(t, scenario.name, status, 400, body)
				requireField(t, scenario.name, body, "code", "VALIDATION_FAILED")
			})
		}
	})
}

func TestWalletReads(t *testing.T) {
	t.Run("Given a wallet with movements/When the ledger is paged/Then the cursor walks the versions in order", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		for _, amount := range []string{"25.00", "10.00"} {
			external := w.nextID("bet")
			status, body := w.submit("key-"+external, w.wager("BET", external, amount, ""))
			requireStatus(t, "bet "+amount, status, 201, body)
		}

		// When
		status, first := w.app(0).Request("GET", "/wallets/"+w.walletID+"/ledger?limit=2", w.internalToken(), nil)
		requireStatus(t, "first ledger page", status, 200, first)
		cursor := optionalText(first, "nextCursor")
		status, second := w.app(0).Request("GET", "/wallets/"+w.walletID+"/ledger?limit=2&cursor="+cursor, w.internalToken(), nil)

		// Then
		requireStatus(t, "second ledger page", status, 200, second)
		firstEntries, _ := first["entries"].([]any)
		secondEntries, _ := second["entries"].([]any)
		if len(firstEntries) != 2 || len(secondEntries) != 1 {
			t.Fatalf("pages carried %d and %d entries, want 2 and 1", len(firstEntries), len(secondEntries))
		}
		if cursor == "" {
			t.Error("the first page should carry a cursor")
		}
		if second["nextCursor"] != nil {
			t.Errorf("the last page should not carry a cursor, got %v", second["nextCursor"])
		}

		versions := []int{}
		for _, page := range [][]any{firstEntries, secondEntries} {
			for _, entry := range page {
				versions = append(versions, number(t, entry.(map[string]any), "walletVersion"))
			}
		}
		for index := 1; index < len(versions); index++ {
			if versions[index] <= versions[index-1] {
				t.Errorf("versions are not ascending: %v", versions)
			}
		}
	})

	t.Run("Given an unreadable cursor/When the ledger is asked/Then the contract reports the cursor", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("10.00")

		// When
		status, body := w.app(0).Request("GET", "/wallets/"+w.walletID+"/ledger?cursor=abc!", w.internalToken(), nil)

		// Then
		requireStatus(t, "invalid cursor", status, 400, body)
		requireField(t, "invalid cursor", body, "code", "INVALID_CURSOR")
	})

	t.Run("Given a wallet that does not exist/When it is read/Then the answer is not found", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		missing := uuid.Must(uuid.NewV7()).String()

		// When
		status, body := w.app(0).Request("GET", "/wallets/"+missing, w.internalToken(), nil)

		// Then
		requireStatus(t, "unknown wallet", status, 404, body)
		requireField(t, "unknown wallet", body, "code", "WALLET_NOT_FOUND")
	})

	t.Run("Given a ledger that no longer matches the wallet/When reconciliation runs/Then the divergence is reported", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("bet")
		status, body := w.submit("key-"+external, w.wager("BET", external, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)

		w.env.DB.WithoutWalletGuards(func(owner *pgxpool.Pool) error {
			_, err := owner.Exec(context.Background(),
				"UPDATE wallets SET balance_minor = balance_minor - 100 WHERE id = $1", w.walletID)
			return err
		})

		// When
		report := w.reconcile()

		// Then
		if boolean(t, report, "consistent") {
			t.Errorf("the report should not be consistent: %v", report)
		}
		if difference := amountOf(t, report, "difference"); difference != "-1.00" {
			t.Errorf("difference = %s, want -1.00", difference)
		}
		testenv.Eventually(t, time.Second, "sanitized divergence log", func() (bool, string) {
			output := w.app(0).Output()
			if strings.Contains(output, `"difference"`) || strings.Contains(output, "-1.00") {
				t.Fatal("monetary difference leaked to logs")
			}
			return strings.Contains(output, "wallet balance diverges from its ledger"), "waiting for reconciliation log"
		})
		if metrics := w.app(0).Metrics(); !metrics.Has("reconciliation_divergences_total", 1) {
			t.Errorf("the divergence should be counted, metrics:\n%s", metrics.Lines("reconciliation_divergences_total"))
		}
	})
}
