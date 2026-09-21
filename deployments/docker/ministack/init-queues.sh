#!/bin/sh
set -eu

ingestion_principal="${SQS_INGESTION_PRINCIPAL:-arn:aws:iam::000000000000:role/wager-ingestion}"
service_principal="${SQS_SERVICE_PRINCIPAL:-arn:aws:iam::000000000000:role/wallet-service}"

queue_url() {
    awslocal sqs get-queue-url --queue-name "$1" --query QueueUrl --output text
}

queue_arn() {
    awslocal sqs get-queue-attributes --queue-url "$1" --attribute-names QueueArn \
        --query 'Attributes.QueueArn' --output text
}

awslocal sqs create-queue --queue-name wager-transactions-dlq.fifo \
    --attributes FifoQueue=true >/dev/null
dlq_url=$(queue_url wager-transactions-dlq.fifo)
dlq_arn=$(queue_arn "$dlq_url")

cat > /tmp/wager-attributes.json <<JSON
{
  "FifoQueue": "true",
  "VisibilityTimeout": "30",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"${dlq_arn}\",\"maxReceiveCount\":\"5\"}"
}
JSON

awslocal sqs create-queue --queue-name wager-transactions.fifo \
    --attributes file:///tmp/wager-attributes.json >/dev/null
wager_url=$(queue_url wager-transactions.fifo)
wager_arn=$(queue_arn "$wager_url")

awslocal sqs create-queue --queue-name wallet-events.fifo \
    --attributes FifoQueue=true >/dev/null
events_url=$(queue_url wallet-events.fifo)

cat > /tmp/wager-policy.json <<JSON
{
  "Policy": "{\"Version\":\"2012-10-17\",\"Id\":\"wager-transactions-access\",\"Statement\":[{\"Sid\":\"OnlyIngestionPublishes\",\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"${ingestion_principal}\"},\"Action\":\"sqs:SendMessage\",\"Resource\":\"${wager_arn}\"},{\"Sid\":\"OnlyWalletServiceConsumes\",\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"${service_principal}\"},\"Action\":[\"sqs:ReceiveMessage\",\"sqs:DeleteMessage\",\"sqs:ChangeMessageVisibility\",\"sqs:GetQueueAttributes\"],\"Resource\":\"${wager_arn}\"}]}"
}
JSON

awslocal sqs set-queue-attributes --queue-url "$wager_url" \
    --attributes file:///tmp/wager-policy.json >/dev/null

echo "queues ready: ${wager_url} ${dlq_url} ${events_url}"
touch /tmp/wallet-queues-ready
