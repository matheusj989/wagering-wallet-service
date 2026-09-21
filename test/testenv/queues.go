//go:build integration

package testenv

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs"
)

const (
	testVisibilityTimeout = "5"
	testMaxReceiveCount   = "2"
)

// Queues owns the three FIFO queues of a single test: the wagering queue with its
// redrive policy, the dead letter queue and the outbound events queue.
type Queues struct {
	t      *testing.T
	client *awssqs.Client

	Wager  string
	DLQ    string
	Events string
}

func newQueues(t *testing.T, booted *suite, suffix string) *Queues {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := sqs.NewClient(ctx, sqs.Settings{
		Region:      "us-east-1",
		Endpoint:    booted.sqsEndpoint(),
		AccessKeyID: "test",
		SecretKey:   "test",
	})
	if err != nil {
		t.Fatalf("sqs client could not be created: %v", err)
	}

	queues := &Queues{t: t, client: client}
	queues.DLQ = queues.create(ctx, fmt.Sprintf("wager-dlq-%s.fifo", suffix), map[string]string{"FifoQueue": "true"})

	arn, err := sqs.QueueARN(ctx, client, queues.DLQ)
	if err != nil {
		t.Fatalf("dead letter queue arn could not be read: %v", err)
	}
	redrive, err := json.Marshal(map[string]string{"deadLetterTargetArn": arn, "maxReceiveCount": testMaxReceiveCount})
	if err != nil {
		t.Fatalf("redrive policy could not be built: %v", err)
	}

	queues.Wager = queues.create(ctx, fmt.Sprintf("wager-%s.fifo", suffix), map[string]string{
		"FifoQueue":         "true",
		"VisibilityTimeout": testVisibilityTimeout,
		"RedrivePolicy":     string(redrive),
	})
	queues.Events = queues.create(ctx, fmt.Sprintf("wallet-events-%s.fifo", suffix), map[string]string{"FifoQueue": "true"})
	return queues
}

// Client exposes the SDK client so a test can exercise queue behaviour directly,
// with the same transport the service uses.
func (q *Queues) Client() *awssqs.Client {
	return q.client
}

func (q *Queues) create(ctx context.Context, name string, attributes map[string]string) string {
	q.t.Helper()

	output, err := q.client.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName:  aws.String(name),
		Attributes: attributes,
	})
	if err != nil {
		q.t.Fatalf("queue %s could not be created: %v", name, err)
	}
	url := aws.ToString(output.QueueUrl)

	q.t.Cleanup(func() {
		deleteCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := q.client.DeleteQueue(deleteCtx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(url)}); err != nil {
			q.t.Logf("queue %s could not be deleted: %v", name, err)
		}
	})
	return url
}

// Send publishes a wagering message the way the trusted ingestion service would:
// the wallet drives the message group and the transport identity is derived from
// the business message id.
func (q *Queues) Send(messageID string, groupID string, body any) {
	q.t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		q.t.Fatalf("message could not be encoded: %v", err)
	}
	q.SendRaw(messageID, groupID, string(payload))
}

// SendWithCorrelation publishes the way the ingestion service would when it
// already has a correlation id to carry across the hop.
func (q *Queues) SendWithCorrelation(messageID string, groupID string, correlationID string, body any) {
	q.t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		q.t.Fatalf("message could not be encoded: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := q.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(q.Wager),
		MessageBody:            aws.String(string(payload)),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(sqs.InboundDeduplicationID(messageID)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"correlationId": {DataType: aws.String("String"), StringValue: aws.String(correlationID)},
		},
	}); err != nil {
		q.t.Fatalf("message %s could not be published: %v", messageID, err)
	}
}

func (q *Queues) SendRaw(messageID string, groupID string, body string) {
	q.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := q.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(q.Wager),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(sqs.InboundDeduplicationID(messageID)),
	}); err != nil {
		q.t.Fatalf("message %s could not be published: %v", messageID, err)
	}
}

type Received struct {
	Body       string
	Attributes map[string]string
	GroupID    string
}

func (q *Queues) Drain(queueURL string, limit int) []Received {
	q.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	collected := make([]Received, 0, limit)
	for len(collected) < limit {
		output, err := q.client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:              aws.String(queueURL),
			MaxNumberOfMessages:   10,
			WaitTimeSeconds:       0,
			VisibilityTimeout:     30,
			MessageAttributeNames: []string{"All"},
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameMessageGroupId,
			},
		})
		if err != nil {
			q.t.Fatalf("queue %s could not be read: %v", queueURL, err)
		}
		if len(output.Messages) == 0 {
			break
		}
		for _, message := range output.Messages {
			attributes := make(map[string]string, len(message.MessageAttributes))
			for name, value := range message.MessageAttributes {
				attributes[name] = aws.ToString(value.StringValue)
			}
			collected = append(collected, Received{
				Body:       aws.ToString(message.Body),
				Attributes: attributes,
				GroupID:    message.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
			})
			if _, err := q.client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
				QueueUrl:      aws.String(queueURL),
				ReceiptHandle: message.ReceiptHandle,
			}); err != nil {
				q.t.Fatalf("message could not be removed while draining %s: %v", queueURL, err)
			}
		}
	}
	return collected
}

func (q *Queues) EventMessages(limit int) []Received {
	return q.Drain(q.Events, limit)
}

func (q *Queues) DeadLetters(limit int) []Received {
	return q.Drain(q.DLQ, limit)
}

func (q *Queues) Pending(queueURL string) int {
	q.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	output, err := q.client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		q.t.Fatalf("queue %s attributes could not be read: %v", queueURL, err)
	}

	total := 0
	for _, name := range []types.QueueAttributeName{
		types.QueueAttributeNameApproximateNumberOfMessages,
		types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
	} {
		if raw, ok := output.Attributes[string(name)]; ok {
			var value int
			if _, err := fmt.Sscanf(raw, "%d", &value); err == nil {
				total += value
			}
		}
	}
	return total
}

// DeleteEvents removes the outbound queue so a scenario can watch the publisher
// fail and reschedule instead of losing the event.
func (q *Queues) DeleteEvents() {
	q.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := q.client.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(q.Events)}); err != nil {
		q.t.Fatalf("the events queue could not be deleted: %v", err)
	}
}
