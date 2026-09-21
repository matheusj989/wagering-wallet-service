//go:build integration

package integration_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestWagerOutcomes(t *testing.T) {
	t.Run("Given a funded wallet/When a bet arrives/Then balance, ledger and events move together", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutOutboxPublisher())
		w.openWallet("1000.00")
		external := w.nextID("bet")

		// When
		status, body := w.submit("key-"+external, w.wager("BET", external, "25.00", ""))

		// Then
		requireStatus(t, "bet", status, 201, body)
		requireField(t, "bet", body, "status", "PROCESSED")
		if amountOf(t, body, "balance") != "975.00" || boolean(t, body, "idempotentReplay") {
			t.Errorf("answer = %v, want 975.00 and no replay", body)
		}
		balance, version := w.walletState()
		if balance != "975.00" || version != 2 {
			t.Errorf("wallet = %s at version %d, want 975.00 at version 2", balance, version)
		}
		if debits := w.env.DB.Count(
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.walletID); debits != 1 {
			t.Errorf("debits = %d, want 1", debits)
		}
		if events := w.env.DB.Count(
			"SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND causation_id = $2", w.walletID, text(t, body, "transactionId")); events != 2 {
			t.Errorf("events for the bet = %d, want 2", events)
		}
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given a wallet without funds/When a bet arrives/Then the rejection is persisted and auditable", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutOutboxPublisher())
		w.openWallet("20.00")
		external := w.nextID("bet")

		// When
		status, body := w.submit("key-"+external, w.wager("BET", external, "80.00", ""))

		// Then
		requireStatus(t, "bet without funds", status, 422, body)
		requireField(t, "bet without funds", body, "status", "REJECTED")
		requireField(t, "bet without funds", body, "failureCode", "INSUFFICIENT_FUNDS")
		if amountOf(t, body, "balance") != "20.00" {
			t.Errorf("balance = %s, want the untouched 20.00", amountOf(t, body, "balance"))
		}
		if rejected := w.env.DB.Count(
			"SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND status = 'REJECTED' AND failure_code = 'INSUFFICIENT_FUNDS'",
			w.walletID); rejected != 1 {
			t.Errorf("persisted rejections = %d, want 1", rejected)
		}
		if events := w.env.DB.Count(
			"SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND event_type = 'WagerTransactionRejected'", w.walletID); events != 1 {
			t.Errorf("rejection events = %d, want 1", events)
		}
		w.requireConsistent("20.00", 1)
	})

	t.Run("Given a loss/When it is settled/Then neither balance nor version move", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutOutboxPublisher())
		w.openWallet("975.00")
		external := w.nextID("loss")

		// When
		status, body := w.submit("key-"+external, w.wager("LOSS", external, "0.00", ""))

		// Then
		requireStatus(t, "loss", status, 201, body)
		requireField(t, "loss", body, "status", "PROCESSED")
		balance, version := w.walletState()
		if balance != "975.00" || version != 1 {
			t.Errorf("wallet = %s at version %d, want 975.00 at version 1", balance, version)
		}
		transactionID := text(t, body, "transactionId")
		if entries := w.env.DB.Count("SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1", transactionID); entries != 0 {
			t.Errorf("entries for the loss = %d, want none", entries)
		}
		if events := w.env.DB.Count(
			"SELECT count(*) FROM outbox_events WHERE causation_id = $1 AND event_type = 'WalletBalanceChanged'", transactionID); events != 0 {
			t.Errorf("balance events for the loss = %d, want none", events)
		}
		if events := w.env.DB.Count(
			"SELECT count(*) FROM outbox_events WHERE causation_id = $1 AND event_type = 'WagerTransactionProcessed'", transactionID); events != 1 {
			t.Errorf("processed events for the loss = %d, want 1", events)
		}
	})

	t.Run("Given a win that names a bet/When the reference does not agree/Then only the mismatch is rejected", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		bet := w.nextID("bet")
		status, body := w.submit("key-"+bet, w.wager("BET", bet, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)

		scenarios := []struct {
			name      string
			round     string
			reference string
			code      int
			failure   string
		}{
			{"reference of the same round", w.round, bet, 201, ""},
			{"reference that does not exist", w.round, "missing-" + bet, 201, ""},
			{"reference of another round", "round-other", bet, 422, "REFERENCE_MISMATCH"},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				external := w.nextID("win")
				request := w.wager("WIN", external, "10.00", scenario.reference)
				request["roundId"] = scenario.round

				// When
				status, body := w.submit("key-"+external, request)

				// Then
				requireStatus(t, scenario.name, status, scenario.code, body)
				if scenario.failure != "" {
					requireField(t, scenario.name, body, "failureCode", scenario.failure)
				}
			})
		}
	})

	t.Run("Given a processed bet/When it is reversed twice/Then only the first reversal moves money", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		bet := w.nextID("bet")
		status, body := w.submit("key-"+bet, w.wager("BET", bet, "25.00", ""))
		requireStatus(t, "bet", status, 201, body)

		// When
		refund := w.nextID("refund")
		status, refunded := w.submit("key-"+refund, w.wager("REFUND", refund, "25.00", bet))
		rollback := w.nextID("rollback")
		secondStatus, second := w.submit("key-"+rollback, w.wager("ROLLBACK", rollback, "25.00", bet))

		// Then
		requireStatus(t, "refund", status, 201, refunded)
		if amountOf(t, refunded, "balance") != "1000.00" {
			t.Errorf("balance after the refund = %s, want 1000.00", amountOf(t, refunded, "balance"))
		}
		requireStatus(t, "second reversal", secondStatus, 422, second)
		requireField(t, "second reversal", second, "failureCode", "ALREADY_REVERSED")
		w.requireConsistent("1000.00", 3)
	})

	t.Run("Given a reversal that would go below zero/When it is settled/Then it uses its own failure code", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("0.00")
		win := w.nextID("win")
		status, body := w.submit("key-"+win, w.wager("WIN", win, "100.00", ""))
		requireStatus(t, "win", status, 201, body)
		bet := w.nextID("bet")
		status, body = w.submit("key-"+bet, w.wager("BET", bet, "80.00", ""))
		requireStatus(t, "bet", status, 201, body)

		// When
		rollback := w.nextID("rollback")
		status, body = w.submit("key-"+rollback, w.wager("ROLLBACK", rollback, "100.00", win))

		// Then
		requireStatus(t, "rollback without funds", status, 422, body)
		requireField(t, "rollback without funds", body, "failureCode", "REVERSAL_INSUFFICIENT_FUNDS")
		w.requireConsistent("20.00", 2)
	})

	t.Run("Given a wallet at the representable limit/When a credit arrives/Then the balance limit is a definitive rejection", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("92233720368547758.07")
		external := w.nextID("win")
		request := w.wager("WIN", external, "0.01", "")

		// When
		status, body := w.submit("key-"+external, request)
		replayStatus, replay := w.submit("key-"+external, request)

		// Then
		requireStatus(t, "credit above the limit", status, 422, body)
		requireField(t, "credit above the limit", body, "failureCode", "BALANCE_LIMIT_EXCEEDED")
		requireStatus(t, "replay of the limit rejection", replayStatus, 422, replay)
		if !boolean(t, replay, "idempotentReplay") {
			t.Error("the second call should be a replay")
		}
		balance, version := w.walletState()
		if balance != "92233720368547758.07" || version != 1 {
			t.Errorf("wallet = %s at version %d, want the limit at version 1", balance, version)
		}
		w.requireConsistent("92233720368547758.07", 1)
	})

	t.Run("Given an operation in another supported currency/When it is settled/Then the rejection keeps the wallet currency", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")
		external := w.nextID("bet")
		request := w.wager("BET", external, "10.00", "")
		request["money"] = moneyIn("10.00", "USD")

		// When
		status, body := w.submit("key-"+external, request)

		// Then
		requireStatus(t, "currency mismatch", status, 422, body)
		requireField(t, "currency mismatch", body, "failureCode", "CURRENCY_MISMATCH")
		balance, ok := body["balance"].(map[string]any)
		if !ok || balance["currency"] != "BRL" || balance["amount"] != "100.00" {
			t.Errorf("balance = %v, want 100.00 BRL from the wallet", body["balance"])
		}
	})

	t.Run("Given a payload for another player/When it is settled/Then the mismatch is auditable", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")
		external := w.nextID("bet")
		request := w.wager("BET", external, "10.00", "")
		request["playerId"] = uuid.Must(uuid.NewV7()).String()

		// When
		status, body := w.submit("key-"+external, request)

		// Then
		requireStatus(t, "player mismatch", status, 422, body)
		requireField(t, "player mismatch", body, "failureCode", "WALLET_PLAYER_MISMATCH")
		w.requireConsistent("100.00", 1)
	})

	t.Run("Given an unusable operation/When it is submitted/Then nothing is persisted", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")

		scenarios := []struct {
			name    string
			mutate  func(map[string]any)
			headers []string
			code    int
		}{
			{"opening through the api", func(body map[string]any) { body["kind"] = "OPENING" }, nil, 400},
			{"loss carrying an amount", func(body map[string]any) { body["kind"] = "LOSS" }, nil, 400},
			{"bet carrying a reference", func(body map[string]any) { body["referenceExternalTransactionId"] = "x" }, nil, 400},
			{"refund without a reference", func(body map[string]any) { body["kind"] = "REFUND" }, nil, 400},
			{"amount without two places", func(body map[string]any) { body["money"] = money("10") }, nil, 400},
			{"unknown field", func(body map[string]any) { body["extra"] = 1 }, nil, 400},
			{"unknown kind", func(body map[string]any) { body["kind"] = "TRANSFER" }, nil, 400},
		}

		before := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE wallet_id = $1", w.walletID)
		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				external := w.nextID("invalid")
				request := w.wager("BET", external, "10.00", "")
				scenario.mutate(request)

				// When
				status, body := w.submit("key-"+external, request)

				// Then
				requireStatus(t, scenario.name, status, scenario.code, body)
				requireField(t, scenario.name, body, "code", "VALIDATION_FAILED")
			})
		}

		after := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE wallet_id = $1", w.walletID)
		if before != after {
			t.Errorf("transactions went from %d to %d, invalid input must not persist", before, after)
		}
	})

	t.Run("Given a wallet that does not exist/When a bet names it/Then the key stays free for the corrected retry", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")
		external := w.nextID("bet")
		key := "key-" + external
		wrong := w.wager("BET", external, "10.00", "")
		wrong["walletId"] = uuid.Must(uuid.NewV7()).String()

		// When
		status, body := w.submit(key, wrong)
		corrected, correctedBody := w.submit(key, w.wager("BET", external, "10.00", ""))

		// Then
		requireStatus(t, "unknown wallet", status, 404, body)
		requireField(t, "unknown wallet", body, "code", "WALLET_NOT_FOUND")
		requireStatus(t, "corrected retry", corrected, 201, correctedBody)
		if boolean(t, correctedBody, "idempotentReplay") {
			t.Error("the corrected retry is a new operation, not a replay")
		}
	})
}

func TestIdempotency(t *testing.T) {
	t.Run("Given a settled operation/When it is sent again/Then the original result comes back", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("bet")
		key := "key-" + external
		request := w.wager("BET", external, "25.00", "")
		status, first := w.submit(key, request)
		requireStatus(t, "bet", status, 201, first)

		// When
		other := w.nextID("win")
		status, body := w.submit("key-"+other, w.wager("WIN", other, "100.00", ""))
		requireStatus(t, "win", status, 201, body)
		replayStatus, replay := w.submit(key, request)

		// Then
		requireStatus(t, "replay", replayStatus, 201, replay)
		if !boolean(t, replay, "idempotentReplay") {
			t.Error("the answer should be marked as a replay")
		}
		if amountOf(t, replay, "balance") != "975.00" {
			t.Errorf("replay balance = %s, want the 975.00 observed originally", amountOf(t, replay, "balance"))
		}
		if text(t, replay, "transactionId") != text(t, first, "transactionId") {
			t.Error("the replay should point at the original transaction")
		}
		w.requireConsistent("1075.00", 3)
	})

	t.Run("Given a used key/When the payload changes/Then the conflict keeps the original operation", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("bet")
		key := "key-" + external
		status, first := w.submit(key, w.wager("BET", external, "25.00", ""))
		requireStatus(t, "bet", status, 201, first)

		// When
		status, conflict := w.submit(key, w.wager("BET", external, "30.00", ""))
		retryStatus, retry := w.submit(key, w.wager("BET", external, "40.00", ""))
		replayStatus, replay := w.submit(key, w.wager("BET", external, "25.00", ""))

		// Then
		requireStatus(t, "conflicting payload", status, 409, conflict)
		requireField(t, "conflicting payload", conflict, "code", "IDEMPOTENCY_KEY_CONFLICT")
		if optionalText(conflict, "transactionId") != text(t, first, "transactionId") {
			t.Error("the conflict should name the existing transaction")
		}
		requireStatus(t, "second conflicting payload", retryStatus, 409, retry)
		requireStatus(t, "original payload", replayStatus, 201, replay)
		if !boolean(t, replay, "idempotentReplay") {
			t.Error("only the original payload produces a replay")
		}
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given a used external id/When another key reuses it/Then the operation is refused", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		external := w.nextID("bet")
		status, first := w.submit("key-"+external, w.wager("BET", external, "25.00", ""))
		requireStatus(t, "bet", status, 201, first)

		// When
		status, body := w.submit("another-key-"+external, w.wager("BET", external, "25.00", ""))

		// Then
		requireStatus(t, "external id reused", status, 409, body)
		requireField(t, "external id reused", body, "code", "EXTERNAL_TRANSACTION_ID_CONFLICT")
		w.requireConsistent("975.00", 2)
	})

	t.Run("Given the same textual key at two providers/When both send their own operation/Then they never mix", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("1000.00")
		shared := "shared-key-" + w.nextID("k")

		// When
		fromA := w.wager("BET", w.nextID("bet-a"), "25.00", "")
		statusA, bodyA := w.submitOn(w.app(0), testenv.ClientProviderA, shared, fromA)

		fromB := w.wager("BET", w.nextID("bet-b"), "10.00", "")
		fromB["providerId"] = "provider-b"
		statusB, bodyB := w.submitOn(w.app(0), testenv.ClientProviderB, shared, fromB)

		// Then
		requireStatus(t, "provider a", statusA, 201, bodyA)
		requireStatus(t, "provider b", statusB, 201, bodyB)
		if text(t, bodyA, "transactionId") == text(t, bodyB, "transactionId") {
			t.Error("each provider owns its own key space")
		}
		if boolean(t, bodyB, "idempotentReplay") {
			t.Error("provider b sent a new operation, not a replay")
		}
		w.requireConsistent("965.00", 3)
	})
}
