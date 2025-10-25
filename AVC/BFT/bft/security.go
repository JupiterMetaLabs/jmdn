// =============================================================================
// FILE: pkg/bft/security.go
// =============================================================================
package bft

import (
	"crypto"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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

// LoadTLSCredentials loads certs for mTLS (CA, cert, key).
func LoadTLSCredentials(caCertPath, certPath, keyPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load cert pair: %w", err)
	}
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert: %w", err)
	}
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to append CA cert")
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
		ClientCAs:    certPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	return tlsCfg, nil
}

// DialOptionsForMTLS returns a grpc.DialOption to use mTLS.
func DialOptionsForMTLS(tlsCfg *tls.Config) grpc.DialOption {
	creds := credentials.NewTLS(tlsCfg)
	return grpc.WithTransportCredentials(creds)
}
