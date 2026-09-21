package sqs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func InboundDeduplicationID(messageID string) string {
	digest := sha256.Sum256([]byte(messageID))
	return hex.EncodeToString(digest[:])
}

func deadLetterDeduplicationID(sourceQueueARN string, brokerMessageID string, reason string) (string, error) {
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode([]string{"wallet-dlq-v1", sourceQueueARN, brokerMessageID, reason}); err != nil {
		return "", err
	}
	digest := sha256.Sum256(bytes.TrimRight(canonical.Bytes(), "\n"))
	return hex.EncodeToString(digest[:]), nil
}
