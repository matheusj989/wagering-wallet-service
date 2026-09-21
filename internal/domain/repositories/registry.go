package repositories

type Registry interface {
	Wallets() Wallet
	Ledger() Ledger
	Transactions() WagerTransaction
	Inbox() Inbox
	Outbox() Outbox
}
