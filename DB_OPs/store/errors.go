package store

import "errors"

var (
	ErrNotFound       = errors.New("store: not found")
	ErrAccountExists  = errors.New("store: account already exists")
	ErrDuplicateNonce = errors.New("store: duplicate nonce")
)
