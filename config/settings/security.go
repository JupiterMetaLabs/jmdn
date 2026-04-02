package settings

import "os"

// Service Identifiers (Constants to avoid magic strings)
const (
	ServiceExplorerAPI     = "explorer_api"
	ServiceBlockIngestHTTP = "block_ingest_http"
	ServiceEthRPC          = "eth_rpc"
	ServiceCLI             = "cli_admin" // Bind: 127.0.0.1
	ServiceBlockIngestGRPC = "block_ingest_grpc"
	ServiceDID             = "did_service"
	ServiceMempool         = "mempool_service"
	ServiceEthGRPC         = "eth_grpc"
	ServiceBFTBuddy        = "bft_buddy"
	ServiceBFTSequencer    = "bft_sequencer"
)

// AuthType defines the authentication method required for a service
type AuthType string

const (
	AuthTypeNone   AuthType = "none"
	AuthTypeToken  AuthType = "token"
	AuthTypeMTLS   AuthType = "mtls"
	AuthTypeHybrid AuthType = "hybrid" // Prefer mTLS, accept Token
)

// Policy defines the security rules for a specific service
type Policy struct {
	// TLS Configuration
	TLS bool `mapstructure:"tls" yaml:"tls"`

	// Authentication
	AuthType AuthType `mapstructure:"auth_type" yaml:"auth_type"`
	TokenEnv string   `mapstructure:"token_env" yaml:"token_env"` // Env var name containing the token

	// Rate Limiting
	RateLimit float64 `mapstructure:"rate_limit" yaml:"rate_limit"` // Requests per second
	Burst     int     `mapstructure:"burst" yaml:"burst"`

	// Certificate Overrides (Optional - Enterprise Feature)
	CertFile string `mapstructure:"cert_file" yaml:"cert_file"`
	KeyFile  string `mapstructure:"key_file" yaml:"key_file"`
	CAFile   string `mapstructure:"ca_file" yaml:"ca_file"` // For mTLS
}

// SecurityConfig is the master security configuration
// It replaces the old scattered env vars and simple structs.
type SecurityConfig struct {
	// Global Settings
	Enabled     bool   `mapstructure:"enabled" yaml:"enabled"`
	CertDir     string `mapstructure:"cert_dir" yaml:"cert_dir"`
	IPCacheSize int    `mapstructure:"ip_cache_size" yaml:"ip_cache_size"` // Size of the LRU cache for Rate Limiting

	// Global Rate Limit (Layer 1: aggregate per-IP cap across all services)
	GlobalRateLimit float64 `mapstructure:"global_rate_limit" yaml:"global_rate_limit"` // 0 = disabled
	GlobalBurst     int     `mapstructure:"global_burst" yaml:"global_burst"`

	// Proxy Configuration (Cloud Native / Load Balancer Support)
	TrustForwardedHeaders bool     `mapstructure:"trust_forwarded_headers" yaml:"trust_forwarded_headers"` // Helper to enable X-Forwarded-For logic
	TrustedProxies        []string `mapstructure:"trusted_proxies" yaml:"trusted_proxies"`                 // List of IP/CIDR to trust for headers (e.g. LB IPs)

	// Trusted Clients — IPs/CIDRs that bypass rate limiting entirely.
	// Use for co-located internal services (e.g. orchestrator on localhost, VPC peers).
	// Conceptually distinct from TrustedProxies (which controls header trust, not rate-limit bypass).
	TrustedClients []string `mapstructure:"trusted_clients" yaml:"trusted_clients"`

	// Legacy Secrets (Moved here for unification)
	ExplorerAPIKey string `mapstructure:"explorer_api_key" yaml:"explorer_api_key"`
	JWTSecret      string `mapstructure:"jwt_secret" yaml:"jwt_secret"`

	// Services map service name (e.g., "block_api") to its policy
	Services map[string]Policy `mapstructure:"services" yaml:"services"`

	// resolvedTokens caches env-var lookups done once at startup.
	// Key = TokenEnv (e.g. "SEQUENCER_TOKEN"), Value = resolved secret.
	// Not YAML-mapped — populated by ResolveTokens().
	resolvedTokens map[string]string `mapstructure:"-" yaml:"-"`
}

// DefaultSecurityConfig returns a safe-by-default configuration
func DefaultSecurityConfig() SecurityConfig {
	return SecurityConfig{
		Enabled:         true,
		CertDir:         "certs",
		IPCacheSize:     1000,
		GlobalRateLimit: 0, // Disabled global limits by default for back-compat
		GlobalBurst:     0,

		Services: map[string]Policy{
			// 1. Explorer API (HTTP public - Had basic token auth before)
			ServiceExplorerAPI: {
				TLS:       false,
				AuthType:  AuthTypeToken,
				TokenEnv:  "EXPLORER_API_KEY",
				RateLimit: 0,
				Burst:     0,
			},
			// 2. Block/Sequencer API (Internal)
			ServiceBlockIngestHTTP: {
				TLS:      false,
				AuthType: AuthTypeNone,
			},
			// 3. JSON-RPC (HTTP public)
			ServiceEthRPC: {
				TLS:      false,
				AuthType: AuthTypeNone,
			},
			// 4. CLI Admin (gRPC local)
			ServiceCLI: {
				TLS:      false,
				AuthType: AuthTypeNone,
			},
			// 5. Validator P2P
			ServiceBlockIngestGRPC: {
				TLS:      false,
				AuthType: AuthTypeNone,
			},
			// 6. DID Service
			ServiceDID: {
				TLS:      false,
				AuthType: AuthTypeNone,
			},
			// 7. Mempool/Routing Service
			ServiceMempool: {
				TLS:      false,
				AuthType: AuthTypeNone,
			},
			// 8. gETH gRPC Service
			ServiceEthGRPC: {
				TLS:      false,
				AuthType: AuthTypeNone,
			},
			// 9. BFT Buddy Service
			ServiceBFTBuddy: {
				TLS:      false,
				AuthType: AuthTypeNone,
			},
			// 10. BFT Sequencer gRPC Server
			ServiceBFTSequencer: {
				TLS:      false,
				AuthType: AuthTypeNone,
			},
		},
	}
}

// ResolveTokens eagerly resolves all service token env vars into a cached map.
// Must be called once at startup after SecurityConfig is fully populated.
// This eliminates per-request os.Getenv calls on the hot path.
func (c *SecurityConfig) ResolveTokens() {
	c.resolvedTokens = make(map[string]string, len(c.Services))
	for _, policy := range c.Services {
		if policy.TokenEnv == "" {
			continue
		}
		// Already resolved? Skip.
		if _, ok := c.resolvedTokens[policy.TokenEnv]; ok {
			continue
		}
		// Priority: well-known config fields → env var
		switch policy.TokenEnv {
		case "EXPLORER_API_KEY":
			if c.ExplorerAPIKey != "" {
				c.resolvedTokens[policy.TokenEnv] = c.ExplorerAPIKey
				continue
			}
		case "JWT_SECRET":
			if c.JWTSecret != "" {
				c.resolvedTokens[policy.TokenEnv] = c.JWTSecret
				continue
			}
		}
		// Fallback: env var lookup (done once, not per request)
		c.resolvedTokens[policy.TokenEnv] = os.Getenv(policy.TokenEnv)
	}
}

// GetResolvedToken returns the cached token for a given TokenEnv key.
// Falls back to well-known config fields if cache is not populated (defensive).
func (c *SecurityConfig) GetResolvedToken(tokenEnv string) string {
	if c.resolvedTokens != nil {
		if val, ok := c.resolvedTokens[tokenEnv]; ok {
			return val
		}
	}
	// Defensive fallback if ResolveTokens wasn't called
	switch tokenEnv {
	case "EXPLORER_API_KEY":
		return c.ExplorerAPIKey
	case "JWT_SECRET":
		return c.JWTSecret
	default:
		return os.Getenv(tokenEnv)
	}
}
