package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/auth"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/logging"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/observability"
)

const correlationHeader = "X-Correlation-Id"

type contextKey int

const (
	correlationKey contextKey = iota
	principalKey
)

func correlationFrom(ctx context.Context) string {
	if value, ok := ctx.Value(correlationKey).(string); ok {
		return value
	}
	return ""
}

func principalFrom(ctx context.Context) (auth.Principal, bool) {
	value, ok := ctx.Value(principalKey).(auth.Principal)
	return value, ok
}

func correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identifier, valid := logging.CorrelationID(r.Header.Get(correlationHeader))
		if !valid {
			identifier = uuid.Must(uuid.NewV7()).String()
		}
		w.Header().Set(correlationHeader, identifier)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey, identifier)))
	})
}

func observed(metrics *observability.Metrics, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startedAt := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			defer func() {
				if recovered := recover(); recovered != nil {
					logger.ErrorContext(r.Context(), "request handler panicked",
						slog.String(logging.FieldCorrelationID, correlationFrom(r.Context())),
						slog.String("path", r.URL.Path),
						slog.Any("cause", recovered))
					if !recorder.written {
						recorder.WriteHeader(http.StatusInternalServerError)
					}
				}
				route := chi.RouteContext(r.Context()).RoutePattern()
				if route == "" {
					route = "unmatched"
				}
				metrics.RequestObserved(route, r.Method, http.StatusText(recorder.status), time.Since(startedAt))
			}()

			next.ServeHTTP(recorder, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.written {
		return
	}
	r.status = status
	r.written = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(payload []byte) (int, error) {
	if !r.written {
		r.written = true
	}
	return r.ResponseWriter.Write(payload)
}

func (h *Handlers) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, present := auth.BearerToken(r.Header.Get("Authorization"))
		if !present {
			h.writeProblem(w, r, http.StatusUnauthorized, CodeUnauthenticated, "a bearer token is required")
			return
		}

		principal, err := h.verifier.Verify(r.Context(), token)
		if err != nil {
			h.logger.InfoContext(r.Context(), "credential refused",
				slog.String(logging.FieldCorrelationID, correlationFrom(r.Context())),
				slog.String("path", r.URL.Path))
			h.writeProblem(w, r, http.StatusUnauthorized, CodeUnauthenticated, "the credential is not valid")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, principal)))
	})
}

func (h *Handlers) internalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFrom(r.Context())
		if !ok || !principal.Internal() {
			h.writeProblem(w, r, http.StatusForbidden, CodeForbidden, "this operation is restricted to the internal service")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handlers) providerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFrom(r.Context())
		if !ok || !principal.Provider() {
			h.writeProblem(w, r, http.StatusForbidden, CodeForbidden, "this operation is restricted to gaming providers")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handlers) providerOrInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFrom(r.Context())
		if !ok || (!principal.Provider() && !principal.Internal()) {
			h.writeProblem(w, r, http.StatusForbidden, CodeForbidden, "this operation requires a wagering role")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handlers) limited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(h.limiter.wait)
		defer timer.Stop()

		select {
		case h.limiter.slots <- struct{}{}:
			h.metrics.RequestStarted()
			defer func() {
				<-h.limiter.slots
				h.metrics.RequestFinished()
			}()
			next.ServeHTTP(w, r)
		case <-timer.C:
			h.metrics.RequestShed()
			h.writeProblem(w, r, http.StatusServiceUnavailable, CodeUnavailable,
				"the service is above its write capacity, please retry")
		case <-r.Context().Done():
			h.writeProblem(w, r, http.StatusServiceUnavailable, CodeUnavailable, "the request was cancelled")
		}
	})
}

func (h *Handlers) bodyLimited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

type limiter struct {
	slots chan struct{}
	wait  time.Duration
}

func newLimiter(concurrency int, wait time.Duration) limiter {
	return limiter{slots: make(chan struct{}, concurrency), wait: wait}
}
