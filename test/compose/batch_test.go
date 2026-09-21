//go:build compose

// Package compose_test exercises the built Compose application from outside its
// network. It never starts, deletes or reconfigures the stack.
package compose_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	queues "github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs"
)

type batchWallet struct{ Player, ID, Prefix string }
type batchState struct{ Wallets []batchWallet }
type batchClient struct {
	t                  *testing.T
	ctx                context.Context
	http               *http.Client
	bases              []string
	next               atomic.Uint64
	internal, provider string
	sqs                *awssqs.Client
	db                 *pgxpool.Pool
	wager, dlq, events string
}

func setting(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func newBatchClient(t *testing.T) *batchClient {
	t.Helper()
	if os.Getenv("COMPOSE_DATABASE_URL") == "" || os.Getenv("COMPOSE_SQS_ENDPOINT") == "" {
		t.Fatal("set COMPOSE_DATABASE_URL and COMPOSE_SQS_ENDPOINT for the isolated validation stack")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	c := &batchClient{t: t, ctx: ctx, http: &http.Client{Timeout: 15 * time.Second}, bases: strings.Split(setting("COMPOSE_API_URLS", "http://localhost:18080,http://localhost:18081,http://localhost:18082"), ",")}
	var err error
	c.db, err = pgxpool.New(ctx, os.Getenv("COMPOSE_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.db.Close)
	c.sqs, err = queues.NewClient(ctx, queues.Settings{Region: "us-east-1", Endpoint: os.Getenv("COMPOSE_SQS_ENDPOINT"), AccessKeyID: "test", SecretKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for name, destination := range map[string]*string{"wager-transactions.fifo": &c.wager, "wager-transactions-dlq.fifo": &c.dlq, "wallet-events.fifo": &c.events} {
		result, err := c.sqs.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(name)})
		if err != nil {
			t.Fatal(err)
		}
		*destination = aws.ToString(result.QueueUrl)
	}
	c.internal = c.token("wallet-internal", "wallet-internal-secret")
	c.provider = c.token("provider-a", "provider-a-secret")
	for _, base := range c.bases {
		response, err := c.http.Get(base + "/health/ready")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("replica %s not ready", base)
		}
	}
	return c
}
func (c *batchClient) token(id, secret string) string {
	c.t.Helper()
	response, err := c.http.PostForm(setting("COMPOSE_KEYCLOAK_URL", "http://localhost:18180")+"/realms/wallet/protocol/openid-connect/token", url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}})
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Token string `json:"access_token"`
	}
	if response.StatusCode != 200 {
		c.t.Fatalf("token status=%d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		c.t.Fatal(err)
	}
	return body.Token
}
func (c *batchClient) call(method, path, token, key string, payload any, want int) map[string]any {
	c.t.Helper()
	var raw []byte
	var err error
	if payload != nil {
		raw, err = json.Marshal(payload)
		if err != nil {
			c.t.Fatal(err)
		}
	}
	base := c.bases[(c.next.Add(1)-1)%uint64(len(c.bases))]
	request, err := http.NewRequestWithContext(c.ctx, method, base+path, bytes.NewReader(raw))
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := c.http.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(response.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	if response.StatusCode != want {
		c.t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.StatusCode, want, raw)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		c.t.Fatal(err)
	}
	return body
}
func amount(value string) map[string]string {
	return map[string]string{"amount": value, "currency": "BRL"}
}
func (w batchWallet) operation(kind, suffix, value, reference string) map[string]any {
	body := map[string]any{"providerId": "provider-a", "externalTransactionId": w.Prefix + suffix, "walletId": w.ID, "playerId": w.Player, "roundId": w.Prefix, "gameId": "batch-validation", "kind": kind, "money": amount(value)}
	if reference != "" {
		body["referenceExternalTransactionId"] = w.Prefix + reference
	}
	return body
}
func (c *batchClient) count(query string, args ...any) int {
	c.t.Helper()
	var count int
	if err := c.db.QueryRow(c.ctx, query, args...).Scan(&count); err != nil {
		c.t.Fatal(err)
	}
	return count
}
func (c *batchClient) wait(description string, condition func() bool) {
	c.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		select {
		case <-c.ctx.Done():
			c.t.Fatal(c.ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	c.t.Fatalf("timeout waiting for %s", description)
}
func (c *batchClient) pending(queue string) int {
	c.t.Helper()
	output, err := c.sqs.GetQueueAttributes(c.ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(queue), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
	if err != nil {
		c.t.Fatal(err)
	}
	count := 0
	for _, raw := range output.Attributes {
		var n int
		_, _ = fmt.Sscan(raw, &n)
		count += n
	}
	return count
}
func (c *batchClient) verify(wallets []batchWallet) {
	c.t.Helper()
	for _, w := range wallets {
		body := c.call("POST", "/wallets/"+w.ID+"/reconciliation", c.internal, "", nil, 200)
		balance, _ := body["storedBalance"].(map[string]any)
		if body["consistent"] != true || balance["amount"] != "1040.00" || body["checkedEntries"] != float64(6) {
			c.t.Fatalf("reconciliation failed: %v", body)
		}
		replay := c.call("POST", "/wagering/transactions", c.provider, w.Prefix+"bet", w.operation("BET", "bet", "100.00", ""), 201)
		if replay["idempotentReplay"] != true {
			c.t.Fatal("persistent replay missing")
		}
		if c.count("SELECT count(*) FROM wager_transactions WHERE wallet_id=$1", w.ID) != 8 {
			c.t.Fatal("unexpected transaction count")
		}
	}
}

func TestComposeBatch(t *testing.T) {
	t.Run("Given three replicas and fifty independent wallets/When HTTP and SQS batches race with duplicates and early references/Then all financial histories reconcile", func(t *testing.T) {
		// Given
		c := newBatchClient(t)
		const walletCount = 50
		wallets := make([]batchWallet, walletCount)
		var workers sync.WaitGroup
		slots := make(chan struct{}, 10)
		for index := range wallets {
			slots <- struct{}{}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-slots }()
				w := batchWallet{Player: uuid.NewString(), Prefix: uuid.NewString() + "-"}
				opened := c.call("POST", "/wallets", c.internal, "", map[string]any{"playerId": w.Player, "initialBalance": amount("1000.00")}, 201)
				w.ID = opened["id"].(string)
				c.call("POST", "/wagering/transactions", c.provider, w.Prefix+"bet", w.operation("BET", "bet", "100.00", ""), 201)
				replay := c.call("POST", "/wagering/transactions", c.provider, w.Prefix+"bet", w.operation("BET", "bet", "100.00", ""), 201)
				if replay["idempotentReplay"] != true {
					t.Error("HTTP replay missing")
				}
				c.call("POST", "/wagering/transactions", c.provider, w.Prefix+"rollback", w.operation("ROLLBACK", "rollback", "20.00", "late-win"), 202)
				c.call("POST", "/wagering/transactions", c.provider, w.Prefix+"rejected", w.operation("BET", "rejected", "2000.00", ""), 422)
				wallets[index] = w
			}()
		}
		workers.Wait()
		if t.Failed() {
			return
		}
		// When: unique transport IDs bypass FIFO deduplication on purpose, while two
		// messages per wallet replay the BET already accepted through HTTP.
		entries := make([]types.SendMessageBatchRequestEntry, 0, walletCount*6+10)
		for _, w := range wallets {
			operations := []struct{ kind, suffix, value, reference string }{{"BET", "bet", "100.00", ""}, {"BET", "bet", "100.00", ""}, {"WIN", "win", "40.00", ""}, {"LOSS", "loss", "0.00", ""}, {"REFUND", "refund", "100.00", "bet"}, {"WIN", "late-win", "20.00", ""}}
			for _, operation := range operations {
				data := w.operation(operation.kind, operation.suffix, operation.value, operation.reference)
				data["idempotencyKey"] = w.Prefix + operation.suffix
				messageID := uuid.NewString()
				raw, _ := json.Marshal(map[string]any{"messageId": messageID, "type": "WagerTransactionRequested", "occurredAt": time.Now().UTC().Format(time.RFC3339Nano), "data": data})
				entries = append(entries, types.SendMessageBatchRequestEntry{Id: aws.String(messageID), MessageBody: aws.String(string(raw)), MessageGroupId: aws.String(w.ID), MessageDeduplicationId: aws.String(messageID)})
			}
		}
		for range 10 {
			id := uuid.NewString()
			entries = append(entries, types.SendMessageBatchRequestEntry{Id: aws.String(id), MessageBody: aws.String(`{"malformed":true}]`), MessageGroupId: aws.String(id), MessageDeduplicationId: aws.String(id)})
		}
		for start := 0; start < len(entries); start += 10 {
			result, err := c.sqs.SendMessageBatch(c.ctx, &awssqs.SendMessageBatchInput{QueueUrl: aws.String(c.wager), Entries: entries[start:min(start+10, len(entries))]})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Failed) > 0 {
				t.Fatalf("batch failures=%v", result.Failed)
			}
		}
		// Then
		c.wait("source queue drained", func() bool { return c.pending(c.wager) == 0 })
		c.wait("all pending references settled", func() bool {
			return c.count("SELECT count(*) FROM wager_transactions WHERE status IN ('PENDING','PENDING_REFERENCE')") == 0
		})
		c.verify(wallets)
		if count := c.pending(c.dlq); count != 10 {
			t.Fatalf("DLQ count=%d want=10", count)
		}
		c.wait("outbox fully published", func() bool { return c.count("SELECT count(*) FROM outbox_events WHERE published_at IS NULL") == 0 })
		ids := make([]string, 0, len(wallets))
		for _, w := range wallets {
			ids = append(ids, w.ID)
		}
		expected := c.count("SELECT count(*) FROM outbox_events WHERE partition_key=ANY($1)", ids)
		observed := map[string]bool{}
		seen := map[string]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		c.wait("every committed batch event delivered", func() bool {
			output, err := c.sqs.ReceiveMessage(c.ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(c.events), MaxNumberOfMessages: 10, WaitTimeSeconds: 0})
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range output.Messages {
				var event struct {
					EventID string `json:"eventId"`
					Data    struct {
						WalletID string `json:"walletId"`
					} `json:"data"`
				}
				if err := json.Unmarshal([]byte(aws.ToString(message.Body)), &event); err != nil {
					t.Fatal(err)
				}
				if seen[event.Data.WalletID] {
					if event.EventID == "" {
						t.Fatal("event has no durable identity")
					}
					observed[event.EventID] = true
				}
				if _, err := c.sqs.DeleteMessage(c.ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(c.events), ReceiptHandle: message.ReceiptHandle}); err != nil {
					t.Fatal(err)
				}
			}
			return len(observed) == expected
		})
		if expected != walletCount*15 {
			t.Fatalf("events=%d want=%d", expected, walletCount*15)
		}
		state, _ := json.Marshal(batchState{Wallets: wallets})
		if err := os.WriteFile(setting("COMPOSE_STATE_FILE", "/tmp/jungle-compose-batch.json"), state, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wallets=%d broker_messages=%d ledger_entries=%d transactions=%d events=%d dlq=10; all balances=1040.00, consistent", walletCount, len(entries), walletCount*6, walletCount*8, len(observed))
	})
}

func TestComposeRestart(t *testing.T) {
	if os.Getenv("COMPOSE_VERIFY_RESTART") != "true" {
		t.Skip("run after restarting all three API containers with COMPOSE_VERIFY_RESTART=true")
	}
	t.Run("Given the batch persisted before restart/When every API process was replaced/Then balances and idempotent responses remain intact", func(t *testing.T) {
		// Given
		c := newBatchClient(t)
		raw, err := os.ReadFile(setting("COMPOSE_STATE_FILE", "/tmp/jungle-compose-batch.json"))
		if err != nil {
			t.Fatal(err)
		}
		var state batchState
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if len(state.Wallets) != 50 {
			t.Fatal("missing batch state")
		}
		// When / Then
		c.verify(state.Wallets)
		t.Logf("all %d wallets and cross-replica replays survived restart", len(state.Wallets))
	})
}
