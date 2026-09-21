package usecase

import (
	"context"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/event"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

func appendSettlementEvents(
	ctx context.Context,
	registry repositories.Registry,
	ids port.IDGenerator,
	now time.Time,
	operation *wagering.Transaction,
	entry *wallet.LedgerEntry,
	balance money.Money,
) error {
	envelopes := make([]event.Envelope, 0, 2)

	switch operation.Status() {
	case wagering.Processed:
		processed, err := event.NewProcessed(ids.New(), operation, balance)
		if err != nil {
			return err
		}
		envelopes = append(envelopes, processed)

		if entry != nil {
			changed, err := event.NewBalanceChanged(ids.New(), operation, *entry)
			if err != nil {
				return err
			}
			envelopes = append(envelopes, changed)
		}
	case wagering.Rejected:
		rejected, err := event.NewRejected(ids.New(), operation, balance)
		if err != nil {
			return err
		}
		envelopes = append(envelopes, rejected)
	case wagering.PendingReference:
		pending, err := event.NewPendingReference(ids.New(), operation)
		if err != nil {
			return err
		}
		envelopes = append(envelopes, pending)
	default:
		return nil
	}

	for _, envelope := range envelopes {
		outboxEvent, err := messaging.NewOutboxEvent(envelope, operation.WalletID(), now)
		if err != nil {
			return err
		}
		if err := registry.Outbox().Append(ctx, outboxEvent); err != nil {
			return err
		}
	}
	return nil
}
