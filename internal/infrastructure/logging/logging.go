package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

const (
	FieldCorrelationID = "correlationId"
	FieldMessageID     = "messageId"
	FieldTransactionID = "transactionId"
	FieldWalletID      = "walletId"
	FieldProviderID    = "providerId"
	FieldComponent     = "component"
	FieldInstanceID    = "instanceId"
	FieldEnvironment   = "env"
)

type contextKey struct{}

func New(level, instanceID, environment string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(level)})
	return slog.New(handler).With(
		slog.String(FieldInstanceID, instanceID),
		slog.String(FieldEnvironment, environment),
	)
}

func Component(logger *slog.Logger, name string) *slog.Logger {
	return logger.With(slog.String(FieldComponent, name))
}

func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

func FromContext(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return fallback
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

const maxCorrelationIDLength = 128

// CorrelationID accepts the identifier a caller sends only when it is printable
// ASCII within the limit, so a header cannot inject line breaks into the log.
func CorrelationID(value string) (string, bool) {
	if value == "" || len(value) > maxCorrelationIDLength {
		return "", false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return "", false
		}
	}
	return value, true
}
