package mapper

import (
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

func WagerResult(operation *wagering.Transaction, replay bool) dto.ProcessWagerResult {
	return dto.ProcessWagerResult{
		TransactionID:    operation.ID(),
		Status:           operation.Status(),
		Balance:          operation.ResultBalance(),
		FailureCode:      operation.FailureCode(),
		IdempotentReplay: replay,
	}
}
