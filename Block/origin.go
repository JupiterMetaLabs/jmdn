package Block

import (
	"net"
	"strings"
)

// TxOrigin identifies the transport and network peer of a SubmitRawTransaction
// caller. Trust for the unsigned internal-deployment bypass is decided from
// this — WHO called — not from the transaction's shape. Before this, any remote
// peer could submit an unsigned {To:nil, V:nil} deployment attributed to an
// arbitrary `from` and skip all signature checks (audit SEC-02).
type TxOrigin struct {
	Transport  string // "http", "grpc", "internal"
	RemoteAddr string // peer host or host:port; "" when unknown / in-process
	trusted    bool   // same-host / in-process, computed at the boundary
}

// OriginHTTP builds an origin from an HTTP caller's SOCKET peer address
// (request.RemoteAddr). Do NOT pass X-Forwarded-For / ClientIP here — those are
// client-spoofable (audit SEC-05) and must never feed a trust decision.
func OriginHTTP(remoteAddr string) TxOrigin {
	return TxOrigin{Transport: "http", RemoteAddr: remoteAddr, trusted: IsLoopbackAddr(remoteAddr)}
}

// OriginGRPC builds an origin from a gRPC caller's peer address.
func OriginGRPC(remoteAddr string) TxOrigin {
	return TxOrigin{Transport: "grpc", RemoteAddr: remoteAddr, trusted: IsLoopbackAddr(remoteAddr)}
}

// OriginInternal marks an in-process (same-binary) Go caller as trusted. Use
// ONLY for direct calls, never for anything reachable from a network surface.
func OriginInternal() TxOrigin {
	return TxOrigin{Transport: "internal", trusted: true}
}

// OriginUntrusted is the fail-closed default for a caller that cannot prove its
// peer (e.g. a public JSON-RPC facade with no socket peer in context).
func OriginUntrusted(transport string) TxOrigin {
	return TxOrigin{Transport: transport, trusted: false}
}

// Trusted reports whether the caller is same-host / in-process and therefore
// eligible for the internal-deployment signature bypass.
func (o TxOrigin) Trusted() bool { return o.trusted }

// IsLoopbackAddr reports whether addr (host or host:port) is a loopback /
// localhost / unix-socket peer. Unknown or empty → false (fail closed).
// Mirrors SmartContract/internal/router.loopbackOnlyInterceptor's acceptance set.
func IsLoopbackAddr(addr string) bool {
	if addr == "" {
		return false
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	switch host {
	case "localhost", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.HasPrefix(host, "127.")
}
