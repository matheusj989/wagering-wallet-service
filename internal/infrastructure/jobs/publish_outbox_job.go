package jobs

import (
	"context"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
)

const backlogEvery = 20

type PublishOutboxJob struct {
	publish  usecase.PublishOutbox
	interval time.Duration
	runs     int
}

func NewPublishOutboxJob(publish usecase.PublishOutbox, interval time.Duration) *PublishOutboxJob {
	return &PublishOutboxJob{publish: publish, interval: interval}
}

func (j *PublishOutboxJob) Name() string { return "outbox-publisher" }

func (j *PublishOutboxJob) Interval() time.Duration { return j.interval }

func (j *PublishOutboxJob) Run(ctx context.Context) error {
	j.runs++
	if j.runs%backlogEvery == 1 {
		if err := j.publish.ReportPending(ctx); err != nil {
			return err
		}
	}
	_, err := j.publish.Execute(ctx)
	return err
}

var _ Job = (*PublishOutboxJob)(nil)
