package port

import (
	"context"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
)

// WalletEventNotifier hands a committed wallet event to whoever carries it to the
// outside world. The application states the intention; the transport is a detail.
type WalletEventNotifier interface {
	Send(ctx context.Context, event messaging.OutboxEvent) error
}
