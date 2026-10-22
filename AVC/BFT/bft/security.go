// =============================================================================
// FILE: pkg/bft/security.go
// =============================================================================
package bft

import (
	"crypto"
	"crypto/ed25519"
	"fmt"
)

// Signer is a minimal signing abstraction so you can plug KMS/HSM later.
type Signer interface {
	Sign(payload []byte) ([]byte, error)
	Public() crypto.PublicKey
}

// localSigner wraps an ed25519 private key (raw bytes) for now.
type localSigner struct {
	privateKey []byte
}

func NewLocalSigner(privateKey []byte) Signer {
	return &localSigner{privateKey: privateKey}
}

func (s *localSigner) Sign(payload []byte) ([]byte, error) {
	if len(s.privateKey) == 0 {
		return nil, fmt.Errorf("private key not set")
	}
	return ed25519.Sign(ed25519.PrivateKey(s.privateKey), payload), nil
}

func (s *localSigner) Public() crypto.PublicKey {
	// not used by engine; we rely on buddy public bytes registry
	return nil
}
