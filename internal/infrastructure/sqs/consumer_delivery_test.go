package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/observability"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs/listener"
	"github.com/prometheus/client_golang/prometheus"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

type handlingFunc func(context.Context, listener.Message) error

func (f handlingFunc) Name() string                                         { return "test" }
func (f handlingFunc) Watching() string                                     { return "http://sqs.test/000/wager.fifo" }
func (f handlingFunc) Handle(ctx context.Context, m listener.Message) error { return f(ctx, m) }

func deliveryConsumer(t *testing.T, transport transportFunc) *Consumer {
	t.Helper()
	client := awssqs.New(awssqs.Options{Region: "us-east-1", BaseEndpoint: aws.String("http://sqs.test"), Credentials: aws.AnonymousCredentials{}, HTTPClient: transport, RetryMaxAttempts: 1})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := NewConsumer(client, "http://sqs.test/000/dlq.fifo", observability.NewMetrics(prometheus.NewRegistry()), logger, ConsumerSettings{Workers: 1, MessageDeadline: time.Millisecond, BackoffBase: 2 * time.Second, BackoffMax: 10 * time.Second, MaxReceiveCount: 5})
	if err != nil {
		t.Fatal(err)
	}
	c.sources["http://sqs.test/000/wager.fifo"] = "arn:aws:sqs:us-east-1:000:wager.fifo"
	return c
}
func deliveryMessage() types.Message {
	return types.Message{MessageId: aws.String("broker-id"), ReceiptHandle: aws.String("receipt"), Body: aws.String("{}")}
}
func sdkResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestConsumerDelivery(t *testing.T) {
	t.Run("Given an expired processing deadline/When handling fails/Then visibility uses an independent live context", func(t *testing.T) {
		// Given
		calls := 0
		c := deliveryConsumer(t, func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Context().Err() != nil {
				t.Error("broker context already expired")
			}
			if !strings.HasSuffix(r.Header.Get("X-Amz-Target"), "ChangeMessageVisibility") {
				t.Errorf("unexpected action %s", r.Header.Get("X-Amz-Target"))
			}
			var payload struct{ VisibilityTimeout int }
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.VisibilityTimeout != 2 {
				t.Errorf("backoff=%d", payload.VisibilityTimeout)
			}
			return sdkResponse("{}"), nil
		})
		handle := handlingFunc(func(ctx context.Context, _ listener.Message) error { <-ctx.Done(); return ctx.Err() })
		// When
		c.dispatch(context.Background(), handle.Watching(), handle, deliveryMessage(), c.logger)
		// Then
		if calls != 1 {
			t.Fatalf("visibility calls=%d", calls)
		}
	})
	for _, failure := range []error{errors.New("broker unavailable"), context.DeadlineExceeded} {
		t.Run("Given a DLQ publication failure "+failure.Error()+"/When it is retried/Then the source is deleted only after confirmed delivery", func(t *testing.T) {
			// Given
			failing := true
			deletes := 0
			var identities []string
			c := deliveryConsumer(t, func(r *http.Request) (*http.Response, error) {
				action := r.Header.Get("X-Amz-Target")
				if strings.HasSuffix(action, "SendMessage") {
					var payload struct{ MessageDeduplicationId string }
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					identities = append(identities, payload.MessageDeduplicationId)
					if failing {
						return nil, failure
					}
					return sdkResponse(`{"MessageId":"dlq-message"}`), nil
				}
				if strings.HasSuffix(action, "DeleteMessage") {
					deletes++
					return sdkResponse("{}"), nil
				}
				t.Fatalf("unexpected action %s", action)
				return nil, nil
			})
			handle := handlingFunc(func(context.Context, listener.Message) error {
				return &listener.Rejection{Reason: "INVALID_MESSAGE", Cause: errors.New("bad envelope")}
			})
			// When
			c.dispatch(context.Background(), handle.Watching(), handle, deliveryMessage(), c.logger)
			// Then
			if deletes != 0 {
				t.Fatal("source deleted without a confirmed DLQ publication")
			}
			failing = false
			c.dispatch(context.Background(), handle.Watching(), handle, deliveryMessage(), c.logger)
			if deletes != 1 || len(identities) != 2 || identities[0] == "" || identities[0] != identities[1] {
				t.Fatalf("deletes=%d identities=%v", deletes, identities)
			}
		})
	}
}
