package mapper

import (
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

func Reconciliation(account *wallet.Wallet, totals wallet.LedgerTotals) (dto.Reconciliation, error) {
	difference, err := account.Balance().Sub(totals.Balance)
	if err != nil {
		return dto.Reconciliation{}, err
	}

	return dto.Reconciliation{
		WalletID:          account.ID(),
		StoredBalance:     account.Balance(),
		CalculatedBalance: totals.Balance,
		Difference:        difference,
		Consistent:        difference.IsZero(),
		CheckedEntries:    totals.Entries,
	}, nil
}
