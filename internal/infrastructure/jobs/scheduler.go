package jobs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/logging"
)

type Scheduler interface {
	Start(ctx context.Context)
	Stop(ctx context.Context) error
}

// TickerScheduler runs each job on its own goroutine and its own ticker. The work
// here is continuous draining with a sub-second cadence, not a wall clock
// appointment, so a ticker says everything a cron expression would and stops the
// moment the context is cancelled.
type TickerScheduler struct {
	jobs   []Job
	base   *slog.Logger
	logger *slog.Logger

	cancel  context.CancelFunc
	stopped sync.WaitGroup
	once    sync.Once
}

func NewTickerScheduler(logger *slog.Logger, scheduled ...Job) *TickerScheduler {
	return &TickerScheduler{jobs: scheduled, base: logger, logger: logging.Component(logger, "scheduler")}
}

func (s *TickerScheduler) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	s.cancel = cancel

	for _, job := range s.jobs {
		s.stopped.Add(1)
		go s.run(ctx, job)
	}
	s.logger.Info("scheduler started", slog.Int("jobs", len(s.jobs)))
}

func (s *TickerScheduler) run(ctx context.Context, job Job) {
	defer s.stopped.Done()

	logger := logging.Component(s.base, job.Name())
	logger.Info("job started", slog.Duration("interval", job.Interval()))

	ticker := time.NewTicker(job.Interval())
	defer ticker.Stop()

	for {
		s.runOnce(ctx, job, logger)
		select {
		case <-ctx.Done():
			logger.Info("job stopped")
			return
		case <-ticker.C:
		}
	}
}

func (s *TickerScheduler) runOnce(ctx context.Context, job Job, logger *slog.Logger) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error("job run panicked", slog.Any("cause", recovered))
		}
	}()

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := job.Run(runCtx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		level := slog.LevelError
		if errors.Is(err, repositories.ErrTransient) || errors.Is(err, repositories.ErrLockTimeout) {
			level = slog.LevelWarn
		}
		logger.Log(ctx, level, "job run failed", slog.String("cause", err.Error()))
	}
}

func (s *TickerScheduler) Stop(ctx context.Context) error {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	if s.cancel == nil {
		return nil
	}

	finished := make(chan struct{})
	go func() {
		s.stopped.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		s.logger.Info("scheduler stopped")
		return nil
	case <-ctx.Done():
		s.logger.Warn("scheduler did not stop within the deadline")
		return ctx.Err()
	}
}

var _ Scheduler = (*TickerScheduler)(nil)
