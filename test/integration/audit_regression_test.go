//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/observability"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/postgres"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/system"
	"github.com/matheusj989/wagering-wallet-service/test/testenv"
	"github.com/prometheus/client_golang/prometheus"
)

func TestInputRegressions(t *testing.T) {
	t.Run("Given trailing JSON tokens/When a bet is posted/Then no financial state is written", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")
		for _, suffix := range []string{"]", "}", "{}", "true"} {
			payload, _ := json.Marshal(w.wager("BET", "invalid-json", "25.00", ""))
			req, _ := http.NewRequest("POST", w.app(0).BaseURL+"/wagering/transactions", bytes.NewReader(append(payload, []byte(suffix)...)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+w.providerToken(testenv.ClientProviderA))
			req.Header.Set("Idempotency-Key", "invalid-json")
			// When
			response, err := w.app(0).Client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			// Then
			if response.StatusCode != 400 {
				t.Fatalf("suffix %q answered %d", suffix, response.StatusCode)
			}
		}
		if n := w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE kind='BET'"); n != 0 {
			t.Fatal("invalid bet persisted")
		}
		w.requireConsistent("100.00", 1)
	})
	t.Run("Given malformed queue envelopes/When delivered/Then each is dead lettered without a debit", func(t *testing.T) {
		// Given
		w := newWorld(t, 1)
		w.openWallet("100.00")
		for i, mode := range []string{"missing-time", "invalid-time", "trailing-bracket"} {
			envelope := w.envelope(mode, mode, w.wager("BET", mode, "10.00", ""))
			if i == 0 {
				delete(envelope, "occurredAt")
			}
			if i == 1 {
				envelope["occurredAt"] = "not-a-date"
			}
			body, _ := json.Marshal(envelope)
			if i == 2 {
				body = append(body, ']')
			}
			// When
			w.env.Queues.SendRaw(mode, w.walletID, string(body))
		}
		// Then
		testenv.Eventually(t, 15*time.Second, "invalid envelopes in DLQ", func() (bool, string) {
			return w.env.Queues.Pending(w.env.Queues.DLQ) == 3, "waiting for three rejected messages"
		})
		if n := w.env.DB.Count("SELECT count(*) FROM inbox_messages"); n != 0 {
			t.Fatal("invalid envelope persisted inbox")
		}
		w.requireConsistent("100.00", 1)
	})
}

func TestReferenceFailureRegressions(t *testing.T) {
	for _, code := range []string{"40001", "40P01", "23514"} {
		t.Run("Given database failure "+code+"/When a reference attempt aborts/Then only permanent failure is terminal", func(t *testing.T) {
			// Given
			w := newWorld(t, 1, testenv.WithoutReferenceWorker(), testenv.WithSetting("REFERENCE_TTL", "5m"))
			w.openWallet("100.00")
			status, body := w.submit("retry", w.wager("ROLLBACK", "retry", "25.00", "future-bet"))
			requireStatus(t, "pending", status, 202, body)
			script := fmt.Sprintf(`CREATE FUNCTION forced_reference_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.status='PENDING_REFERENCE' AND NEW.status<>'FAILED' THEN RAISE EXCEPTION 'forced failure' USING ERRCODE='%s'; END IF; RETURN NEW; END $$; CREATE TRIGGER forced_reference_failure BEFORE UPDATE ON wager_transactions FOR EACH ROW EXECUTE FUNCTION forced_reference_failure();`, code)
			if err := w.env.DB.AttemptAsOwner("UPDATE wager_transactions SET next_attempt_at=now() WHERE status='PENDING_REFERENCE';" + script); err != nil {
				t.Fatal(err)
			}
			worker := referenceWorker(t, w.env, system.NewIDGenerator())
			// When
			_, err := worker.Execute(context.Background())
			// Then
			_, operation := w.transaction("retry")
			if code == "23514" {
				if err != nil {
					t.Fatal(err)
				}
				requireField(t, "permanent", operation, "status", "FAILED")
				requireField(t, "permanent", operation, "failureCode", "PERMANENT_FAILURE")
			} else {
				if !errors.Is(err, repositories.ErrTransient) {
					t.Fatalf("retry exhaustion = %v", err)
				}
				requireField(t, "transient", operation, "status", "PENDING_REFERENCE")
				if err := w.env.DB.AttemptAsOwner("DROP TRIGGER forced_reference_failure ON wager_transactions; DROP FUNCTION forced_reference_failure();"); err != nil {
					t.Fatal(err)
				}
				status, body := w.submit("future-bet", w.wager("BET", "future-bet", "25.00", ""))
				requireStatus(t, "reference", status, 201, body)
				if _, err := worker.Execute(context.Background()); err != nil {
					t.Fatal(err)
				}
				_, operation = w.transaction("retry")
				requireField(t, "recovered", operation, "status", "PROCESSED")
				w.requireConsistent("100.00", 3)
			}
		})
	}
	t.Run("Given panic after financial writes/When the worker aborts/Then rollback precedes FAILED and the next pending can proceed", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutReferenceWorker(), testenv.WithSetting("REFERENCE_TTL", "5m"))
		w.openWallet("100.00")
		status, body := w.submit("panic", w.wager("ROLLBACK", "panic", "25.00", "bet"))
		requireStatus(t, "pending", status, 202, body)
		status, body = w.submit("bet", w.wager("BET", "bet", "25.00", ""))
		requireStatus(t, "bet", status, 201, body)
		worker := referenceWorker(t, w.env, &panicOnSecondID{})
		// When
		_, err := worker.Execute(context.Background())
		// Then
		if err != nil {
			t.Fatal(err)
		}
		_, operation := w.transaction("panic")
		requireField(t, "panicked attempt", operation, "status", "FAILED")
		w.requireConsistent("75.00", 2)
		if n := w.env.DB.Count("SELECT count(*) FROM outbox_events WHERE causation_id=$1 AND event_type='WagerTransactionProcessed'", text(t, operation, "transactionId")); n != 0 {
			t.Fatal("rolled back event survived")
		}
		status, body = w.submit("next", w.wager("ROLLBACK", "next", "10.00", "next-bet"))
		requireStatus(t, "next pending", status, 202, body)
		status, body = w.submit("next-bet", w.wager("BET", "next-bet", "10.00", ""))
		requireStatus(t, "next bet", status, 201, body)
		if _, err := worker.Execute(context.Background()); err != nil {
			t.Fatal(err)
		}
		w.requireConsistent("75.00", 4)
	})
}

type panicOnSecondID struct{ calls int }

func (p *panicOnSecondID) New() uuid.UUID {
	p.calls++
	if p.calls == 2 {
		panic("forced event construction failure")
	}
	return uuid.Must(uuid.NewV7())
}

func referenceWorker(t *testing.T, env *testenv.Env, ids port.IDGenerator) usecase.RetryPendingReference {
	t.Helper()
	client, err := postgres.NewClient(context.Background(), postgres.ClientSettings{URL: env.DB.URL(), MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	uow := postgres.NewUnitOfWork(client, postgres.Settings{RetryAttempts: 3, LockTimeout: time.Second, StatementTimeout: 2 * time.Second})
	return usecase.NewRetryPendingReference(uow, system.NewClock(), ids, observability.NewMetrics(prometheus.NewRegistry()), slog.New(slog.NewTextHandler(io.Discard, nil)), usecase.ReferenceSettings{BackoffBase: time.Second, BackoffMax: time.Minute, BatchSize: 5})
}

func TestRuntimeRegressions(t *testing.T) {
	t.Run("Given unreachable JWKS/When the application starts/Then startup fails before accepting traffic", func(t *testing.T) {
		// Given
		env := testenv.New(t)
		// When
		result := env.StartFailingApp(testenv.WithSetting("OIDC_JWKS_URL", "http://127.0.0.1:1/certs"))
		// Then
		if result.ExitCode == 0 || !strings.Contains(result.Output, "JWKS") {
			t.Fatalf("unexpected failure: %s", result.Output)
		}
	})
	t.Run("Given a pending reversal/When it is resolved/Then the durable backlog gauge rises and returns to zero", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithSetting("REFERENCE_TTL", "5m"))
		w.openWallet("100.00")
		// When
		status, body := w.submit("pending", w.wager("ROLLBACK", "pending", "25.00", "bet"))
		requireStatus(t, "pending", status, 202, body)
		// Then
		testenv.Eventually(t, 5*time.Second, "pending metric", func() (bool, string) {
			return w.app(0).Metrics().Value("reference_pending") == 1, "waiting for backlog=1"
		})
		status, body = w.submit("bet", w.wager("BET", "bet", "25.00", ""))
		requireStatus(t, "reference", status, 201, body)
		testenv.Eventually(t, 5*time.Second, "resolved metric", func() (bool, string) {
			return w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE status='PENDING_REFERENCE'") == 0 && w.app(0).Metrics().Value("reference_pending") == 0, "waiting for backlog=0"
		})
		w.requireConsistent("100.00", 3)
	})
}

func TestHTTPTransientFailure(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		t.Run("Given repeated SQLSTATE "+code+"/When the HTTP retry budget is exhausted/Then the caller receives 503 and can retry without duplicate debit", func(t *testing.T) {
			// Given
			w := newWorld(t, 1, testenv.WithoutConsumer(), testenv.WithoutOutboxPublisher(), testenv.WithoutReferenceWorker())
			w.openWallet("100.00")
			script := fmt.Sprintf(`CREATE FUNCTION transient_wallet_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced transient failure' USING ERRCODE='%s'; END $$; CREATE TRIGGER transient_wallet_failure BEFORE UPDATE ON wallets FOR EACH ROW EXECUTE FUNCTION transient_wallet_failure();`, code)
			if err := w.env.DB.AttemptAsOwner(script); err != nil {
				t.Fatal(err)
			}
			// When
			status, body := w.submit("retry-http", w.wager("BET", "retry-http", "25.00", ""))
			// Then
			requireStatus(t, "exhausted retry", status, 503, body)
			requireField(t, "exhausted retry", body, "code", "SERVICE_UNAVAILABLE")
			w.requireConsistent("100.00", 1)
			if err := w.env.DB.AttemptAsOwner("DROP TRIGGER transient_wallet_failure ON wallets; DROP FUNCTION transient_wallet_failure();"); err != nil {
				t.Fatal(err)
			}
			status, body = w.submit("retry-http", w.wager("BET", "retry-http", "25.00", ""))
			requireStatus(t, "retry after recovery", status, 201, body)
			if boolean(t, body, "idempotentReplay") {
				t.Fatal("aborted attempt cannot be a replay")
			}
			w.requireConsistent("75.00", 2)
		})
	}
}
