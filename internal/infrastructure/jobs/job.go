package jobs

import (
	"context"
	"time"
)

// Job is a unit of recurring work. It runs again every Interval, and each run is
// expected to drain whatever is pending instead of handling a single item.
type Job interface {
	Name() string
	Interval() time.Duration
	Run(ctx context.Context) error
}
