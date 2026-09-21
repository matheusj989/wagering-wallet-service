package port

import "github.com/google/uuid"

type IDGenerator interface {
	New() uuid.UUID
}
