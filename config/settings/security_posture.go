package settings

import (
	"fmt"
	"sort"
	"strings"
)

// Security posture validation (audit SEC-03).
//
// The compiled security defaults ship most services with auth_type=none, and the
// shipped YAML leaves eth_rpc/mempool none too, so a node booted WITHOUT a
// hardened config exposes unauthenticated services on public interfaces. This
// file turns that from a silent condition into either a loud boot warning
// (default) or a hard fail-closed refusal (security.strict_posture=true).
//
// The check is BIND-DRIVEN, not a hardcoded list: a gatekeeper-mediated service
// is a risk only when it is auth_type=none AND bound to a non-loopback address.
// Loopback-only services (CLI, geth, smart, thebe-debug) with no auth are fine —
// they are not remotely reachable. P2P/BFT/mempool paths are out of scope here:
// they authenticate out-of-band (libp2p transport, committee BLS), not via the
// gatekeeper, so they have no gatekeeper bind entry.

// gatekeeperServiceBind maps each gatekeeper-mediated service to the bind address
// that decides whether it faces untrusted networks. Services absent from this map
// are not HTTP/gRPC gatekeeper surfaces (they authenticate out-of-band).
func gatekeeperServiceBind(b BindSettings) map[string]string {
	return map[string]string{
		ServiceExplorerAPI:     b.API,      // public HTTP data API
		ServiceEthRPC:          b.Facade,   // public JSON-RPC
		ServiceBlockIngestHTTP: b.BlockGen, // block/sequencer ingest (admin)
		ServiceDID:             b.DID,      // identity service
		ServiceCLI:             b.CLI,      // admin CLI (loopback)
		ServiceEthGRPC:         b.Geth,     // eth gRPC (loopback)
	}
	// Deliberately EXCLUDED: block_ingest_grpc and the BFT buddy/sequencer paths.
	// Those are consensus/P2P surfaces authenticated by libp2p transport + committee
	// BLS, NOT by the gatekeeper — auth_type=none there is correct, so bind-based
	// flagging would be a false positive. mempool is a client dial target, not a
	// local bind. If any of these later gains a gatekeeper HTTP surface, add it here.
}

// isLoopbackBind reports whether a bind address is loopback-only (not remotely
// reachable). Empty is treated as NON-loopback (fail-closed: an unset bind on a
// public service should be flagged, not silently trusted).
func isLoopbackBind(addr string) bool {
	a := strings.TrimSpace(strings.ToLower(addr))
	switch a {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return strings.HasPrefix(a, "127.")
}

// publicRPCServices are OPEN BY DESIGN — a public, Ethereum-compatible JSON-RPC
// endpoint that must be reachable without a token or JWT (operator decision,
// 2026-08-18). They are therefore EXEMPT from the auth requirement, but because
// rate limiting is their only abuse control, the posture check requires them to
// carry an effective rate limit (per-service or the global cap) instead.
var publicRPCServices = map[string]bool{
	ServiceEthRPC: true,
}

// PostureViolation is one gatekeeper service that fails the public-exposure
// policy: either unauthenticated on a public bind, or an open public RPC with no
// rate limit.
type PostureViolation struct {
	Service string
	Bind    string
	Reason  string
}

func (v PostureViolation) String() string {
	return fmt.Sprintf("%s (%s, bind=%s)", v.Service, v.Reason, v.Bind)
}

// InsecurePublicServices returns the gatekeeper-mediated services that violate
// the public-exposure policy while security is enabled:
//   - a normal service that is auth_type=none on a non-loopback bind; or
//   - a public-by-design RPC (publicRPCServices) on a non-loopback bind with NO
//     effective rate limit (neither a per-service limit nor the global cap).
//
// Public RPC is intentionally unauthenticated, so it is NOT flagged for missing
// auth — only for missing rate limiting. Empty slice = policy satisfied.
// Deterministic order.
func (c *NodeConfig) InsecurePublicServices() []PostureViolation {
	if !c.Security.Enabled {
		return nil
	}
	binds := gatekeeperServiceBind(c.Binds)
	var out []PostureViolation
	for svc, bind := range binds {
		policy, ok := c.Security.Services[svc]
		if !ok || isLoopbackBind(bind) {
			continue
		}
		if publicRPCServices[svc] {
			// Open-by-design: require rate limiting, not auth.
			if policy.RateLimit <= 0 && c.Security.GlobalRateLimit <= 0 {
				out = append(out, PostureViolation{Service: svc, Bind: bind, Reason: "public RPC with no rate limit"})
			}
			continue
		}
		if policy.AuthType == AuthTypeNone {
			out = append(out, PostureViolation{Service: svc, Bind: bind, Reason: "auth_type=none"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// ValidateSecurityPosture returns a non-nil error ONLY when strict_posture is set
// and at least one public service is unauthenticated (SEC-03 fail-closed refusal).
// In the default (non-strict) mode it returns nil; the caller should still call
// InsecurePublicServices to emit a boot warning. Keeping the warn/fatal split in
// the caller lets the node log every violation before deciding whether to abort.
func (c *NodeConfig) ValidateSecurityPosture() error {
	v := c.InsecurePublicServices()
	if len(v) == 0 || !c.Security.StrictPosture {
		return nil
	}
	names := make([]string, len(v))
	for i := range v {
		names[i] = v[i].String()
	}
	return fmt.Errorf("SEC-03 fail-closed (security.strict_posture=true): unauthenticated public service(s): %s — set auth_type token/mtls for each, or clear strict_posture to run open", strings.Join(names, "; "))
}
