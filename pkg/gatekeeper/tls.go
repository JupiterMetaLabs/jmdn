package gatekeeper

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"gossipnode/config/settings"
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// TLSLoader handles loading certificates based on configuration
type TLSLoader struct {
	config *settings.SecurityConfig
	logger *ion.Ion
}

// NewTLSLoader creates a new loader with the given config
func NewTLSLoader(cfg *settings.SecurityConfig, logger *ion.Ion) *TLSLoader {
	if logger == nil {
		// Fallback to package-level gatekeeper logger to prevent nil panics
		logger = gatekeeperLogger(log.Security)
	}
	return &TLSLoader{
		config: cfg,
		logger: logger,
	}
}

// LoadServerTLS loads the TLS configuration for a specific service (Server Side)
func (l *TLSLoader) LoadServerTLS(serviceName string) (*tls.Config, error) {
	if l.config != nil && !l.config.Enabled {
		return nil, nil // Security globally disabled, skip TLS
	}

	policy, ok := l.config.Services[serviceName]
	if !ok {
		return nil, fmt.Errorf("unknown service: %s", serviceName)
	}

	if !policy.TLS {
		return nil, nil // TLS disabled for this service
	}

	// 1. Determine Certificate Paths
	// Priority: Policy Override -> Global Cert Dir -> Defaults
	certFile := policy.CertFile
	keyFile := policy.KeyFile
	caFile := policy.CAFile

	if certFile == "" {
		certFile = fmt.Sprintf("%s/%s.crt", l.config.CertDir, serviceName)
	}
	if keyFile == "" {
		keyFile = fmt.Sprintf("%s/%s.key", l.config.CertDir, serviceName)
	}
	if caFile == "" {
		caFile = fmt.Sprintf("%s/ca.crt", l.config.CertDir)
	}

	// 2. Load KeyPair
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load key pair for %s: %w", serviceName, err)
	}

	// 3. Create Base Config (TLS 1.3 Strict)
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	// 4. Client Authentication (mTLS)
	if policy.AuthType == settings.AuthTypeMTLS || policy.AuthType == settings.AuthTypeHybrid {
		// Load CA for verifying clients
		caPem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA file %s: %w", caFile, err)
		}
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caPem) {
			return nil, fmt.Errorf("failed to append CA certs")
		}

		tlsConfig.ClientCAs = caPool

		if policy.AuthType == settings.AuthTypeMTLS {
			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		} else {
			tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven // Hybrid
		}
	}

	l.logger.Info(context.Background(), "TLS Configuration Loaded",
		ion.String("service", serviceName),
		ion.String("auth_type", string(policy.AuthType)),
	)
	return tlsConfig, nil
}

// LoadClientTLS loads the TLS configuration for a client connecting to a verified service
// clientIdentity is optional; if provided, it loads a client certificate for mTLS (e.g. "cli_client")
func (l *TLSLoader) LoadClientTLS(targetServiceName string, clientIdentity string) (*tls.Config, error) {
	// For clients, we mainly need to trust the CA that signed the server's cert
	caFile := fmt.Sprintf("%s/ca.crt", l.config.CertDir)

	// Check if specific service policy overrides CA
	if policy, ok := l.config.Services[targetServiceName]; ok && policy.CAFile != "" {
		caFile = policy.CAFile
	}

	caPem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA file: %w", err)
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPem) {
		return nil, fmt.Errorf("failed to append CA certs")
	}

	tlsConfig := &tls.Config{
		RootCAs:    certPool,
		MinVersion: tls.VersionTLS13,
	}

	// 2. Load Client Certificate if identity provided (mTLS)
	if clientIdentity != "" {
		certFile := fmt.Sprintf("%s/%s.crt", l.config.CertDir, clientIdentity)
		keyFile := fmt.Sprintf("%s/%s.key", l.config.CertDir, clientIdentity)

		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load client identity %s: %w", clientIdentity, err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
}

// LoadClientCredentials is a high-level helper that enforces the standard security security policy:
// 1. Check if TLS is enabled for the target service in SecurityConfig.
// 2. If Enabled: Load TLS config. If that fails -> RETURN ERROR (Fail Hard).
// 3. If Disabled: Return insecure.NewCredentials().
func (l *TLSLoader) LoadClientCredentials(targetServiceName string, clientIdentity string) (credentials.TransportCredentials, error) {
	if l.config != nil && !l.config.Enabled {
		return insecure.NewCredentials(), nil
	}

	policy, ok := l.config.Services[targetServiceName]
	if !ok {
		// If service is not defined in policy, strictly default to error or secure?
		// For now, let's error to force config/registration.
		return nil, fmt.Errorf("security policy not found for service: %s", targetServiceName)
	}

	if policy.TLS {
		tlsConfig, err := l.LoadClientTLS(targetServiceName, clientIdentity)
		if err != nil {
			return nil, fmt.Errorf("failed to load required TLS config for %s: %w", targetServiceName, err)
		}
		return credentials.NewTLS(tlsConfig), nil
	}

	// TLS is explicitly disabled in policy
	return insecure.NewCredentials(), nil
}
