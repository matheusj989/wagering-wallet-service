package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/auth"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/config"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/http"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/observability"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/postgres"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs"
)

const dependencyTimeout = 5 * time.Second

type runtime struct {
	fx.In

	Config     *config.Config
	Logger     *slog.Logger
	Health     *observability.Health
	Database   *postgres.Client
	SQS        *awssqs.Client
	Queues     sqs.Queues
	Verifier   *auth.Verifier
	Components Components
	Server     *http.Server
}

func registerLifecycle(lifecycle fx.Lifecycle, runtime runtime) {
	registerDatabase(lifecycle, runtime)
	registerMessaging(lifecycle, runtime)
	registerIdentityProvider(lifecycle, runtime)
	registerJobs(lifecycle, runtime)
	registerEntrances(lifecycle, runtime)
	registerHealthChecks(runtime)
}

func registerDatabase(lifecycle fx.Lifecycle, runtime runtime) {
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := runtime.Database.Ping(ctx, dependencyTimeout); err != nil {
				return err
			}

			budget := runtime.Config.ConnectionBudget()
			runtime.Logger.Info("database pool ready",
				slog.Int("poolSize", budget.PoolSize),
				slog.Int("httpWrites", budget.HTTPWrites),
				slog.Int("consumer", budget.Consumer),
				slog.Int("jobs", budget.Jobs),
				slog.Int("reserved", budget.Reserved),
			)
			return nil
		},
		OnStop: func(context.Context) error {
			runtime.Database.Close()
			runtime.Logger.Info("database pool closed")
			return nil
		},
	})
}

func registerMessaging(lifecycle fx.Lifecycle, runtime runtime) {
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			verifyCtx, cancel := context.WithTimeout(ctx, dependencyTimeout)
			defer cancel()
			return sqs.VerifyQueues(verifyCtx, runtime.SQS, runtime.Queues)
		},
	})
}

func registerIdentityProvider(lifecycle fx.Lifecycle, runtime runtime) {
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return runtime.Verifier.Connect(ctx)
		},
	})
}

func registerJobs(lifecycle fx.Lifecycle, runtime runtime) {
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			runtime.Components.Scheduler.Start(ctx)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			stopCtx, cancel := context.WithTimeout(ctx, runtime.Config.App.ShutdownTimeout)
			defer cancel()
			return runtime.Components.Scheduler.Stop(stopCtx)
		},
	})
}

func registerEntrances(lifecycle fx.Lifecycle, runtime runtime) {
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := runtime.Server.Start(ctx); err != nil {
				return err
			}
			if runtime.Config.Consumer.Enabled {
				runtime.Components.Consumer.Start(ctx)
			}
			return nil
		},
		OnStop: func(ctx context.Context) error {
			runtime.Components.Consumer.StopReceiving()
			consumerStopped := make(chan error, 1)
			go func() { consumerStopped <- runtime.Components.Consumer.Stop(ctx) }()
			httpErr := runtime.Server.Stop(ctx)
			return errors.Join(httpErr, <-consumerStopped)
		},
	})
}

func registerHealthChecks(runtime runtime) {
	runtime.Health.Register("postgres", func(ctx context.Context) error {
		return runtime.Database.Ping(ctx, dependencyTimeout)
	})
	runtime.Health.Register("sqs", func(ctx context.Context) error {
		_, err := sqs.QueueARN(ctx, runtime.SQS, runtime.Queues.Wager)
		return err
	})
}
