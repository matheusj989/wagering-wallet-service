package mocks

//go:generate go tool mockgen -destination=repositories.go -package=mocks github.com/matheusj989/wagering-wallet-service/internal/domain/repositories UnitOfWork,Registry,Wallet,Ledger,WagerTransaction,Inbox,Outbox
//go:generate go tool mockgen -destination=port.go -package=mocks github.com/matheusj989/wagering-wallet-service/internal/application/port Clock,IDGenerator,WalletEventNotifier,Failpoint,WageringMetrics,ReconciliationMetrics,ReferenceMetrics,OutboxMetrics,ConsumerMetrics
//go:generate go tool mockgen -destination=usecase.go -package=mocks github.com/matheusj989/wagering-wallet-service/internal/application/usecase OpenWallet,ProcessWagerTransaction,FindWallet,ListWalletLedger,ReconcileWallet,FindWagerTransaction,RetryPendingReference,PublishOutbox
