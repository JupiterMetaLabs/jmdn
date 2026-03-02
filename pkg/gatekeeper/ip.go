package gatekeeper

import (
	"context"
	"net"
	"net/http"
	"strings"

	"jmdn/config/settings"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// IPExtractor handles the logic of resolving the True Client IP
// while defending against X-Forwarded-For spoofing.
type IPExtractor struct {
	config      *settings.SecurityConfig
	trustedNets []*net.IPNet // Pre-parsed CIDR networks
}

// NewIPExtractor creates a new extractor with pre-parsed trusted proxy CIDRs.
func NewIPExtractor(cfg *settings.SecurityConfig) *IPExtractor {
	var nets []*net.IPNet
	for _, cidr := range cfg.TrustedProxies {
		// Try CIDR first ("10.0.0.0/8")
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			nets = append(nets, ipNet)
			continue
		}
		// Try single IP ("10.0.0.1" → "10.0.0.1/32")
		ip := net.ParseIP(cidr)
		if ip != nil {
			mask := net.CIDRMask(128, 128)
			if ip.To4() != nil {
				mask = net.CIDRMask(32, 32)
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: mask})
		}
	}
	return &IPExtractor{config: cfg, trustedNets: nets}
}

// headerGetter is a function that retrieves a header value by name.
// This abstracts the difference between gRPC metadata and HTTP headers.
type headerGetter func(name string) string

// GetClientIP extracts the client IP from gRPC context, respecting Proxy headers
// ONLY if the direct connection comes from a trusted proxy.
func (e *IPExtractor) GetClientIP(ctx context.Context) string {
	// 1. Start with Direct IP (Peer)
	directIP := "unknown"
	if p, ok := peer.FromContext(ctx); ok {
		host, _, err := net.SplitHostPort(p.Addr.String())
		if err == nil {
			directIP = host
		} else {
			directIP = p.Addr.String()
		}
	}

	// 2. Resolve real IP using shared logic
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return directIP
	}
	return e.resolveForwardedIP(directIP, func(name string) string {
		if vals := md.Get(name); len(vals) > 0 {
			return vals[0]
		}
		return ""
	})
}

// GetClientIPFromRequest extracts the client IP from an HTTP request (for Gin/HTTP).
// Same trust-proxy logic as GetClientIP.
func (e *IPExtractor) GetClientIPFromRequest(req *http.Request) string {
	// 1. Start with Direct IP (RemoteAddr)
	var directIP string
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err == nil {
		directIP = host
	} else {
		directIP = req.RemoteAddr
	}

	// 2. Resolve real IP using shared logic
	return e.resolveForwardedIP(directIP, req.Header.Get)
}

// resolveForwardedIP contains the shared header-checking logic used by both
// gRPC and HTTP extractors. It only trusts forwarded headers if BOTH:
//   - TrustForwardedHeaders is enabled in config
//   - The direct connection is from a trusted proxy
func (e *IPExtractor) resolveForwardedIP(directIP string, getHeader headerGetter) string {
	if !e.config.TrustForwardedHeaders || !e.IsTrustedProxy(directIP) {
		return directIP
	}

	// Priority 1: Cloudflare (CF-Connecting-IP) — most reliable behind CF
	if cfIP := getHeader("cf-connecting-ip"); cfIP != "" {
		return strings.TrimSpace(cfIP)
	}

	// Priority 2: Standard X-Forwarded-For (client, proxy1, proxy2)
	if xff := getHeader("x-forwarded-for"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			clientIP := strings.TrimSpace(parts[0])
			if clientIP != "" {
				return clientIP
			}
		}
	}

	// Priority 3: X-Real-IP (Nginx/Envoy)
	if realIP := getHeader("x-real-ip"); realIP != "" {
		return strings.TrimSpace(realIP)
	}

	return directIP
}

// IsTrustedProxy checks if an IP belongs to the trusted proxy list using CIDR matching.
// If TrustedProxies is empty, returns false (trust nobody — safe default).
func (e *IPExtractor) IsTrustedProxy(ip string) bool {
	if len(e.trustedNets) == 0 {
		return false // Empty list = trust nobody
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidrNet := range e.trustedNets {
		if cidrNet.Contains(parsed) {
			return true
		}
	}
	return false
}
