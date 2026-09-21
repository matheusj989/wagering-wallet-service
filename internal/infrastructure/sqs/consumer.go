package sqs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/logging"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs/listener"
)

const reasonRetriesExhausted = "RETRIES_EXHAUSTED"

type ConsumerSettings struct {
	Workers         int
	WaitTime        time.Duration
	Visibility      time.Duration
	MessageDeadline time.Duration
	BackoffBase     time.Duration
	BackoffMax      time.Duration
	MaxReceiveCount int
	ShutdownTimeout time.Duration
}

// Consumer polls the queues its listeners watch and routes each message to the
// one that watches that queue. It owns the acknowledgement: a listener that
// returns nil gets its message deleted, and a listener that returns an error
// never does.
type Consumer struct {
	client          *sqs.Client
	deadLetterQueue string
	listeners       map[string]listener.Listener
	sources         map[string]string
	metrics         port.ConsumerMetrics
	base            *slog.Logger
	logger          *slog.Logger
	settings        ConsumerSettings

	cancel     context.CancelFunc
	abort      context.CancelFunc
	processing context.Context
	stopped    chan struct{}
	mutex      sync.Mutex
	inFlight   map[string]delivery
	releasing  bool
}

type delivery struct {
	queue   string
	message types.Message
}

func NewConsumer(
	client *sqs.Client,
	deadLetterQueue string,
	metrics port.ConsumerMetrics,
	logger *slog.Logger,
	settings ConsumerSettings,
	listeners ...listener.Listener,
) (*Consumer, error) {
	if settings.ShutdownTimeout <= 0 {
		settings.ShutdownTimeout = 15 * time.Second
	}
	routes := make(map[string]listener.Listener, len(listeners))
	for _, target := range listeners {
		watching := target.Watching()
		if watching == "" {
			return nil, fmt.Errorf("sqs: listener %s does not say which queue it watches", target.Name())
		}
		if existing, taken := routes[watching]; taken {
			return nil, fmt.Errorf("sqs: %s and %s watch the same queue", existing.Name(), target.Name())
		}
		routes[watching] = target
	}

	return &Consumer{
		client:          client,
		deadLetterQueue: deadLetterQueue,
		listeners:       routes,
		sources:         make(map[string]string, len(routes)),
		metrics:         metrics,
		base:            logger,
		logger:          logging.Component(logger, "sqs-consumer"),
		settings:        settings,
		inFlight:        make(map[string]delivery),
	}, nil
}

func (c *Consumer) Run(ctx context.Context) error {
	for queueURL := range c.listeners {
		arn, err := QueueARN(ctx, c.client, queueURL)
		if err != nil {
			return err
		}
		c.sources[queueURL] = arn
	}

	var watchers sync.WaitGroup
	for queueURL, target := range c.listeners {
		watchers.Add(1)
		go func() {
			defer watchers.Done()
			c.watch(ctx, queueURL, target)
		}()
	}
	watchers.Wait()

	c.logger.Info("consumer stopped")
	return nil
}

func (c *Consumer) watch(ctx context.Context, queueURL string, target listener.Listener) {
	logger := logging.Component(c.base, target.Name())
	logger.Info("listening", slog.Int("concurrency", c.settings.Workers))

	slots := make(chan struct{}, c.settings.Workers)
	var handlers sync.WaitGroup
	defer func() {
		handlers.Wait()
		logger.Info("listener stopped")
	}()

	for ctx.Err() == nil {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		messages, err := c.receive(ctx, queueURL)
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				return
			}
			logger.Warn("could not receive messages", slog.String("cause", err.Error()))
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		if len(messages) == 0 {
			<-slots
			continue
		}

		for _, message := range messages {
			handlers.Add(1)
			go func() {
				defer func() {
					<-slots
					handlers.Done()
				}()
				parent := c.processing
				if parent == nil {
					parent = ctx
				}
				c.dispatch(parent, queueURL, target, message, logger)
			}()
		}
	}
}

func (c *Consumer) receive(ctx context.Context, queueURL string) ([]types.Message, error) {
	output, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              aws.String(queueURL),
		MaxNumberOfMessages:   1,
		WaitTimeSeconds:       int32(c.settings.WaitTime.Seconds()),
		VisibilityTimeout:     int32(c.settings.Visibility.Seconds()),
		MessageAttributeNames: []string{"All"},
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
			types.MessageSystemAttributeNameMessageGroupId,
		},
	})
	if err != nil {
		return nil, err
	}
	return output.Messages, nil
}

func (c *Consumer) dispatch(
	parent context.Context,
	queueURL string,
	target listener.Listener,
	message types.Message,
	logger *slog.Logger,
) {
	key := aws.ToString(message.ReceiptHandle)
	c.mutex.Lock()
	if c.releasing {
		c.mutex.Unlock()
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), brokerTimeout)
		defer cancel()
		c.release(releaseCtx, delivery{queueURL, message})
		return
	}
	c.inFlight[key] = delivery{queueURL, message}
	c.mutex.Unlock()
	defer func() {
		if parent.Err() == nil {
			c.mutex.Lock()
			delete(c.inFlight, key)
			c.mutex.Unlock()
		}
	}()
	c.metrics.MessageReceived()

	ctx, cancel := context.WithTimeout(parent, c.settings.MessageDeadline)
	defer cancel()

	err := target.Handle(ctx, deliveryOf(message))
	if parent.Err() != nil {
		return
	}
	brokerCtx, brokerCancel := context.WithTimeout(parent, brokerTimeout)
	defer brokerCancel()
	brokerID := aws.ToString(message.MessageId)

	var rejection *listener.Rejection
	switch {
	case err == nil:
		c.remove(brokerCtx, queueURL, message, brokerID, logger)

	case errors.Is(err, listener.ErrUnknownOutcome):
		logger.WarnContext(ctx, "the outcome is unknown, the message stays in the queue",
			slog.String(logging.FieldMessageID, brokerID),
			slog.String("cause", err.Error()))

	case errors.As(err, &rejection):
		c.deadLetter(brokerCtx, queueURL, message, rejection, logger)

	default:
		c.retryLater(brokerCtx, queueURL, message, brokerID, err, logger)
	}
}

func deliveryOf(message types.Message) listener.Message {
	attributes := make(map[string]string, len(message.MessageAttributes))
	for name, value := range message.MessageAttributes {
		attributes[name] = aws.ToString(value.StringValue)
	}

	return listener.Message{
		BrokerID:     aws.ToString(message.MessageId),
		Body:         []byte(aws.ToString(message.Body)),
		GroupID:      groupOf(message),
		ReceiveCount: receiveCount(message),
		Attributes:   attributes,
	}
}

func (c *Consumer) remove(ctx context.Context, queueURL string, message types.Message, messageID string, logger *slog.Logger) {
	if _, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(queueURL),
		ReceiptHandle: message.ReceiptHandle,
	}); err != nil {
		logger.WarnContext(ctx, "could not delete a handled message, it will be redelivered",
			slog.String(logging.FieldMessageID, messageID),
			slog.String("cause", err.Error()))
	}
}

func (c *Consumer) retryLater(
	ctx context.Context,
	queueURL string,
	message types.Message,
	messageID string,
	cause error,
	logger *slog.Logger,
) {
	received := receiveCount(message)
	if received >= c.settings.MaxReceiveCount {
		c.metrics.MessageDeadLettered(reasonRetriesExhausted)
	}
	c.metrics.MessageRetried()

	delay := backoff(c.settings.BackoffBase, c.settings.BackoffMax, received-1)
	if _, err := c.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(queueURL),
		ReceiptHandle:     message.ReceiptHandle,
		VisibilityTimeout: int32(delay.Seconds()),
	}); err != nil {
		logger.WarnContext(ctx, "could not reschedule a message",
			slog.String(logging.FieldMessageID, messageID),
			slog.String("cause", err.Error()))
	}

	logger.WarnContext(ctx, "message handling failed and will be retried",
		slog.String(logging.FieldMessageID, messageID),
		slog.Int("receiveCount", received),
		slog.Duration("retryIn", delay),
		slog.String("cause", cause.Error()))
}

func (c *Consumer) deadLetter(
	ctx context.Context,
	queueURL string,
	message types.Message,
	rejection *listener.Rejection,
	logger *slog.Logger,
) {
	brokerID := aws.ToString(message.MessageId)
	deduplicationID, err := deadLetterDeduplicationID(c.sources[queueURL], brokerID, rejection.Reason)
	if err != nil {
		logger.ErrorContext(ctx, "could not build the dead letter identity", slog.String("cause", err.Error()))
		return
	}

	_, err = c.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(c.deadLetterQueue),
		MessageBody:            message.Body,
		MessageGroupId:         aws.String(groupOf(message)),
		MessageDeduplicationId: aws.String(deduplicationID),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"reason":            StringAttribute(rejection.Reason),
			"originalMessageId": StringAttribute(brokerID),
			"receiveCount":      NumberAttribute(receiveCount(message)),
		},
	})
	if err != nil {
		logger.ErrorContext(ctx, "could not move a message to the dead letter queue, it stays in the queue",
			slog.String("reason", rejection.Reason),
			slog.String("cause", err.Error()))
		return
	}

	c.metrics.MessageDeadLettered(rejection.Reason)
	logger.WarnContext(ctx, "message moved to the dead letter queue",
		slog.String("reason", rejection.Reason),
		slog.String("originalMessageId", brokerID),
		slog.String("cause", rejection.Error()))

	c.remove(ctx, queueURL, message, brokerID, logger)
}

func receiveCount(message types.Message) int {
	raw, ok := message.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]
	if !ok {
		return 1
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 1
	}
	return value
}

func groupOf(message types.Message) string {
	if group, ok := message.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]; ok && group != "" {
		return group
	}
	return aws.ToString(message.MessageId)
}

func backoff(base time.Duration, maximum time.Duration, exponent int) time.Duration {
	delay := base
	for range max(exponent, 0) {
		delay *= 2
		if delay >= maximum {
			return maximum
		}
	}
	return min(delay, maximum)
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Consumer) Start(parent context.Context) {
	c.processing, c.abort = context.WithCancel(context.WithoutCancel(parent))
	ctx, cancel := context.WithCancel(c.processing)
	c.cancel = cancel
	c.stopped = make(chan struct{})

	go func() {
		defer close(c.stopped)
		if err := c.Run(ctx); err != nil && ctx.Err() == nil {
			c.logger.Error("consumer stopped with an error", slog.String("cause", err.Error()))
		}
	}()
}

func (c *Consumer) StopReceiving() {
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *Consumer) Stop(ctx context.Context) error {
	if c.cancel == nil {
		return nil
	}
	c.StopReceiving()
	stopCtx, cancel := context.WithTimeout(ctx, c.settings.ShutdownTimeout)
	defer cancel()
	deadline, _ := stopCtx.Deadline()
	reserve := min(time.Second, time.Until(deadline)/4)
	drainCtx, drainCancel := context.WithDeadline(stopCtx, deadline.Add(-reserve))
	defer drainCancel()

	select {
	case <-c.stopped:
		c.abort()
		return nil
	case <-drainCtx.Done():
	}
	c.mutex.Lock()
	c.releasing = true
	unfinished := make([]delivery, 0, len(c.inFlight))
	for _, message := range c.inFlight {
		unfinished = append(unfinished, message)
	}
	c.mutex.Unlock()
	c.abort()
	var releases sync.WaitGroup
	for _, message := range unfinished {
		releases.Add(1)
		go func() {
			defer releases.Done()
			c.release(stopCtx, message)
		}()
	}
	releases.Wait()
	select {
	case <-c.stopped:
		return nil
	case <-stopCtx.Done():
		return stopCtx.Err()
	}
}

const brokerTimeout = 5 * time.Second

func (c *Consumer) release(ctx context.Context, pending delivery) {
	_, err := c.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(pending.queue), ReceiptHandle: pending.message.ReceiptHandle, VisibilityTimeout: 0,
	})
	if err != nil {
		c.logger.Warn("could not release an unfinished message", slog.String("cause", err.Error()))
		return
	}
	c.logger.Info("unfinished message released", slog.String("messageId", aws.ToString(pending.message.MessageId)))
}
