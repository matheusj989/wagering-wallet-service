package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	transactions        *prometheus.CounterVec
	replays             *prometheus.CounterVec
	conflicts           *prometheus.CounterVec
	processingDuration  *prometheus.HistogramVec
	concurrency         *prometheus.CounterVec
	reconciliation      prometheus.Counter
	referenceRetries    prometheus.Counter
	referenceFailures   prometheus.Counter
	referencePending    prometheus.Gauge
	outboxAttempts      *prometheus.CounterVec
	outboxPending       prometheus.Gauge
	outboxOldestPending prometheus.Gauge
	messagesReceived    prometheus.Counter
	messageRetries      prometheus.Counter
	redeliveries        prometheus.Counter
	deadLettered        *prometheus.CounterVec
	requestsInFlight    prometheus.Gauge
	requestsShed        prometheus.Counter
	requestDuration     *prometheus.HistogramVec
}

func NewMetrics(registry prometheus.Registerer) *Metrics {
	factory := promauto(registry)
	return &Metrics{
		transactions: factory.counterVec("wager_transactions_total",
			"Wager transactions by kind, final status and origin.", "kind", "status", "origin"),
		replays: factory.counterVec("wager_idempotent_replays_total",
			"Operations answered with a previously persisted result.", "origin"),
		conflicts: factory.counterVec("wager_idempotency_conflicts_total",
			"Operations refused because the key or the external id was already used.", "origin", "reason"),
		processingDuration: factory.histogramVec("wager_processing_duration_seconds",
			"Time spent settling a wager transaction.", []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			"origin", "kind"),
		concurrency: factory.counterVec("wallet_concurrency_conflicts_total",
			"Attempts that lost a race for a wallet.", "reason"),
		reconciliation: factory.counter("reconciliation_divergences_total",
			"Reconciliations where the stored balance differed from the ledger."),
		referenceRetries: factory.counter("reference_retries_total",
			"Pending reference attempts that were rescheduled."),
		referenceFailures: factory.counter("reference_failed_total",
			"Pending references that ended in a permanent failure."),
		referencePending: factory.gauge("reference_pending",
			"Operations currently waiting for their reference."),
		outboxAttempts: factory.counterVec("outbox_publish_attempts_total",
			"Outbox publication attempts by result.", "result"),
		outboxPending: factory.gauge("outbox_pending",
			"Events written to the outbox and not published yet."),
		outboxOldestPending: factory.gauge("outbox_oldest_pending_age_seconds",
			"Age of the oldest unpublished outbox event."),
		messagesReceived: factory.counter("sqs_messages_received_total",
			"Messages received from the wagering queue."),
		messageRetries: factory.counter("sqs_retries_total",
			"Messages returned to the queue after a transient failure."),
		redeliveries: factory.counter("sqs_redeliveries_total",
			"Messages redelivered after they had already been handled."),
		deadLettered: factory.counterVec("sqs_dlq_total",
			"Messages sent to the dead letter queue by reason.", "reason"),
		requestsInFlight: factory.gauge("http_requests_in_flight",
			"Write requests holding a slot in the limiter."),
		requestsShed: factory.counter("http_requests_shed_total",
			"Write requests refused because the limiter was full."),
		requestDuration: factory.histogramVec("http_request_duration_seconds",
			"Time spent answering an HTTP request.", []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			"route", "method", "status"),
	}
}

func (m *Metrics) TransactionSettled(kind string, status string, origin string) {
	m.transactions.WithLabelValues(kind, status, origin).Inc()
}

func (m *Metrics) IdempotentReplay(origin string) {
	m.replays.WithLabelValues(origin).Inc()
}

func (m *Metrics) IdempotencyConflict(origin string, reason string) {
	m.conflicts.WithLabelValues(origin, reason).Inc()
}

func (m *Metrics) ProcessingObserved(origin string, kind string, elapsed time.Duration) {
	m.processingDuration.WithLabelValues(origin, kind).Observe(elapsed.Seconds())
}

func (m *Metrics) ConcurrencyConflict(reason string) {
	m.concurrency.WithLabelValues(reason).Inc()
}

func (m *Metrics) ReconciliationDivergence() {
	m.reconciliation.Inc()
}

func (m *Metrics) ReferenceRetried() {
	m.referenceRetries.Inc()
}

func (m *Metrics) ReferenceFailed() {
	m.referenceFailures.Inc()
}

func (m *Metrics) ReferencePending(count int64) {
	m.referencePending.Set(float64(count))
}

func (m *Metrics) OutboxPublishAttempt(result string) {
	m.outboxAttempts.WithLabelValues(result).Inc()
}

func (m *Metrics) OutboxPending(count int64, oldestAge time.Duration) {
	m.outboxPending.Set(float64(count))
	m.outboxOldestPending.Set(oldestAge.Seconds())
}

func (m *Metrics) MessageReceived() {
	m.messagesReceived.Inc()
}

func (m *Metrics) MessageRetried() {
	m.messageRetries.Inc()
}

func (m *Metrics) MessageRedelivered() {
	m.redeliveries.Inc()
}

func (m *Metrics) MessageDeadLettered(reason string) {
	m.deadLettered.WithLabelValues(reason).Inc()
}

func (m *Metrics) RequestStarted() {
	m.requestsInFlight.Inc()
}

func (m *Metrics) RequestFinished() {
	m.requestsInFlight.Dec()
}

func (m *Metrics) RequestShed() {
	m.requestsShed.Inc()
}

func (m *Metrics) RequestObserved(route string, method string, status string, elapsed time.Duration) {
	m.requestDuration.WithLabelValues(route, method, status).Observe(elapsed.Seconds())
}

type factory struct {
	registry prometheus.Registerer
}

func promauto(registry prometheus.Registerer) factory {
	return factory{registry: registry}
}

func (f factory) counter(name string, help string) prometheus.Counter {
	metric := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	f.registry.MustRegister(metric)
	return metric
}

func (f factory) counterVec(name string, help string, labels ...string) *prometheus.CounterVec {
	metric := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	f.registry.MustRegister(metric)
	return metric
}

func (f factory) gauge(name string, help string) prometheus.Gauge {
	metric := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	f.registry.MustRegister(metric)
	return metric
}

func (f factory) histogramVec(name string, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	metric := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets}, labels)
	f.registry.MustRegister(metric)
	return metric
}
