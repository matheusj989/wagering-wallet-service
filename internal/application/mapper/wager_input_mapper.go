package mapper

import (
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/message"
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

func WagerInputFromMessage(wager message.WagerMessage, rawBodyHash string, correlationID string) (dto.ProcessWagerInput, error) {
	problems := &validation.Error{}
	playerID := validation.Identifier(problems, "playerId", wager.Data.PlayerID)
	walletID := validation.Identifier(problems, "walletId", wager.Data.WalletID)
	if err := problems.OrNil(); err != nil {
		return dto.ProcessWagerInput{}, err
	}

	return dto.ProcessWagerInput{
		Origin:                wagering.Queue,
		ProviderID:            wager.Data.ProviderID,
		ExternalTransactionID: wager.Data.ExternalTransactionID,
		IdempotencyKey:        wager.Data.IdempotencyKey,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               wager.Data.RoundID,
		GameID:                wager.Data.GameID,
		Kind:                  wagering.Kind(wager.Data.Kind),
		Amount:                wager.Data.Money,
		ReferenceExternalID:   wager.Reference(),
		CorrelationID:         correlationID,
		MessageID:             wager.MessageID,
		RawBodyHash:           rawBodyHash,
	}, nil
}
