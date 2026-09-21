package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
)

type RetryPendingReferenceJob struct {
	retry    usecase.RetryPendingReference
	interval time.Duration
}

func NewRetryPendingReferenceJob(retry usecase.RetryPendingReference, interval time.Duration) *RetryPendingReferenceJob {
	return &RetryPendingReferenceJob{retry: retry, interval: interval}
}

func (j *RetryPendingReferenceJob) Name() string { return "reference-worker" }

func (j *RetryPendingReferenceJob) Interval() time.Duration { return j.interval }

func (j *RetryPendingReferenceJob) Run(ctx context.Context) error {
	_, err := j.retry.Execute(ctx)
	return errors.Join(err, j.retry.ReportPending(ctx))
}

var _ Job = (*RetryPendingReferenceJob)(nil)
