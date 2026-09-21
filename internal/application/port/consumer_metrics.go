package port

type ConsumerMetrics interface {
	MessageReceived()
	MessageRetried()
	MessageRedelivered()
	MessageDeadLettered(reason string)
}
