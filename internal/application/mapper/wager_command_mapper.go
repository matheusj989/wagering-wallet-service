package mapper

import (
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

func WagerCommand(input dto.ProcessWagerInput, payloadHash string) wagering.Command {
	return wagering.Command{
		Origin:                input.Origin,
		Kind:                  input.Kind,
		ProviderID:            input.ProviderID,
		ExternalTransactionID: input.ExternalTransactionID,
		IdempotencyKey:        input.IdempotencyKey,
		PayloadHash:           payloadHash,
		PlayerID:              input.PlayerID,
		WalletID:              input.WalletID,
		RoundID:               input.RoundID,
		GameID:                input.GameID,
		Amount:                input.Amount,
		ReferenceExternalID:   input.ReferenceExternalID,
		CorrelationID:         input.CorrelationID,
	}
}
