//go:build integration

package integration_test

import (
	"context"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/system"
	"github.com/matheusj989/wagering-wallet-service/test/testenv"
	"strings"
	"testing"
	"time"
)

func TestQueueCommitRecovery(t *testing.T) {
	t.Run("Given a committed SQS debit/When the COMMIT response is lost/Then redelivery replays the inbox without another movement", func(t *testing.T) {
		// Given
		env := testenv.New(t)
		proxy := env.StartDatabaseProxy()
		w := &world{t: t, env: env, apps: env.StartApp(1, testenv.WithSetting("DATABASE_URL", proxy.URL()), testenv.WithoutOutboxPublisher(), testenv.WithoutReferenceWorker()), playerID: newPlayerID(), round: "queue-commit"}
		w.openWallet("100.00")
		// When
		proxy.CutAfter("COMMIT")
		w.publish("uncertain", "uncertain", w.wager("BET", "uncertain", "25.00", ""))
		// Then
		testenv.Eventually(t, 10*time.Second, "unknown commit logged", func() (bool, string) {
			return strings.Contains(w.app(0).Output(), "the outcome is unknown"), w.app(0).Output()
		})
		if proxy.Cuts() != 1 {
			t.Fatal("expected one lost COMMIT answer")
		}
		if env.DB.Count("SELECT count(*) FROM inbox_messages") != 1 {
			t.Fatal("committed inbox missing")
		}
		if env.Queues.Pending(env.Queues.Wager) != 1 {
			t.Fatal("uncertain delivery was acknowledged")
		}
		testenv.Eventually(t, 15*time.Second, "redelivery acknowledged", func() (bool, string) { return env.Queues.Pending(env.Queues.Wager) == 0, "source message remains" })
		w.requireConsistent("75.00", 2)
		if !w.app(0).Metrics().Has("wager_idempotent_replays_total", 1) {
			t.Fatal("no durable inbox replay recorded")
		}
	})
	t.Run("Given a pending reversal the database resolves/When its COMMIT response is lost/Then another worker observes the terminal result", func(t *testing.T) {
		// Given
		w := newWorld(t, 1, testenv.WithoutConsumer(), testenv.WithoutOutboxPublisher(), testenv.WithoutReferenceWorker(), testenv.WithSetting("REFERENCE_TTL", "5m"))
		w.openWallet("100.00")
		status, body := w.submit("rollback", w.wager("ROLLBACK", "rollback", "25.00", "bet"))
		requireStatus(t, "pending", status, 202, body)
		status, body = w.submit("bet", w.wager("BET", "bet", "25.00", ""))
		requireStatus(t, "bet", status, 201, body)
		proxy := w.env.StartDatabaseProxy()
		// When
		proxy.CutAfter("COMMIT")
		worker := w.env.StartApp(1, testenv.WithSetting("DATABASE_URL", proxy.URL()), testenv.WithoutConsumer(), testenv.WithoutOutboxPublisher())[0]
		// Then
		testenv.Eventually(t, 10*time.Second, "durable reversal", func() (bool, string) {
			return proxy.Cuts() == 1 && w.env.DB.Count("SELECT count(*) FROM wager_transactions WHERE external_transaction_id='rollback' AND status='PROCESSED'") == 1, worker.Output()
		})
		worker.Stop()
		replacement := referenceWorker(t, w.env, system.NewIDGenerator())
		if _, err := replacement.Execute(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, operation := w.transaction("rollback")
		requireField(t, "resolved", operation, "status", "PROCESSED")
		w.requireConsistent("100.00", 3)
	})
}

func TestInFlightShutdown(t *testing.T) {
	for _, finish := range []bool{true, false} {
		name := "deadline releases unfinished message"
		if finish {
			name = "drain finishes accepted message"
		}
		t.Run("Given a handler waiting for a wallet lock/When SIGTERM arrives/Then "+name, func(t *testing.T) {
			// Given
			w := newWorld(t, 1, testenv.WithoutOutboxPublisher(), testenv.WithoutReferenceWorker(), testenv.WithSetting("SHUTDOWN_TIMEOUT", "2s"), testenv.WithSetting("SQS_MESSAGE_DEADLINE", "12s"), testenv.WithSetting("SQS_VISIBILITY_TIMEOUT", "30s"), testenv.WithSetting("DB_LOCK_TIMEOUT", "8s"), testenv.WithSetting("DB_STATEMENT_TIMEOUT", "10s"))
			w.openWallet("100.00")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			blocker, err := w.env.DB.Pool().Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(ctx, "SELECT id FROM wallets WHERE id=$1 FOR NO KEY UPDATE", w.walletID); err != nil {
				t.Fatal(err)
			}
			w.publish("in-flight", "in-flight", w.wager("BET", "in-flight", "25.00", ""))
			testenv.Eventually(t, 5*time.Second, "handler blocked inside SQL", func() (bool, string) {
				return w.env.DB.Count("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'") > 0, "handler has not reached wallet lock"
			})
			// When
			started := time.Now()
			stopped := make(chan struct{})
			go func() { w.app(0).Stop(); close(stopped) }()
			if finish {
				testenv.Eventually(t, time.Second, "HTTP listener closed for shutdown", func() (bool, string) {
					r, err := w.app(0).Client.Get(w.app(0).BaseURL + "/health/live")
					if err == nil {
						r.Body.Close()
					}
					return err != nil, "listener still accepts requests"
				})
				if err := blocker.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown exceeded its bounded drain")
			}
			// Then
			if elapsed := time.Since(started); elapsed > 4*time.Second {
				t.Fatalf("shutdown took %s", elapsed)
			}
			if !finish {
				if !strings.Contains(w.app(0).Output(), "unfinished message released") {
					t.Fatalf("unfinished delivery not released: %s", w.app(0).Output())
				}
				if w.env.DB.Count("SELECT count(*) FROM inbox_messages") != 0 {
					t.Fatal("aborted delivery persisted inbox")
				}
				if err := blocker.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
			}
			w.replacePrimary(w.env.StartApp(1)[0])
			testenv.Eventually(t, 5*time.Second, "replacement settles the delivery before original visibility expires", func() (bool, string) { return w.env.Queues.Pending(w.env.Queues.Wager) == 0, "source message remains" })
			w.requireConsistent("75.00", 2)
		})
	}
}

func TestDeadLetterPublicationRecovery(t *testing.T) {
	for _, mode := range []string{"unavailable", "timeout"} {
		t.Run("Given a DLQ publication "+mode+"/When a malformed message is delivered again/Then one dead letter remains and the source is acknowledged only after recovery", func(t *testing.T) {
			// Given
			env := testenv.New(t)
			proxy := env.StartQueueProxy(mode)
			app := env.StartApp(1, testenv.WithSetting("SQS_ENDPOINT", proxy.URL()), testenv.WithSetting("SQS_VISIBILITY_TIMEOUT", "10s"), testenv.WithoutOutboxPublisher(), testenv.WithoutReferenceWorker())[0]
			// When
			env.Queues.SendRaw("invalid", "bad-group", "not-json")
			// Then
			testenv.Eventually(t, 12*time.Second, "failed DLQ attempt", func() (bool, string) {
				return strings.Contains(app.Output(), "could not move a message to the dead letter queue"), app.Output()
			})
			if proxy.Attempts() == 0 || env.Queues.Pending(env.Queues.Wager) != 1 {
				t.Fatal("source deleted without a confirmed publication")
			}
			proxy.Restore()
			testenv.Eventually(t, 15*time.Second, "DLQ recovery", func() (bool, string) {
				return env.Queues.Pending(env.Queues.Wager) == 0 && env.Queues.Pending(env.Queues.DLQ) == 1, "waiting for source acknowledgement and one DLQ message"
			})
			letters := env.Queues.DeadLetters(10)
			if len(letters) != 1 || letters[0].Body != "not-json" {
				t.Fatalf("dead letters=%v", letters)
			}
			if env.DB.Count("SELECT count(*) FROM inbox_messages") != 0 {
				t.Fatal("malformed input wrote an inbox record")
			}
		})
	}
}
