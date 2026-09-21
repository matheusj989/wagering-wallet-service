package mapper

import (
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/idempotency"
)

func IdempotencyPayload(input dto.ProcessWagerInput) idempotency.Payload {
	return idempotency.Payload{
		ProviderID:            input.ProviderID,
		ExternalTransactionID: input.ExternalTransactionID,
		PlayerID:              input.PlayerID,
		WalletID:              input.WalletID,
		RoundID:               input.RoundID,
		GameID:                input.GameID,
		Kind:                  input.Kind,
		Amount:                input.Amount,
		ReferenceExternalID:   input.ReferenceExternalID,
	}
}
