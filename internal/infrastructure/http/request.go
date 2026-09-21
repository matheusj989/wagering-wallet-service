package http

import (
	"encoding/json"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

// wagerRequest is the HTTP body of an operation. The idempotency key is not here
// because HTTP carries it in a header, which is the one difference from the queue
// contract in internal/application/dto/message.
type wagerRequest struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	PlayerID                       string      `json:"playerId"`
	WalletID                       string      `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID *string     `json:"referenceExternalTransactionId"`
}

func (r wagerRequest) toInput(idempotencyKey string, correlationID string) (dto.ProcessWagerInput, error) {
	problems := &validation.Error{}
	playerID := validation.Identifier(problems, "playerId", r.PlayerID)
	walletID := validation.Identifier(problems, "walletId", r.WalletID)
	if err := problems.OrNil(); err != nil {
		return dto.ProcessWagerInput{}, err
	}

	return dto.ProcessWagerInput{
		Origin:                wagering.HTTP,
		ProviderID:            r.ProviderID,
		ExternalTransactionID: r.ExternalTransactionID,
		IdempotencyKey:        idempotencyKey,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               r.RoundID,
		GameID:                r.GameID,
		Kind:                  wagering.Kind(r.Kind),
		Amount:                r.Money,
		ReferenceExternalID:   optionalText(r.ReferenceExternalTransactionID),
		CorrelationID:         correlationID,
	}, nil
}

type openWalletRequest struct {
	PlayerID       string      `json:"playerId"`
	InitialBalance money.Money `json:"initialBalance"`
}

func (r openWalletRequest) toInput(correlationID string) (dto.OpenWalletInput, error) {
	problems := &validation.Error{}
	playerID := validation.Identifier(problems, "playerId", r.PlayerID)
	if err := problems.OrNil(); err != nil {
		return dto.OpenWalletInput{}, err
	}

	return dto.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: r.InitialBalance,
		CorrelationID:  correlationID,
	}, nil
}

func optionalText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// timestamp fixes the shape of every instant the API answers, so a client never
// has to guess the precision or the zone.
type timestamp time.Time

func (t timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC().Format("2006-01-02T15:04:05.000Z07:00"))
}

func at(moment time.Time) timestamp {
	return timestamp(moment)
}

func atOptional(moment time.Time) *timestamp {
	if moment.IsZero() {
		return nil
	}
	value := timestamp(moment)
	return &value
}
