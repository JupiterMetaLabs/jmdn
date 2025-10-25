package types

import "errors"

var (
	ErrInvalidNodeID     = errors.New("invalid node ID")
	ErrInvalidPublicKey  = errors.New("invalid public key")
	ErrConnectionFailed  = errors.New("connection failed")
	ErrAlgorithmNotFound = errors.New("algorithm not found")
	ErrNoEligibleNodes   = errors.New("no eligible nodes available")
	ErrInvalidRequest    = errors.New("invalid request")
)
