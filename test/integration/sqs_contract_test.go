//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs"
	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

// TestSQSEmulatorContract checks the queue behaviour the service depends on, with
// the same SDK the service uses. If the emulator ever stops honouring one of these,
// the failure points at the queue and not at the wallet.
func TestSQSEmulatorContract(t *testing.T) {
	env := testenv.New(t)
	client := env.Queues.Client()
	ctx := context.Background()

	receive := func(visibility int32) []types.Message {
		t.Helper()
		output, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(env.Queues.Wager),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     0,
			VisibilityTimeout:   visibility,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameApproximateReceiveCount,
				types.MessageSystemAttributeNameMessageGroupId,
			},
		})
		if err != nil {
			t.Fatalf("receive failed: %v", err)
		}
		return output.Messages
	}

	t.Run("Given a fifo queue with redrive/When a message keeps failing/Then it moves to the dead letter queue", func(t *testing.T) {
		// Given
		env.Queues.SendRaw("contract-redrive", "group-a", "first")

		// When
		first := receive(1)
		if len(first) != 1 || aws.ToString(first[0].Body) != "first" {
			t.Fatalf("the first receive should deliver the message, got %d", len(first))
		}
		if count := first[0].Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]; count != "1" {
			t.Errorf("receive count = %q, want 1", count)
		}
		if invisible := receive(1); len(invisible) != 0 {
			t.Error("the message should be invisible while its visibility timeout runs")
		}

		testenv.Eventually(t, 10*time.Second, "the message comes back after the visibility timeout", func() (bool, string) {
			again := receive(1)
			if len(again) == 0 {
				return false, "still invisible"
			}
			if count := again[0].Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]; count != "2" {
				return false, "receive count is " + count
			}
			return true, ""
		})

		// Then
		testenv.Eventually(t, 20*time.Second, "the message reaches the dead letter queue", func() (bool, string) {
			for _, message := range env.Queues.DeadLetters(5) {
				if message.Body == "first" {
					return true, ""
				}
			}
			receive(1)
			return false, "the dead letter queue is still empty"
		})
	})

	t.Run("Given a message in flight/When its visibility is set to zero/Then it is delivered again at once", func(t *testing.T) {
		// Given
		env.Queues.SendRaw("contract-visibility", "group-b", "second")
		inFlight := receive(30)
		if len(inFlight) != 1 {
			t.Fatalf("the message should be delivered, got %d", len(inFlight))
		}

		// When
		if _, err := client.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
			QueueUrl:          aws.String(env.Queues.Wager),
			ReceiptHandle:     inFlight[0].ReceiptHandle,
			VisibilityTimeout: 0,
		}); err != nil {
			t.Fatalf("the visibility could not be changed: %v", err)
		}

		// Then
		released := receive(30)
		if len(released) != 1 || aws.ToString(released[0].Body) != "second" {
			t.Fatalf("the message should come back at once, got %d", len(released))
		}
		if _, err := client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
			QueueUrl:      aws.String(env.Queues.Wager),
			ReceiptHandle: released[0].ReceiptHandle,
		}); err != nil {
			t.Fatalf("the message could not be deleted: %v", err)
		}
		if left := receive(1); len(left) != 0 {
			t.Error("a deleted message should not be delivered again")
		}
	})

	t.Run("Given several groups/When one message is in flight/Then its group waits and the others do not", func(t *testing.T) {
		// Given
		for index, body := range []string{"c-1", "c-2"} {
			env.Queues.SendRaw(fmt.Sprintf("contract-group-c-%d", index), "group-c", body)
		}
		env.Queues.SendRaw("contract-group-d", "group-d", "d-1")

		// When
		held := receive(30)
		if len(held) != 1 || aws.ToString(held[0].Body) != "c-1" {
			t.Fatalf("the first message of the group should come first, got %v", held)
		}
		other := receive(30)

		// Then
		if len(other) != 1 || other[0].Attributes[string(types.MessageSystemAttributeNameMessageGroupId)] != "group-d" {
			t.Fatalf("while a group is busy only another group is delivered, got %v", other)
		}
		if _, err := client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
			QueueUrl:      aws.String(env.Queues.Wager),
			ReceiptHandle: held[0].ReceiptHandle,
		}); err != nil {
			t.Fatalf("the message could not be deleted: %v", err)
		}
		next := receive(30)
		if len(next) != 1 || aws.ToString(next[0].Body) != "c-2" {
			t.Fatalf("the group should continue in order, got %v", next)
		}
	})

	t.Run("Given the same deduplication identity/When it is published twice/Then only one message is delivered", func(t *testing.T) {
		// Given
		messageID := "contract-dedup"

		// When
		env.Queues.SendRaw(messageID, "group-e", "dedup")
		env.Queues.SendRaw(messageID, "group-e", "dedup")

		// Then
		delivered := 0
		for range 3 {
			messages := receive(30)
			delivered += len(messages)
			for _, message := range messages {
				if _, err := client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
					QueueUrl:      aws.String(env.Queues.Wager),
					ReceiptHandle: message.ReceiptHandle,
				}); err != nil {
					t.Fatalf("the message could not be deleted: %v", err)
				}
			}
		}
		if delivered != 1 {
			t.Errorf("messages delivered = %d, want 1 for the same deduplication identity", delivered)
		}
		if len(sqs.InboundDeduplicationID(messageID)) != 64 {
			t.Error("the transport identity should be a 64 character hash")
		}
	})
}
