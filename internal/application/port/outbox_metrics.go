package port

import "time"

type OutboxMetrics interface {
	OutboxPublishAttempt(result string)
	OutboxPending(count int64, oldestAge time.Duration)
}
