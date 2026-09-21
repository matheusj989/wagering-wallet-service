package listener

import (
	"errors"
	"fmt"
)

const (
	ReasonInvalidPayload    = "INVALID_PAYLOAD"
	ReasonOpeningNotAllowed = "OPENING_NOT_ALLOWED"
	ReasonWalletNotFound    = "WALLET_NOT_FOUND"
	ReasonInboxMismatch     = "INBOX_HASH_MISMATCH"
)

// ErrUnknownOutcome says the work may or may not have happened. The message is
// left exactly as it is, so the next delivery reads the durable state and decides
// instead of guessing now.
var ErrUnknownOutcome = errors.New("listener: the outcome of this message is unknown")

// Rejection is a failure another delivery will not fix. The consumer moves the
// message to the dead letter queue carrying Reason, instead of retrying until the
// redrive policy gives up on it.
type Rejection struct {
	Reason string
	Cause  error
}

func Reject(reason string, cause error) *Rejection {
	return &Rejection{Reason: reason, Cause: cause}
}

func (e *Rejection) Error() string {
	return fmt.Sprintf("listener: message rejected as %s: %v", e.Reason, e.Cause)
}

func (e *Rejection) Unwrap() error {
	return e.Cause
}

func Unknown(cause error) error {
	return fmt.Errorf("%w: %w", ErrUnknownOutcome, cause)
}
