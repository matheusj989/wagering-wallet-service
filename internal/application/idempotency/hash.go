package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

type Payload struct {
	ProviderID            string
	ExternalTransactionID string
	PlayerID              uuid.UUID
	WalletID              uuid.UUID
	RoundID               string
	GameID                string
	Kind                  wagering.Kind
	Amount                money.Money
	ReferenceExternalID   string
}

func Hash(payload Payload) (string, error) {
	fields := map[string]any{
		"externalTransactionId": payload.ExternalTransactionID,
		"gameId":                payload.GameID,
		"kind":                  payload.Kind.String(),
		"money": map[string]string{
			"amount":   payload.Amount.String(),
			"currency": payload.Amount.Currency().String(),
		},
		"playerId":   strings.ToLower(payload.PlayerID.String()),
		"providerId": payload.ProviderID,
		"roundId":    payload.RoundID,
		"walletId":   strings.ToLower(payload.WalletID.String()),
	}
	if payload.ReferenceExternalID != "" {
		fields["referenceExternalTransactionId"] = payload.ReferenceExternalID
	}

	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(fields); err != nil {
		return "", err
	}

	digest := sha256.Sum256(bytes.TrimRight(canonical.Bytes(), "\n"))
	return hex.EncodeToString(digest[:]), nil
}

func HashBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
