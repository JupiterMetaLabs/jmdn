// Package l1auth is the sender-authentication decision for L1-finality gossip
// messages (the Ethereum commit-rollup tx hash + L1 block number stamped onto a
// local block).
//
// # WHY THIS EXISTS (Ibnu76 report, 2026-09-25)
//
// The L1-commit gossip topic is public and had no publisher check beyond a
// self-echo guard, so ANY peer on the mesh could publish a forged L1-commit and
// every receiving node stamped the attacker's tx hash onto up to 10,000 blocks —
// served afterward over eth_getBlockByNumber(wantL1Commit=true) as proof of L1
// finality, and invisible to the SyncMonitor Merkle fingerprint (which has no L1
// fields), so it never self-healed.
//
// The fix reuses the SAME authenticated identity the vote path already relies on:
// under libp2p gossipsub's default StrictSign policy, msg.Sender is the
// cryptographically authenticated originator (libp2p GetFrom, set at
// SubscriberHelper.go / SubscriptionManager.go), NOT the attacker-controllable
// msg.Data.Sender JSON field (see the D-26(a) note in subscriptionService.go).
// So requiring msg.Sender to equal the trusted sequencer's peer ID is a sound,
// wire-format-free authentication: an attacker can only publish as themselves.
//
// This package is pure (peer-id decode + compare, no I/O) so the decision is
// unit-testable without the consensus/network stack.
package l1auth

import (
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Enforced reports whether L1-commit sender authentication is active. It is active
// exactly when a trusted sequencer peer ID is configured (consensus.sequencer_peer_id).
// Empty = legacy unauthenticated behavior (caller should WARN loudly and accept, to
// stay non-bricking pre-rollout); non-empty = fail-closed enforcement. Production
// posture requires it (see main.go boot gate), so the empty case cannot ship to
// mainnet — the lesson from the block-858 finding-4 empty-pin trap.
func Enforced(trustedSequencerID string) bool {
	return strings.TrimSpace(trustedSequencerID) != ""
}

// IsSequencer reports whether the authenticated gossip sender is the trusted
// sequencer. authenticatedSender MUST be the transport-authenticated identity
// (msg.Sender == libp2p GetFrom under StrictSign), never the self-declared payload
// field. Both IDs are decoded so different string encodings of the same identity
// compare equal; an empty or undecodable ID is never a match (fail-closed).
func IsSequencer(authenticatedSender, trustedSequencerID string) bool {
	a := strings.TrimSpace(authenticatedSender)
	b := strings.TrimSpace(trustedSequencerID)
	if a == "" || b == "" {
		return false
	}
	pa, err := peer.Decode(a)
	if err != nil {
		return false
	}
	pb, err := peer.Decode(b)
	if err != nil {
		return false
	}
	return pa == pb
}
