package message

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
)

const WagerTransactionRequested = "WagerTransactionRequested"

// WagerMessage is the contract of the wagering queue. It carries the idempotency
// key inside the body because a queue has no headers, which is the one thing that
// separates it from the HTTP contract.
type WagerMessage struct {
	MessageID  string    `json:"messageId" validate:"required,max=255,opaque"`
	Type       string    `json:"type" validate:"required"`
	OccurredAt string    `json:"occurredAt"`
	Data       WagerData `json:"data"`
}

type WagerData struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	PlayerID                       string      `json:"playerId"`
	WalletID                       string      `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID *string     `json:"referenceExternalTransactionId"`
	IdempotencyKey                 string      `json:"idempotencyKey"`
}

func Decode(body []byte) (WagerMessage, error) {
	var wager WagerMessage

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wager); err != nil {
		return WagerMessage{}, fmt.Errorf("message: body is not a valid envelope: %w", err)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return WagerMessage{}, errors.New("message: body must contain exactly one JSON document")
	}
	if err := wager.Validate(); err != nil {
		return WagerMessage{}, err
	}
	return wager, nil
}

func (m WagerMessage) Validate() error {
	problems := &validation.Error{}
	problems.Collect(validation.Struct(m), nil)
	if _, err := time.Parse(time.RFC3339Nano, m.OccurredAt); err != nil {
		problems.Add("occurredAt", "must be a timestamp in RFC 3339 format")
	}

	if m.Type != WagerTransactionRequested && m.Type != "" {
		problems.Add("type", fmt.Sprintf("must be %s", WagerTransactionRequested))
	}
	return problems.OrNil()
}

func (m WagerMessage) Reference() string {
	if m.Data.ReferenceExternalTransactionID == nil {
		return ""
	}
	return *m.Data.ReferenceExternalTransactionID
}
