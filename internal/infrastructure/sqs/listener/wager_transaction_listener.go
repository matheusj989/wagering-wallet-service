package listener

import (
	"context"
	"errors"
	"log/slog"

	wagermessage "github.com/matheusj989/wagering-wallet-service/internal/application/dto/message"
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/application/idempotency"
	"github.com/matheusj989/wagering-wallet-service/internal/application/mapper"
	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/messaging"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/failpoint"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/logging"
)

const correlationAttribute = "correlationId"

// WagerTransactionListener turns a queue message into the same operation the HTTP
// door submits. Both doors end in the same use case, so the queue cannot drift
// into a second set of rules.
type WagerTransactionListener struct {
	watching string
	process  usecase.ProcessWagerTransaction
	metrics  port.ConsumerMetrics
	logger   *slog.Logger
}

func NewWagerTransactionListener(
	watching string,
	process usecase.ProcessWagerTransaction,
	metrics port.ConsumerMetrics,
	logger *slog.Logger,
) *WagerTransactionListener {
	return &WagerTransactionListener{
		watching: watching,
		process:  process,
		metrics:  metrics,
		logger:   logging.Component(logger, "wager-transaction-listener"),
	}
}

func (l *WagerTransactionListener) Name() string { return "wager-transaction-listener" }

func (l *WagerTransactionListener) Watching() string { return l.watching }

func (l *WagerTransactionListener) Handle(ctx context.Context, message Message) error {
	wager, err := wagermessage.Decode(message.Body)
	if err != nil {
		return Reject(ReasonInvalidPayload, err)
	}
	if wager.Data.Kind == string(wagering.Opening) {
		return Reject(ReasonOpeningNotAllowed, errors.New("openings are created by the internal service only"))
	}

	input, err := mapper.WagerInputFromMessage(wager, idempotency.HashBytes(message.Body), correlationOf(message, wager))
	if err != nil {
		return Reject(ReasonInvalidPayload, err)
	}

	result, err := l.process.Execute(ctx, input)
	if err == nil {
		failpoint.Hit("consumer.after_commit_before_delete")
		if result.InboxOutcome != "" {
			l.metrics.MessageRedelivered()
			l.logger.InfoContext(ctx, "message had already been handled, the stored outcome was answered",
				slog.String(logging.FieldMessageID, wager.MessageID),
				slog.String("outcome", result.InboxOutcome.String()))
		}
		return nil
	}

	if errors.Is(err, repositories.ErrCommitOutcomeUnknown) {
		return Unknown(err)
	}
	if reason := rejectionReason(err); reason != "" {
		return Reject(reason, err)
	}
	return err
}

func correlationOf(message Message, wager wagermessage.WagerMessage) string {
	if value, valid := logging.CorrelationID(message.Attribute(correlationAttribute)); valid {
		return value
	}
	if wager.MessageID != "" {
		return wager.MessageID
	}
	return message.BrokerID
}

// rejectionReason names the failures that another delivery cannot fix. Anything
// missing from this list is treated as transient, which is the safe default: the
// message comes back instead of being thrown away.
func rejectionReason(err error) string {
	var conflict *usecase.ConflictError
	if errors.As(err, &conflict) {
		return string(conflict.Kind)
	}
	if errors.Is(err, messaging.ErrPayloadMismatch) {
		return ReasonInboxMismatch
	}
	if errors.Is(err, wallet.ErrNotFound) {
		return ReasonWalletNotFound
	}

	var invalid *validation.Error
	if errors.As(err, &invalid) {
		return ReasonInvalidPayload
	}
	for _, domainError := range []error{
		wagering.ErrInvalidKind, wagering.ErrInvalidAmount, wagering.ErrInvalidOrigin,
		wagering.ErrMissingField, wagering.ErrMissingIdentifier, wagering.ErrInvalidPayloadHash,
		wagering.ErrReferenceRequired, wagering.ErrReferenceForbidden,
	} {
		if errors.Is(err, domainError) {
			return ReasonInvalidPayload
		}
	}
	return ""
}

var _ Listener = (*WagerTransactionListener)(nil)
