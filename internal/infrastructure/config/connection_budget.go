package config

import "fmt"

// ConnectionBudget is how one instance shares its database pool. Everything that
// can hold a connection at the same time declares its slice here, so the sum is
// checked when the process starts instead of being discovered later as waiting on
// the pool. The slices come from the same variables that size each component, so
// turning the consumer off or raising the write limiter moves the budget with it.
type ConnectionBudget struct {
	HTTPWrites int
	Consumer   int
	Jobs       int
	Reserved   int
	PoolSize   int
}

func (c *Config) ConnectionBudget() ConnectionBudget {
	consumer := 0
	if c.Consumer.Enabled {
		consumer = c.Consumer.Workers
	}

	jobs := 0
	if c.Outbox.Enabled {
		jobs++
	}
	if c.Reference.Enabled {
		jobs++
	}

	return ConnectionBudget{
		HTTPWrites: c.HTTP.WriteConcurrency,
		Consumer:   consumer,
		Jobs:       jobs,
		Reserved:   c.Database.Reserved,
		PoolSize:   int(c.Database.MaxConns),
	}
}

// Demand is the number of connections the instance can need at the same time.
func (b ConnectionBudget) Demand() int {
	return b.HTTPWrites + b.Consumer + b.Jobs + b.Reserved
}

func (b ConnectionBudget) Fits() bool {
	return b.Demand() <= b.PoolSize
}

func (b ConnectionBudget) String() string {
	return fmt.Sprintf("%d writes + %d consumer + %d jobs + %d reserved = %d of %d",
		b.HTTPWrites, b.Consumer, b.Jobs, b.Reserved, b.Demand(), b.PoolSize)
}
