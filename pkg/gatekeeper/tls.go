package gatekeeper

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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

// LoadClientTLS builds a TLS client config for outbound connections to a named service.
//
// CA resolution order (first match wins):
//  1. Explicit policy.CAFile override   — private CA / pinned cert
//  2. <cert_dir>/ca.crt on disk         — private CA (internal mTLS)
//  3. OS / system CA pool               — public services (Let's Encrypt, etc.)
//
// Using the system pool (case 3) is correct and intentional for internet-facing endpoints
// their Let's Encrypt ISRG Root X1 cert is already trusted by the OS on every modern Linux host.
// No local cert provisioning needed.
//
// clientIdentity is optional. When provided, a client certificate is loaded from
// <cert_dir>/<clientIdentity>.crt/.key for mTLS.
func (l *TLSLoader) LoadClientTLS(targetServiceName string, clientIdentity string) (*tls.Config, error) {
	// 1. Determine CA file path — policy override takes priority.
	caFile := fmt.Sprintf("%s/ca.crt", l.config.CertDir)
	if policy, ok := l.config.Services[targetServiceName]; ok && policy.CAFile != "" {
		caFile = policy.CAFile
	}

	// 2. Build CA pool using resolution order documented above.
	var (
		certPool *x509.CertPool
		caSource string
	)
	switch caPem, err := os.ReadFile(caFile); {
	case err == nil:
		// Local CA file found — private CA or pinned cert scenario.
		certPool = x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(caPem) {
			return nil, fmt.Errorf("gatekeeper: failed to parse CA PEM from %s", caFile)
		}
		caSource = caFile

	case os.IsNotExist(err):
		// No local CA file — use OS trust store (covers Let's Encrypt, DigiCert, etc.).
		// x509.SystemCertPool returns nil on some platforms; tolerate that gracefully.
		certPool, _ = x509.SystemCertPool()
		if certPool == nil {
			certPool = x509.NewCertPool()
		}
		caSource = "system"

	default:
		// File exists but is unreadable (permissions / I/O error) — hard fail.
		return nil, fmt.Errorf("gatekeeper: failed to read CA file %s: %w", caFile, err)
	}

	l.logger.Info(context.Background(), "TLS client credentials resolved",
		ion.String("service", targetServiceName),
		ion.String("ca_source", caSource),
		ion.Bool("mtls_identity", clientIdentity != ""),
	)

	tlsConfig := &tls.Config{
		RootCAs:    certPool, // nil == system pool per crypto/tls docs; explicit pool set above
		MinVersion: tls.VersionTLS13,
	}

	// 3. Load client certificate for mTLS when an identity is provided.
	//
	// The client cert is OPTIONAL: if the cert file is absent, we proceed with
	// one-way TLS (server-auth only). This is the correct behaviour for public
	// endpoints like mre.jmdt.io whose nginx terminates TLS without requiring a
	// client certificate. A hard failure here would leave the routing client
	// singleton nil and silently block all transaction submissions.
	//
	// We only hard-fail if the cert file EXISTS but cannot be read/parsed, which
	// indicates a genuine provisioning error rather than a deliberate omission.
	if clientIdentity != "" {
		certFile := fmt.Sprintf("%s/%s.crt", l.config.CertDir, clientIdentity)
		keyFile := fmt.Sprintf("%s/%s.key", l.config.CertDir, clientIdentity)

		switch cert, err := tls.LoadX509KeyPair(certFile, keyFile); {
		case err == nil:
			// mTLS client cert found and loaded — present it to the server.
			tlsConfig.Certificates = []tls.Certificate{cert}

		case errors.Is(err, os.ErrNotExist):
			// Cert not provisioned on this node — proceed with one-way TLS.
			// This is expected for nodes connecting to internet-facing endpoints.
			l.logger.Warn(context.Background(), "mTLS client cert not found — proceeding with one-way TLS",
				ion.String("identity", clientIdentity),
				ion.String("cert_file", certFile),
			)

		default:
			// File exists but failed to read or parse — genuine provisioning error.
			return nil, fmt.Errorf("gatekeeper: failed to load client identity %s: %w", clientIdentity, err)
		}
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
