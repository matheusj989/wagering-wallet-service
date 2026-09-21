//go:build integration

package integration_test

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

// world is the vocabulary the scenarios speak: one isolated environment, the
// instances under test, a player and the wallet the story happens in.
type world struct {
	t        *testing.T
	env      *testenv.Env
	apps     []*testenv.App
	playerID string
	walletID string
	round    string
	sequence atomic.Int64
}

func newWorld(t *testing.T, instances int, options ...testenv.AppOption) *world {
	t.Helper()

	env := testenv.New(t)
	return &world{
		t:        t,
		env:      env,
		apps:     env.StartApp(instances, options...),
		playerID: uuid.Must(uuid.NewV7()).String(),
		round:    "round-" + uuid.Must(uuid.NewV7()).String()[:8],
	}
}

func (w *world) app(index int) *testenv.App {
	w.t.Helper()
	if index >= len(w.apps) {
		w.t.Fatalf("instance %d was not started", index)
	}
	return w.apps[index]
}

// replacePrimary points the story at a new instance after the first one is killed.
func (w *world) replacePrimary(replacement *testenv.App) {
	w.apps[0] = replacement
}

func (w *world) internalToken() string {
	return w.env.Auth.Token(testenv.ClientInternal)
}

func (w *world) providerToken(client string) string {
	return w.env.Auth.Token(client)
}

func (w *world) nextID(prefix string) string {
	return fmt.Sprintf("%s-%d-%s", prefix, w.sequence.Add(1), uuid.Must(uuid.NewV7()).String()[:8])
}

func (w *world) openWallet(amount string) map[string]any {
	w.t.Helper()

	status, body := w.app(0).Request("POST", "/wallets", w.internalToken(), map[string]any{
		"playerId":       w.playerID,
		"initialBalance": money(amount),
	})
	if status != 201 {
		w.t.Fatalf("opening a wallet with %s answered %d: %v", amount, status, body)
	}
	w.walletID = text(w.t, body, "id")
	return body
}

func (w *world) wager(kind string, external string, amount string, reference string) map[string]any {
	body := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": external,
		"playerId":              w.playerID,
		"walletId":              w.walletID,
		"roundId":               w.round,
		"gameId":                "fortune-chimp",
		"kind":                  kind,
		"money":                 money(amount),
	}
	if reference != "" {
		body["referenceExternalTransactionId"] = reference
	}
	return body
}

func (w *world) submit(key string, body map[string]any, headers ...string) (int, map[string]any) {
	w.t.Helper()
	return w.submitOn(w.app(0), testenv.ClientProviderA, key, body, headers...)
}

func (w *world) submitOn(app *testenv.App, client string, key string, body map[string]any, headers ...string) (int, map[string]any) {
	w.t.Helper()

	all := append([]string{"Idempotency-Key", key}, headers...)
	return app.Request("POST", "/wagering/transactions", w.providerToken(client), body, all...)
}

func (w *world) envelope(messageID string, key string, body map[string]any) map[string]any {
	data := make(map[string]any, len(body)+1)
	for name, value := range body {
		data[name] = value
	}
	data["idempotencyKey"] = key

	return map[string]any{
		"messageId":  messageID,
		"type":       "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"data":       data,
	}
}

func (w *world) publish(messageID string, key string, body map[string]any) {
	w.t.Helper()
	w.env.Queues.Send(messageID, w.walletID, w.envelope(messageID, key, body))
}

func (w *world) publishWithCorrelation(messageID string, key string, correlationID string, body map[string]any) {
	w.t.Helper()
	w.env.Queues.SendWithCorrelation(messageID, w.walletID, correlationID, w.envelope(messageID, key, body))
}

func (w *world) transaction(external string) (int, map[string]any) {
	w.t.Helper()
	return w.app(0).Request("GET", "/providers/provider-a/wagering/transactions/"+external, w.internalToken(), nil)
}

func (w *world) reconcile() map[string]any {
	w.t.Helper()

	status, body := w.app(0).Request("POST", "/wallets/"+w.walletID+"/reconciliation", w.internalToken(), nil)
	if status != 200 {
		w.t.Fatalf("reconciliation answered %d: %v", status, body)
	}
	return body
}

// requireConsistent is the closing assertion of every financial scenario: the
// stored balance has to equal the ledger, with the balance the story expects.
func (w *world) requireConsistent(expectedBalance string, expectedEntries int) {
	w.t.Helper()

	report := w.reconcile()
	if !boolean(w.t, report, "consistent") {
		w.t.Errorf("wallet %s diverges from its ledger: %v", w.walletID, report)
	}
	if stored := amountOf(w.t, report, "storedBalance"); stored != expectedBalance {
		w.t.Errorf("stored balance = %s, want %s", stored, expectedBalance)
	}
	if entries := number(w.t, report, "checkedEntries"); entries != expectedEntries {
		w.t.Errorf("ledger entries = %d, want %d", entries, expectedEntries)
	}
}

func (w *world) walletState() (string, int64) {
	w.t.Helper()

	status, body := w.app(0).Request("GET", "/wallets/"+w.walletID, w.internalToken(), nil)
	if status != 200 {
		w.t.Fatalf("reading the wallet answered %d: %v", status, body)
	}
	return amountOf(w.t, body, "balance"), int64(number(w.t, body, "version"))
}

func money(amount string) map[string]any {
	return map[string]any{"amount": amount, "currency": "BRL"}
}

func moneyIn(amount string, currency string) map[string]any {
	return map[string]any{"amount": amount, "currency": currency}
}

func text(t *testing.T, body map[string]any, field string) string {
	t.Helper()

	value, ok := body[field]
	if !ok || value == nil {
		t.Fatalf("field %q is missing from %v", field, body)
	}
	asString, ok := value.(string)
	if !ok {
		t.Fatalf("field %q is %T, want a string", field, value)
	}
	return asString
}

func optionalText(body map[string]any, field string) string {
	value, ok := body[field]
	if !ok || value == nil {
		return ""
	}
	asString, ok := value.(string)
	if !ok {
		return ""
	}
	return asString
}

func number(t *testing.T, body map[string]any, field string) int {
	t.Helper()

	value, ok := body[field]
	if !ok || value == nil {
		t.Fatalf("field %q is missing from %v", field, body)
	}
	asFloat, ok := value.(float64)
	if !ok {
		t.Fatalf("field %q is %T, want a number", field, value)
	}
	return int(asFloat)
}

func boolean(t *testing.T, body map[string]any, field string) bool {
	t.Helper()

	value, ok := body[field]
	if !ok || value == nil {
		t.Fatalf("field %q is missing from %v", field, body)
	}
	asBool, ok := value.(bool)
	if !ok {
		t.Fatalf("field %q is %T, want a boolean", field, value)
	}
	return asBool
}

func amountOf(t *testing.T, body map[string]any, field string) string {
	t.Helper()

	nested, ok := body[field].(map[string]any)
	if !ok {
		t.Fatalf("field %q is not a money object in %v", field, body)
	}
	return text(t, nested, "amount")
}

func requireStatus(t *testing.T, description string, got int, want int, body map[string]any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s answered %d, want %d: %v", description, got, want, body)
	}
}

func requireField(t *testing.T, description string, body map[string]any, field string, want string) {
	t.Helper()
	if got := optionalText(body, field); got != want {
		t.Errorf("%s: %s = %q, want %q (%v)", description, field, got, want, body)
	}
}

func newPlayerID() string {
	return uuid.Must(uuid.NewV7()).String()
}
