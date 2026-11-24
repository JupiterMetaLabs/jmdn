package reputation

import "strings"

// Fault attribution guards (D-60). The reputation model only penalises OBJECTIVE
// faults (see package doc). A validator that is simply BEHIND the sequencer — one
// block short, so it cannot resolve the proposal's parent or the senders created
// in the missing block — is not at fault: it correctly refuses to validate what
// it cannot verify. Yet such a peer surfaces to the sequencer in two ways that
// the naive scoring path mistook for protocol faults:
//
//  1. a vote response carrying NO BLS signature (an abstain / "can't vote"),
//     which fails BLS verification and was charged BadSignature (-0.30); and
//  2. a reject whose reason is a liveness/sync condition ("not synced",
//     "validation returned false", "sender account not found", missing parent),
//     not a signature or equivocation fault.
//
// Charging these turned infrastructure lag into a reputation death-spiral
// (incident 2026-09-17). These helpers keep the penalty for a GENUINELY invalid
// signature — a non-empty signature that fails to verify — and drop it for the
// behind/abstain shapes.

// behindReasonSubstrings are lowercased fragments of a reject/abstain reason
// that indicate the peer is BEHIND or not-yet-synced, i.e. unable to validate,
// not malicious. Matched case-insensitively as substrings so log/format drift
// (e.g. StatefulChecker "sender account <addr> not found in cache") still hits.
var behindReasonSubstrings = []string{
	"not synced",
	"node not synced",
	"not yet synced",
	"periodnotsynced",
	"validation returned false",
	"not found in cache",
	"account not found",
	"unknown parent",
	"missing parent",
	"parent not found",
	"unknown block",
	"catch up",
	"catching up",
	"behind",
}

// IsBehindReason reports whether a rejection/abstain reason indicates the peer
// is behind / unable to validate (not an objective fault). Empty reason → false.
func IsBehindReason(reason string) bool {
	if reason == "" {
		return false
	}
	r := strings.ToLower(reason)
	for _, sub := range behindReasonSubstrings {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}

// ShouldChargeBadSignature decides whether a vote response that failed BLS
// verification is a chargeable BadSignature fault.
//
//   - hasSignature == false: the response carried NO BLS data — an abstain from
//     a peer that could not vote (typically because it is behind). NOT charged.
//   - a behind/sync reason accompanies it: NOT charged.
//   - otherwise (a non-empty signature that genuinely fails to verify, with no
//     behind reason): a real protocol fault — charged.
//
// This is the guard the consensus verify site must consult before
// Default.Observe(BadSignature).
func ShouldChargeBadSignature(hasSignature bool, rejectionReason string) bool {
	if !hasSignature {
		return false
	}
	if IsBehindReason(rejectionReason) {
		return false
	}
	return true
}

// AnyBehindReason reports whether any reason in a response's RejectionReasons map
// indicates behind/sync. Convenience for the consensus site, whose BLSresponse
// carries RejectionReasons map[peerID]reason.
func AnyBehindReason(reasons map[string]string) bool {
	for _, reason := range reasons {
		if IsBehindReason(reason) {
			return true
		}
	}
	return false
}
