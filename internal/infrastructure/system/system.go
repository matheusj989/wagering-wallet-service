package system

import (
	"time"

	"github.com/google/uuid"
)

type Clock struct{}

func NewClock() Clock {
	return Clock{}
}

func (Clock) Now() time.Time {
	return time.Now().UTC()
}

type IDGenerator struct{}

func NewIDGenerator() IDGenerator {
	return IDGenerator{}
}

func (IDGenerator) New() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}
