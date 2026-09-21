package listener

import "context"

// Message is what a listener receives: the body and the little the broker says
// about this delivery. There is nothing here about acknowledging the message,
// because that decision belongs to the consumer.
type Message struct {
	BrokerID     string
	Body         []byte
	GroupID      string
	ReceiveCount int
	Attributes   map[string]string
}

func (m Message) Attribute(name string) string {
	return m.Attributes[name]
}

// Listener handles the messages of one queue. Watching says which queue those
// messages come from, so the consumer routes by queue and never has to know what
// a payload means.
type Listener interface {
	Name() string
	Watching() string
	Handle(ctx context.Context, message Message) error
}
