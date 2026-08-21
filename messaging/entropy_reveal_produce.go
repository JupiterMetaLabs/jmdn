package messaging

// The PRODUCTION side of Architecture §4.3 Decision A — this node computing its
// own reveal for an epoch. New 2026-08-20.
//
// Under the superseded commit-reveal design this file could not have existed
// usefully: producing a reveal meant generating a 32-byte secret, persisting it
// durably, publishing a commitment in one phase and the secret in another, and
// surviving a restart in between. Architecture §4.3 flagged the whole thing as
// missing ("no secret generator exists... that caller is not written").
//
// Under Decision A the entire lifecycle collapses to one deterministic
// signature. There is nothing to generate, nothing to store, and no phase
// ordering to get wrong — which is why this is a small file rather than the
// secret-store subsystem the old design needed.
//
// # What is wired, and the one seam that is not
//
// ProduceRevealForEpoch is complete: it decides whether this node is on the
// epoch's entropy committee and, if so, returns the exact bytes to publish.
// What does NOT exist yet is the transport that carries that value into a
// proposed block (Architecture §4.4's RevealPush, and the block-assembly step
// that fills config.ZKBlock.RandaoReveals). Until that lands, nothing calls
// this in production and block.RandaoReveals stays empty — the same
// honest-inert state every other M4 stage is in.
//
// Deliberately NOT done here: attaching to a block. That belongs with the
// proposal path, and faking it (e.g. writing into a block struct this package
// happens to see) would create a reveal path that bypasses §4.4's delivery
// design entirely.
import (
	"errors"
	"fmt"
	"sync"

	"github.com/JupiterMetaLabs/avc/randao"
	ic "github.com/libp2p/go-libp2p/core/crypto"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// ErrNoNodeIdentity is returned when SetNodeIdentity has never been called.
// Fail-closed and deliberately distinct from "not seated": one means this node
// cannot sign anything, the other means it has nothing to sign.
var ErrNoNodeIdentity = errors.New("messaging: no node identity installed (fail closed): call SetNodeIdentity at startup")

// ErrNotOnEntropyCommittee means this node is not among epoch's m revealers.
// Not an error condition in the operational sense — most nodes are not seated
// in most epochs — but returned as an error so a caller can never mistake an
// empty reveal for a valid one.
var ErrNotOnEntropyCommittee = errors.New("messaging: this node is not on the entropy committee for this epoch")

// Node identity registration. Same install-at-startup pattern as
// SetBeaconSource — messaging cannot reach into the node package (which
// already depends on messaging), so the owner registers itself here.
var (
	nodeIdentityMu   sync.RWMutex
	nodeIdentityPriv ic.PrivKey
	nodeIdentityPeer string
)

// SetNodeIdentity installs this node's libp2p identity — the same key pair
// node/node.go creates with crypto.GenerateKeyPair(crypto.Ed25519, 0), and the
// peer ID derived from it.
//
// It is the ed25519 identity key, reused rather than a new key: the peer ID
// self-certifies it, so no registration or key-distribution step is needed for
// other nodes to verify this node's reveals. Domain separation
// (randao.RevealMessage's tag) is what keeps that reuse safe against a reveal
// signature being replayable as libp2p handshake auth.
//
// Call once at startup. Passing a nil key or empty peer ID is rejected rather
// than stored, so a misconfigured caller fails here instead of producing
// unverifiable reveals later.
func SetNodeIdentity(priv ic.PrivKey, peerID string) error {
	if priv == nil {
		return fmt.Errorf("%w: nil private key", ErrNoNodeIdentity)
	}
	if peerID == "" {
		return fmt.Errorf("%w: empty peer ID", ErrNoNodeIdentity)
	}
	// Verify the pair actually matches before storing it. randao.SignReveal
	// checks this too, but catching it at install time turns a
	// silently-rejected-every-epoch failure into a startup error.
	if _, err := randao.SignReveal(priv, BLS_Signer.DomainChainID(), 0, peerID); err != nil {
		return fmt.Errorf("messaging: refusing to install node identity: %w", err)
	}

	nodeIdentityMu.Lock()
	nodeIdentityPriv = priv
	nodeIdentityPeer = peerID
	nodeIdentityMu.Unlock()
	return nil
}

// nodeIdentity returns the installed identity, or an error if none is.
func nodeIdentity() (ic.PrivKey, string, error) {
	nodeIdentityMu.RLock()
	defer nodeIdentityMu.RUnlock()
	if nodeIdentityPriv == nil || nodeIdentityPeer == "" {
		return nil, "", ErrNoNodeIdentity
	}
	return nodeIdentityPriv, nodeIdentityPeer, nil
}

// SelfOnEntropyCommittee reports whether this node is one of epoch's m
// revealers.
//
// Derived by running the same SelectEntropyCommittee draw every other node
// runs — never transmitted, never trusted from a peer. That is the same
// "committee is derived, never received" property block-committee selection
// already relies on (Communication-Flow Stage 3).
func SelfOnEntropyCommittee(epoch uint64) (bool, error) {
	_, peerID, err := nodeIdentity()
	if err != nil {
		return false, err
	}
	members, err := SelectEntropyCommittee(epoch)
	if err != nil {
		return false, err
	}
	for _, m := range members {
		if m.PeerID == peerID {
			return true, nil
		}
	}
	return false, nil
}

// ProduceRevealForEpoch returns this node's reveal for epoch — the 64-byte
// ed25519 signature to publish — or an error explaining why it has none.
//
// Deterministic: calling it repeatedly within an epoch returns identical bytes.
// That is the property that makes retrying safe, and it is what lets §4.4's
// RevealPush design push once per slot across the whole reveal window without
// any risk of producing a second, competing valid reveal. Under commit-reveal
// that same retry pattern would have needed the persisted secret to still be
// there.
//
// Fails closed in every branch: no identity, no resolvable committee, or not
// seated all return an error and no bytes. There is no path here that returns
// a reveal this node cannot prove.
func ProduceRevealForEpoch(epoch uint64) ([]byte, error) {
	priv, peerID, err := nodeIdentity()
	if err != nil {
		return nil, err
	}

	seated, err := SelfOnEntropyCommittee(epoch)
	if err != nil {
		return nil, fmt.Errorf("messaging: resolving entropy committee for epoch %d: %w", epoch, err)
	}
	if !seated {
		return nil, fmt.Errorf("%w: epoch %d", ErrNotOnEntropyCommittee, epoch)
	}

	sig, err := randao.SignReveal(priv, BLS_Signer.DomainChainID(), epoch, peerID)
	if err != nil {
		return nil, fmt.Errorf("messaging: producing reveal for epoch %d: %w", epoch, err)
	}
	return sig, nil
}

// Errors from the reveal inbox (entropy_reveal_inbox.go). Declared here beside
// the other reveal-production errors so all of them are in one place.
var (
	// ErrEmptyRevealPeer means a reveal arrived with no claimed peer ID.
	ErrEmptyRevealPeer = errors.New("messaging: reveal has no proposer ID (fail closed)")

	// ErrRevealDidNotVerify means the ed25519 check failed. Under Decision A a
	// reveal is a signature over a fixed, epoch-bound message, so this is a
	// protocol violation — not a transient condition.
	ErrRevealDidNotVerify = errors.New("messaging: reveal failed ed25519 verification (fail closed)")

	// ErrConflictingReveal means a second, DIFFERENT reveal arrived for a peer
	// that already has one this epoch. Exactly one signature can be valid per
	// (peer, epoch), so two different valid-looking values means one is forged;
	// the first is kept and the second rejected rather than overwriting.
	ErrConflictingReveal = errors.New("messaging: conflicting second reveal for this peer and epoch (first one kept)")
)
