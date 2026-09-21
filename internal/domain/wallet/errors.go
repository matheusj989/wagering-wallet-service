package wallet

import "errors"

var (
	ErrNotFound       = errors.New("wallet: not found")
	ErrAlreadyExists  = errors.New("wallet: player already has a wallet in this currency")
	ErrVersionChanged = errors.New("wallet: version changed since it was read")
)
