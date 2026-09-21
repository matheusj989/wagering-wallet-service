package repositories

import "errors"

var (
	ErrTransient            = errors.New("unit of work: transient failure")
	ErrLockTimeout          = errors.New("unit of work: lock timeout while waiting for a wallet")
	ErrCommitOutcomeUnknown = errors.New("unit of work: commit outcome is unknown")
	ErrRecoveredPanic       = errors.New("unit of work: operation panicked and was rolled back")
)
