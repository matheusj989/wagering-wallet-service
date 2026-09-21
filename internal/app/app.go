package app

import (
	"context"
	"log/slog"
	nethttp "net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/auth"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/config"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/failpoint"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/http"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/jobs"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/logging"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/observability"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/postgres"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs/listener"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/sqs/publisher"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/system"
)

const lifecycleTimeout = 30 * time.Second

func Options() fx.Option {
	return fx.Options(
		fx.StartTimeout(lifecycleTimeout),
		fx.StopTimeout(lifecycleTimeout),
		configModule,
		loggingModule,
		observabilityModule,
		postgresModule,
		sqsModule,
		authModule,
		usecaseModule,
		jobsModule,
		httpModule,
		fx.Invoke(registerLifecycle),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logging.Component(logger, "fx")}
		}),
	)
}

var configModule = fx.Module("config",
	fx.Provide(config.Load),
)

var loggingModule = fx.Module("logging",
	fx.Provide(func(configuration *config.Config) *slog.Logger {
		return logging.New(configuration.App.LogLevel, configuration.App.InstanceID, configuration.App.Environment)
	}),
	fx.Provide(
		func() port.Clock { return system.NewClock() },
		func() port.IDGenerator { return system.NewIDGenerator() },
		func() port.Failpoint { return failpoint.New() },
	),
)

var observabilityModule = fx.Module("observability",
	fx.Provide(
		prometheus.NewRegistry,
		func(registry *prometheus.Registry) *observability.Metrics { return observability.NewMetrics(registry) },
		observability.NewHealth,
		func(metrics *observability.Metrics) port.WageringMetrics { return metrics },
		func(metrics *observability.Metrics) port.ReconciliationMetrics { return metrics },
		func(metrics *observability.Metrics) port.ReferenceMetrics { return metrics },
		func(metrics *observability.Metrics) port.OutboxMetrics { return metrics },
		func(metrics *observability.Metrics) port.ConsumerMetrics { return metrics },
	),
)

var postgresModule = fx.Module("postgres",
	fx.Provide(
		func(configuration *config.Config) (*postgres.Client, error) {
			return postgres.NewClient(context.Background(), postgres.ClientSettings{
				URL:      configuration.Database.URL,
				MaxConns: configuration.Database.MaxConns,
				MinConns: configuration.Database.MinConns,
			})
		},
		func(client *postgres.Client, configuration *config.Config) repositories.UnitOfWork {
			return postgres.NewUnitOfWork(client, postgres.Settings{
				LockTimeout:      configuration.Database.LockTimeout,
				StatementTimeout: configuration.Database.StatementTimeout,
				RetryAttempts:    configuration.Database.RetryAttempts,
			})
		},
	),
)

var sqsModule = fx.Module("sqs",
	fx.Provide(
		func(configuration *config.Config) (*awssqs.Client, error) {
			return sqs.NewClient(context.Background(), sqs.Settings{
				Region:      configuration.SQS.Region,
				Endpoint:    configuration.SQS.Endpoint,
				AccessKeyID: configuration.SQS.AccessKeyID,
				SecretKey:   configuration.SQS.SecretKey,
			})
		},
		func(configuration *config.Config) sqs.Queues {
			return sqs.Queues{
				Wager:  configuration.SQS.WagerQueueURL,
				DLQ:    configuration.SQS.WagerDLQURL,
				Events: configuration.SQS.EventsQueue,
			}
		},
		func(client *awssqs.Client, queues sqs.Queues) port.WalletEventNotifier {
			return publisher.NewWalletEventNotifier(client, queues.Events)
		},
	),
)

var authModule = fx.Module("auth",
	fx.Provide(func(configuration *config.Config) *auth.Verifier {
		return auth.NewVerifier(auth.Settings{
			Issuer:       configuration.OIDC.Issuer,
			DiscoveryURL: configuration.OIDC.DiscoveryURL,
			JWKSURL:      configuration.OIDC.JWKSURL,
			Audience:     configuration.OIDC.Audience,
		})
	}),
)

var usecaseModule = fx.Module("usecase",
	fx.Provide(
		func(unitOfWork repositories.UnitOfWork, clock port.Clock, ids port.IDGenerator, logger *slog.Logger) usecase.OpenWallet {
			return usecase.NewOpenWallet(unitOfWork, clock, ids, logging.Component(logger, "wallets"))
		},
		func(
			unitOfWork repositories.UnitOfWork, clock port.Clock, ids port.IDGenerator,
			metrics port.WageringMetrics, logger *slog.Logger, configuration *config.Config,
		) usecase.ProcessWagerTransaction {
			return usecase.NewProcessWagerTransaction(unitOfWork, clock, ids, metrics,
				logging.Component(logger, "wagering"), usecase.ProcessSettings{
					ReferenceBackoff: configuration.Reference.BackoffBase,
					ReferenceTTL:     configuration.Reference.TTL,
				})
		},
		func(unitOfWork repositories.UnitOfWork, logger *slog.Logger) usecase.FindWallet {
			return usecase.NewFindWallet(unitOfWork, logging.Component(logger, "wallets"))
		},
		func(unitOfWork repositories.UnitOfWork, logger *slog.Logger) usecase.ListWalletLedger {
			return usecase.NewListWalletLedger(unitOfWork, logging.Component(logger, "ledger"))
		},
		func(unitOfWork repositories.UnitOfWork, metrics port.ReconciliationMetrics, logger *slog.Logger) usecase.ReconcileWallet {
			return usecase.NewReconcileWallet(unitOfWork, metrics, logging.Component(logger, "reconciliation"))
		},
		func(unitOfWork repositories.UnitOfWork, logger *slog.Logger) usecase.FindWagerTransaction {
			return usecase.NewFindWagerTransaction(unitOfWork, logging.Component(logger, "wagering"))
		},
		func(
			unitOfWork repositories.UnitOfWork, clock port.Clock, ids port.IDGenerator,
			metrics port.ReferenceMetrics, logger *slog.Logger, configuration *config.Config,
		) usecase.RetryPendingReference {
			return usecase.NewRetryPendingReference(unitOfWork, clock, ids, metrics,
				logging.Component(logger, "reference-worker"), usecase.ReferenceSettings{
					BackoffBase: configuration.Reference.BackoffBase,
					BackoffMax:  configuration.Reference.BackoffMax,
					BatchSize:   configuration.Reference.BatchSize,
				})
		},
		func(
			unitOfWork repositories.UnitOfWork, notifier port.WalletEventNotifier, clock port.Clock,
			metrics port.OutboxMetrics, failpoints port.Failpoint, logger *slog.Logger, configuration *config.Config,
		) usecase.PublishOutbox {
			return usecase.NewPublishOutbox(unitOfWork, notifier, clock, metrics, failpoints,
				logging.Component(logger, "outbox-publisher"), usecase.OutboxSettings{
					Owner:       configuration.App.InstanceID,
					Lease:       configuration.Outbox.Lease,
					BatchSize:   configuration.Outbox.BatchSize,
					BackoffBase: configuration.Outbox.BackoffBase,
					BackoffMax:  configuration.Outbox.BackoffMax,
				})
		},
	),
)

type Components struct {
	Scheduler jobs.Scheduler
	Consumer  *sqs.Consumer
}

var jobsModule = fx.Module("jobs",
	fx.Provide(func(
		publish usecase.PublishOutbox,
		retry usecase.RetryPendingReference,
		process usecase.ProcessWagerTransaction,
		client *awssqs.Client,
		queues sqs.Queues,
		metrics port.ConsumerMetrics,
		logger *slog.Logger,
		configuration *config.Config,
	) (Components, error) {
		consumer, err := sqs.NewConsumer(client, queues.DLQ, metrics, logger, sqs.ConsumerSettings{
			Workers:         configuration.Consumer.Workers,
			WaitTime:        configuration.Consumer.WaitTime,
			Visibility:      configuration.Consumer.Visibility,
			MessageDeadline: configuration.Consumer.MessageDeadline,
			BackoffBase:     configuration.Consumer.BackoffBase,
			BackoffMax:      configuration.Consumer.BackoffMax,
			MaxReceiveCount: configuration.Consumer.MaxReceiveCount,
			ShutdownTimeout: configuration.App.ShutdownTimeout,
		}, listener.NewWagerTransactionListener(queues.Wager, process, metrics, logger))
		if err != nil {
			return Components{}, err
		}

		return Components{
			Scheduler: jobs.NewTickerScheduler(logger, scheduled(publish, retry, configuration)...),
			Consumer:  consumer,
		}, nil
	}),
)

func scheduled(publish usecase.PublishOutbox, retry usecase.RetryPendingReference, configuration *config.Config) []jobs.Job {
	scheduled := make([]jobs.Job, 0, 2)
	if configuration.Outbox.Enabled {
		scheduled = append(scheduled, jobs.NewPublishOutboxJob(publish, configuration.Outbox.PollInterval))
	}
	if configuration.Reference.Enabled {
		scheduled = append(scheduled, jobs.NewRetryPendingReferenceJob(retry, configuration.Reference.PollInterval))
	}
	return scheduled
}

var httpModule = fx.Module("http",
	fx.Provide(
		func(
			open usecase.OpenWallet, process usecase.ProcessWagerTransaction,
			findWallet usecase.FindWallet, listLedger usecase.ListWalletLedger,
			reconcileWallet usecase.ReconcileWallet, findTransaction usecase.FindWagerTransaction,
			verifier *auth.Verifier, health *observability.Health, metrics *observability.Metrics,
			logger *slog.Logger, configuration *config.Config,
		) *http.Handlers {
			return http.NewHandlers(open, process, findWallet, listLedger, reconcileWallet, findTransaction,
				verifier, health, metrics, logger, http.HandlersSettings{
					WriteConcurrency: configuration.HTTP.WriteConcurrency,
					WriteQueueWait:   http.Duration{Value: configuration.HTTP.WriteQueueWait},
					MaxBodyBytes:     configuration.HTTP.MaxBodyBytes,
				})
		},
		func(handlers *http.Handlers, registry *prometheus.Registry, metrics *observability.Metrics, logger *slog.Logger) nethttp.Handler {
			return http.NewRouter(handlers, registry, metrics, logger)
		},
		func(router nethttp.Handler, configuration *config.Config, logger *slog.Logger) *http.Server {
			return http.NewServer(router, http.ServerSettings{
				Addr:            configuration.HTTP.Addr,
				ShutdownTimeout: configuration.App.ShutdownTimeout,
			}, logger)
		},
	),
)
