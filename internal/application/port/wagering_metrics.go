package port

import "time"

type WageringMetrics interface {
	TransactionSettled(kind string, status string, origin string)
	IdempotentReplay(origin string)
	IdempotencyConflict(origin string, reason string)
	ProcessingObserved(origin string, kind string, elapsed time.Duration)
	ConcurrencyConflict(reason string)
}
