package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	App       App
	HTTP      HTTP
	Database  Database
	SQS       SQS
	Consumer  Consumer
	Outbox    Outbox
	Reference Reference
	OIDC      OIDC
}

type App struct {
	Environment     string
	InstanceID      string
	LogLevel        string
	ShutdownTimeout time.Duration
}

type HTTP struct {
	Addr             string
	WriteConcurrency int
	WriteQueueWait   time.Duration
	MaxBodyBytes     int64
}

type Database struct {
	URL              string
	MaxConns         int32
	MinConns         int32
	Reserved         int
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	RetryAttempts    int
}

type SQS struct {
	Region        string
	Endpoint      string
	AccessKeyID   string
	SecretKey     string
	WagerQueueURL string
	WagerDLQURL   string
	EventsQueue   string
}

type Consumer struct {
	Enabled         bool
	Workers         int
	WaitTime        time.Duration
	Visibility      time.Duration
	MessageDeadline time.Duration
	BackoffBase     time.Duration
	BackoffMax      time.Duration
	MaxReceiveCount int
}

type Outbox struct {
	Enabled      bool
	PollInterval time.Duration
	BatchSize    int
	Lease        time.Duration
	BackoffBase  time.Duration
	BackoffMax   time.Duration
}

type Reference struct {
	Enabled      bool
	PollInterval time.Duration
	BatchSize    int
	BackoffBase  time.Duration
	BackoffMax   time.Duration
	TTL          time.Duration
}

type OIDC struct {
	Issuer       string
	DiscoveryURL string
	JWKSURL      string
	Audience     string
}

func Load() (*Config, error) {
	r := &reader{}

	cfg := &Config{
		App: App{
			Environment:     r.text("APP_ENV", "local"),
			InstanceID:      r.text("INSTANCE_ID", defaultInstanceID()),
			LogLevel:        r.oneOf("LOG_LEVEL", "info", "debug", "info", "warn", "error"),
			ShutdownTimeout: r.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
		},
		HTTP: HTTP{
			Addr:             r.text("HTTP_ADDR", ":8080"),
			WriteConcurrency: r.number("HTTP_WRITE_CONCURRENCY", 12),
			WriteQueueWait:   r.duration("HTTP_WRITE_QUEUE_TIMEOUT", 2*time.Second),
			MaxBodyBytes:     int64(r.number("HTTP_MAX_BODY_BYTES", 65536)),
		},
		Database: Database{
			URL:              r.dsn("DATABASE_URL", "postgres://wallet_app:wallet_app@localhost:5432/wallet?sslmode=disable"),
			MaxConns:         int32(r.number("DB_POOL_MAX_CONNS", 20)),
			MinConns:         int32(r.count("DB_POOL_MIN_CONNS", 2)),
			Reserved:         r.count("DB_POOL_RESERVED", 2),
			LockTimeout:      r.duration("DB_LOCK_TIMEOUT", 3*time.Second),
			StatementTimeout: r.duration("DB_STATEMENT_TIMEOUT", 5*time.Second),
			RetryAttempts:    r.number("DB_RETRY_ATTEMPTS", 3),
		},
		SQS: SQS{
			Region:        r.text("AWS_REGION", "us-east-1"),
			Endpoint:      r.link("SQS_ENDPOINT", "http://localhost:4566"),
			AccessKeyID:   r.text("AWS_ACCESS_KEY_ID", "test"),
			SecretKey:     r.text("AWS_SECRET_ACCESS_KEY", "test"),
			WagerQueueURL: r.link("SQS_WAGER_QUEUE_URL", ""),
			WagerDLQURL:   r.link("SQS_WAGER_DLQ_URL", ""),
			EventsQueue:   r.link("SQS_EVENTS_QUEUE_URL", ""),
		},
		Consumer: Consumer{
			Enabled:         r.flag("SQS_CONSUMER_ENABLED", true),
			Workers:         r.number("SQS_CONSUMER_WORKERS", 4),
			WaitTime:        r.duration("SQS_WAIT_TIME", 20*time.Second),
			Visibility:      r.duration("SQS_VISIBILITY_TIMEOUT", 30*time.Second),
			MessageDeadline: r.duration("SQS_MESSAGE_DEADLINE", 10*time.Second),
			BackoffBase:     r.duration("SQS_RETRY_BACKOFF_BASE", 5*time.Second),
			BackoffMax:      r.duration("SQS_RETRY_BACKOFF_MAX", 5*time.Minute),
			MaxReceiveCount: r.number("SQS_MAX_RECEIVE_COUNT", 5),
		},
		Outbox: Outbox{
			Enabled:      r.flag("OUTBOX_PUBLISHER_ENABLED", true),
			PollInterval: r.duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond),
			BatchSize:    r.number("OUTBOX_BATCH_SIZE", 50),
			Lease:        r.duration("OUTBOX_LEASE", 30*time.Second),
			BackoffBase:  r.duration("OUTBOX_BACKOFF_BASE", time.Second),
			BackoffMax:   r.duration("OUTBOX_BACKOFF_MAX", 5*time.Minute),
		},
		Reference: Reference{
			Enabled:      r.flag("REFERENCE_WORKER_ENABLED", true),
			PollInterval: r.duration("REFERENCE_POLL_INTERVAL", time.Second),
			BatchSize:    r.number("REFERENCE_BATCH_SIZE", 10),
			BackoffBase:  r.duration("REFERENCE_BACKOFF_BASE", time.Second),
			BackoffMax:   r.duration("REFERENCE_BACKOFF_MAX", 60*time.Second),
			TTL:          r.duration("REFERENCE_TTL", 15*time.Minute),
		},
		OIDC: OIDC{
			Issuer:       r.link("OIDC_ISSUER", "http://localhost:8180/realms/wallet"),
			DiscoveryURL: r.link("OIDC_DISCOVERY_URL", "http://localhost:8180/realms/wallet"),
			JWKSURL:      r.link("OIDC_JWKS_URL", "http://localhost:8180/realms/wallet/protocol/openid-connect/certs"),
			Audience:     r.text("OIDC_AUDIENCE", "wallet-api"),
		},
	}

	r.check(cfg)
	if err := errors.Join(r.problems...); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func (r *reader) check(cfg *Config) {
	if cfg.Database.MinConns > cfg.Database.MaxConns {
		r.reject("DB_POOL_MIN_CONNS", "must not exceed DB_POOL_MAX_CONNS")
	}
	if budget := cfg.ConnectionBudget(); !budget.Fits() {
		r.reject("DB_POOL_MAX_CONNS", fmt.Sprintf(
			"is smaller than what this instance can hold at once (%s). "+
				"Raise it, or lower HTTP_WRITE_CONCURRENCY, SQS_CONSUMER_WORKERS or DB_POOL_RESERVED", budget))
	}
	if cfg.Consumer.MessageDeadline >= cfg.Consumer.Visibility {
		r.reject("SQS_MESSAGE_DEADLINE", "must be shorter than SQS_VISIBILITY_TIMEOUT")
	}
	if cfg.Consumer.BackoffMax < cfg.Consumer.BackoffBase {
		r.reject("SQS_RETRY_BACKOFF_MAX", "must not be shorter than SQS_RETRY_BACKOFF_BASE")
	}
	if cfg.Outbox.BackoffMax < cfg.Outbox.BackoffBase {
		r.reject("OUTBOX_BACKOFF_MAX", "must not be shorter than OUTBOX_BACKOFF_BASE")
	}
	if cfg.Reference.BackoffMax < cfg.Reference.BackoffBase {
		r.reject("REFERENCE_BACKOFF_MAX", "must not be shorter than REFERENCE_BACKOFF_BASE")
	}
	if cfg.Reference.TTL <= cfg.Reference.BackoffBase {
		r.reject("REFERENCE_TTL", "must be longer than REFERENCE_BACKOFF_BASE")
	}
	if cfg.Database.StatementTimeout <= cfg.Database.LockTimeout {
		r.reject("DB_STATEMENT_TIMEOUT", "must be longer than DB_LOCK_TIMEOUT")
	}
}

type reader struct {
	problems []error
}

func (r *reader) reject(key, reason string) {
	r.problems = append(r.problems, fmt.Errorf("%s %s", key, reason))
}

// raw treats a variable set to nothing as a variable that was not set. Compose
// always defines the key when a service declares ${VAR:-}, so without this an
// empty line in .env would read as a deliberate empty value and reject the start.
func (r *reader) raw(key, fallback string) string {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

func (r *reader) text(key, fallback string) string {
	value := r.raw(key, fallback)
	if value == "" {
		r.reject(key, "is required")
	}
	return value
}

func (r *reader) oneOf(key, fallback string, allowed ...string) string {
	value := r.text(key, fallback)
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	r.reject(key, fmt.Sprintf("must be one of %s", strings.Join(allowed, ", ")))
	return fallback
}

func (r *reader) link(key, fallback string) string {
	value := r.text(key, fallback)
	if value == "" {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		r.reject(key, "must be an absolute URL")
	}
	return value
}

func (r *reader) dsn(key, fallback string) string {
	value := r.text(key, fallback)
	if value == "" {
		return value
	}
	if _, err := url.Parse(value); err != nil {
		r.reject(key, "must be a valid PostgreSQL connection string")
	}
	return value
}

func (r *reader) number(key string, fallback int) int {
	value := r.count(key, fallback)
	if value == 0 {
		r.reject(key, "must be greater than zero")
	}
	return value
}

func (r *reader) count(key string, fallback int) int {
	raw := r.raw(key, "")
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		r.reject(key, "must be an integer")
		return fallback
	}
	if value < 0 {
		r.reject(key, "must not be negative")
		return fallback
	}
	return value
}

func (r *reader) duration(key string, fallback time.Duration) time.Duration {
	raw := r.raw(key, "")
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		r.reject(key, "must be a duration such as 500ms, 5s or 2m")
		return fallback
	}
	if value <= 0 {
		r.reject(key, "must be greater than zero")
		return fallback
	}
	return value
}

func (r *reader) flag(key string, fallback bool) bool {
	raw := r.raw(key, "")
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		r.reject(key, "must be true or false")
		return fallback
	}
	return value
}

func defaultInstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}
