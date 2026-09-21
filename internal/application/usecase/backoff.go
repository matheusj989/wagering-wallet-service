package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
)

func backoff(base time.Duration, maximum time.Duration, exponent int) time.Duration {
	delay := base
	for range max(exponent, 0) {
		delay *= 2
		if delay >= maximum {
			return maximum
		}
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func recoverable(err error) bool {
	return errors.Is(err, repositories.ErrTransient) ||
		errors.Is(err, repositories.ErrLockTimeout) ||
		errors.Is(err, repositories.ErrCommitOutcomeUnknown) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}
