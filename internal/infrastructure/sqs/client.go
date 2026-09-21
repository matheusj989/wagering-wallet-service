package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type Settings struct {
	Region      string
	Endpoint    string
	AccessKeyID string
	SecretKey   string
}

type Queues struct {
	Wager  string
	DLQ    string
	Events string
}

func NewClient(ctx context.Context, settings Settings) (*sqs.Client, error) {
	configuration, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(settings.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(settings.AccessKeyID, settings.SecretKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("sqs: could not load the aws configuration: %w", err)
	}

	return sqs.NewFromConfig(configuration, func(options *sqs.Options) {
		if settings.Endpoint != "" {
			options.BaseEndpoint = aws.String(settings.Endpoint)
		}
	}), nil
}

func VerifyQueues(ctx context.Context, client *sqs.Client, queues Queues) error {
	for name, url := range map[string]string{
		"wager-transactions":     queues.Wager,
		"wager-transactions-dlq": queues.DLQ,
		"wallet-events":          queues.Events,
	} {
		if _, err := attributes(ctx, client, url); err != nil {
			return fmt.Errorf("sqs: queue %s is not reachable at %s: %w", name, url, err)
		}
	}
	return nil
}

func QueueARN(ctx context.Context, client *sqs.Client, queueURL string) (string, error) {
	found, err := attributes(ctx, client, queueURL)
	if err != nil {
		return "", err
	}
	arn, ok := found[string(types.QueueAttributeNameQueueArn)]
	if !ok {
		return "", fmt.Errorf("sqs: queue %s did not report its arn", queueURL)
	}
	return arn, nil
}

func attributes(ctx context.Context, client *sqs.Client, queueURL string) (map[string]string, error) {
	output, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return nil, err
	}
	return output.Attributes, nil
}
