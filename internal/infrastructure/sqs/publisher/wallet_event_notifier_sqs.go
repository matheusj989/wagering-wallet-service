package publisher

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs"
)

// WalletEventNotifier sends wallet events to the outbound FIFO queue. The message
// group is the wallet, so everything about one wallet stays in order, and the
// deduplication id is the event id, so a redelivered publication is collapsed by
// the queue instead of reaching consumers twice.
type WalletEventNotifier struct {
	client   *awssqs.Client
	queueURL string
}

func NewWalletEventNotifier(client *awssqs.Client, queueURL string) *WalletEventNotifier {
	return &WalletEventNotifier{client: client, queueURL: queueURL}
}

func (n *WalletEventNotifier) Send(ctx context.Context, event messaging.OutboxEvent) error {
	_, err := n.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(n.queueURL),
		MessageBody:            aws.String(string(event.Payload())),
		MessageGroupId:         aws.String(event.PartitionKey()),
		MessageDeduplicationId: aws.String(event.ID().String()),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"eventType":     sqs.StringAttribute(event.EventType()),
			"eventVersion":  sqs.NumberAttribute(event.EventVersion()),
			"aggregateType": sqs.StringAttribute(event.AggregateType()),
			"correlationId": sqs.StringAttribute(event.CorrelationID()),
		},
	})
	if err != nil {
		return fmt.Errorf("sqs: could not publish event %s: %w", event.ID(), err)
	}
	return nil
}

var _ port.WalletEventNotifier = (*WalletEventNotifier)(nil)
