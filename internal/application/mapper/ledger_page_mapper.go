package mapper

import (
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

// LedgerPage receives one entry more than the page holds. That extra entry is how
// the caller learns there is a next page without a second count query.
func LedgerPage(entries []wallet.LedgerEntry, limit int) dto.LedgerPage {
	page := dto.LedgerPage{Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		page.HasMore = true
	}
	if page.HasMore && len(page.Entries) > 0 {
		page.NextCursor = page.Entries[len(page.Entries)-1].WalletVersion()
	}
	return page
}
