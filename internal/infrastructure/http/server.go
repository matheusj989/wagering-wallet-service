package http

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/logging"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/observability"
)

type Duration struct {
	Value time.Duration
}

type ServerSettings struct {
	Addr            string
	ShutdownTimeout time.Duration
}

type Server struct {
	server   *http.Server
	listener net.Listener
	settings ServerSettings
	logger   *slog.Logger
}

func NewRouter(handlers *Handlers, registry *prometheus.Registry, metrics *observability.Metrics, logger *slog.Logger) http.Handler {
	router := chi.NewRouter()
	router.Use(correlation, observed(metrics, logger))

	router.Get("/health/live", handlers.live)
	router.Get("/health/ready", handlers.ready)
	router.Method(http.MethodGet, "/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	router.Group(func(secured chi.Router) {
		secured.Use(handlers.authenticated, handlers.bodyLimited)

		secured.Group(func(internal chi.Router) {
			internal.Use(handlers.internalOnly)
			internal.With(handlers.limited).Post("/wallets", handlers.openWallet)
			internal.Get("/wallets/{walletId}", handlers.getWallet)
			internal.Get("/wallets/{walletId}/ledger", handlers.getLedger)
			internal.With(handlers.limited).Post("/wallets/{walletId}/reconciliation", handlers.reconcile)
		})

		secured.With(handlers.providerOnly, handlers.limited).
			Post("/wagering/transactions", handlers.submitTransaction)

		secured.Group(func(readers chi.Router) {
			readers.Use(handlers.providerOrInternal)
			readers.Get("/wagering/transactions/{transactionId}", handlers.getTransaction)
			readers.Get("/providers/{providerId}/wagering/transactions/{externalTransactionId}", handlers.getProviderTransaction)
		})
	})

	return router
}

func NewServer(router http.Handler, settings ServerSettings, logger *slog.Logger) *Server {
	return &Server{
		server: &http.Server{
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		settings: settings,
		logger:   logging.Component(logger, "http-server"),
	}
}

func (s *Server) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.settings.Addr)
	if err != nil {
		return err
	}
	s.listener = listener

	s.logger.Info("http server listening", slog.String("addr", listener.Addr().String()))

	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.logger.Error("http server stopped unexpectedly", slog.String("cause", err.Error()))
		}
	}()
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, s.settings.ShutdownTimeout)
	defer cancel()

	if err := s.server.Shutdown(shutdownCtx); err != nil {
		s.logger.Warn("http server did not stop gracefully", slog.String("cause", err.Error()))
		return s.server.Close()
	}
	s.logger.Info("http server stopped")
	return nil
}

func (s *Server) Addr() string {
	if s.listener == nil {
		return s.settings.Addr
	}
	return s.listener.Addr().String()
}
