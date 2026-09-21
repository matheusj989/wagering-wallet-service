package port

import "time"

type Clock interface {
	Now() time.Time
}
