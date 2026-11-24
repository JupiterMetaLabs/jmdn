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
// # Who calls it
//
// ProduceRevealForEpoch decides whether this node is on the epoch's entropy
// committee and, if so, returns the exact bytes to publish. Two callers carry
// them on: ensureSelfReveal files them in this node's own inbox, for blocks it
// proposes (entropy_reveal_inbox.go), and RevealPush sends them to the slot's
// proposer (entropy_reveal_push.go).
//
// Deliberately NOT done here: attaching to a block. That belongs with the
// proposal path (RevealsForBlock), so every reveal reaches a block through the
// verified inbox.
import (
	"errors"
	"fmt"
	"sync"

	"github.com/JupiterMetaLabs/avc/randao"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

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
//
// The libp2p key signs this node's reveals for epochs BEFORE the entropy
// cut-over (Ed25519, self-certified by the peer ID). From the cut-over on,
// reveals are signed with the committee BLS key (entropy_reveal_bls.go) and
// the libp2p key only proves the peer ID is this node's own.
var (
	nodeIdentityMu   sync.RWMutex
	nodeIdentityPriv ic.PrivKey
	nodeIdentityPeer string
)

// ErrNodeIdentityMismatch means the private key passed to SetNodeIdentity does
// not derive the peer ID passed with it.
var ErrNodeIdentityMismatch = errors.New("messaging: private key does not match the peer ID")

// SetNodeIdentity installs this node's libp2p peer ID — the identity its
// reveals are filed under and its entropy-committee seat is looked up by.
//
// priv must be the libp2p (ed25519) key that peerID is derived from; a
// mismatch is refused, so a misconfigured caller fails at startup instead of
// revealing under someone else's peer ID. Call once at startup.
func SetNodeIdentity(priv ic.PrivKey, peerID string) error {
	if priv == nil {
		return fmt.Errorf("%w: nil private key", ErrNoNodeIdentity)
	}
	if peerID == "" {
		return fmt.Errorf("%w: empty peer ID", ErrNoNodeIdentity)
	}
	derived, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("messaging: refusing to install node identity: %w", err)
	}
	if derived.String() != peerID {
		return fmt.Errorf("messaging: refusing to install node identity: %w (key gives %s, claimed %s)",
			ErrNodeIdentityMismatch, derived, peerID)
	}
	// The key must also be able to sign a pre-cut-over (Ed25519) reveal;
	// catching that here turns a silently-rejected-every-epoch failure into a
	// startup error.
	if _, err := randao.SignReveal(priv, BLS_Signer.DomainChainID(), 0, peerID); err != nil {
		return fmt.Errorf("messaging: refusing to install node identity: %w", err)
	}

	nodeIdentityMu.Lock()
	nodeIdentityPriv = priv
	nodeIdentityPeer = peerID
	nodeIdentityMu.Unlock()
	return nil
}

// nodeIdentityKey returns the installed libp2p key, for pre-cut-over reveals.
func nodeIdentityKey() (ic.PrivKey, error) {
	nodeIdentityMu.RLock()
	defer nodeIdentityMu.RUnlock()
	if nodeIdentityPriv == nil {
		return nil, ErrNoNodeIdentity
	}
	return nodeIdentityPriv, nil
}

// nodeIdentity returns the installed peer ID, or an error if none is.
func nodeIdentity() (string, error) {
	nodeIdentityMu.RLock()
	defer nodeIdentityMu.RUnlock()
	if nodeIdentityPeer == "" {
		return "", ErrNoNodeIdentity
	}
	return nodeIdentityPeer, nil
}

// SelfOnEntropyCommittee reports whether this node is one of epoch's m
// revealers.
//
// Derived by running the same committee draw every other node runs
// (revealCommitteeFor: the anchored committee from the cut-over on) — never
// transmitted, never trusted from a peer. That is the same "committee is
// derived, never received" property block-committee selection already relies
// on (Communication-Flow Stage 3). A BLS epoch with no anchored pool returns
// an error: nobody is expected to reveal in it.
func SelfOnEntropyCommittee(epoch uint64) (bool, error) {
	peerID, err := nodeIdentity()
	if err != nil {
		return false, err
	}
	members, err := revealCommitteeFor(epoch)
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
// signature to publish (Ed25519 before the cut-over, BLS from it) — or an
// error explaining why it has none.
//
// Deterministic: calling it repeatedly within an epoch returns identical bytes.
// That is the property that makes retrying safe, and it is what lets §4.4's
// RevealPush design push once per slot across the whole reveal window without
// any risk of producing a second, competing valid reveal. Under commit-reveal
// that same retry pattern would have needed the persisted secret to still be
// there.
//
// Fails closed in every branch: no identity, no resolvable committee, not
// seated, or a BLS key that is not the one the snapshot binds all return an
// error and no bytes. There is no path here that returns a reveal this node
// cannot prove.
func ProduceRevealForEpoch(epoch uint64) ([]byte, error) {
	peerID, err := nodeIdentity()
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

	// A-2: from the cut-over the reveal is a BLS signature under the
	// committee BLS key (entropy_reveal_bls.go).
	if afterEntropyCutover(epoch) {
		sig, err := produceBLSReveal(epoch, peerID)
		if err != nil {
			return nil, fmt.Errorf("messaging: producing BLS reveal for epoch %d: %w", epoch, err)
		}
		return sig, nil
	}
	priv, err := nodeIdentityKey()
	if err != nil {
		return nil, err
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

	// ErrRevealDidNotVerify means the epoch's reveal check failed (Ed25519
	// before the entropy cut-over, BLS from it). A reveal is a signature over a
	// fixed, epoch-bound message, so this is a protocol violation — not a
	// transient condition.
	ErrRevealDidNotVerify = errors.New("messaging: reveal failed verification (fail closed)")

	// ErrRevealEpochOutOfWindow means a reveal names an epoch too far from
	// this node's current one to be useful.
	ErrRevealEpochOutOfWindow = errors.New("messaging: reveal epoch outside the accepted window")

	// ErrRevealInboxFull means the inbox is at its epoch or per-epoch bound
	// and the reveal did not qualify for a slot.
	ErrRevealInboxFull = errors.New("messaging: reveal inbox full")

	// ErrConflictingReveal means a second, DIFFERENT reveal arrived for a peer
	// that already has one this epoch. Exactly one signature can be valid per
	// (peer, epoch), so two different valid-looking values means one is forged;
	// the first is kept and the second rejected rather than overwriting.
	ErrConflictingReveal = errors.New("messaging: conflicting second reveal for this peer and epoch (first one kept)")
)
