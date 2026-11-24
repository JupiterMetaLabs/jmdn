package messaging

// A-2 (2026-10-22 audit; LLD-A2-BLS-RANDAO-reveal): from the chain's entropy
// cut-over (entropy_cutover.go), a member's reveal is a BLS signature under its
// committee BLS key. One key and one message give exactly one valid BLS reveal
// (randao.BLSRevealVerifier), so a member cannot grind its contribution, as it
// could with the Ed25519 reveal by choosing its nonce. Epochs before the
// cut-over keep the Ed25519 reveal so a chain's existing history verifies.
//
// # The scheme is chosen by epoch, never by the reveal bytes
//
// Both reveals are 64 bytes. Every path that checks, produces or seats a
// reveal - the inbox (AddInboundReveal), the per-epoch Accumulator
// (entropyAccumulatorFor), ProduceRevealForEpoch and the seat checks - asks
// afterEntropyCutover(epoch), so a reveal is only ever valid under its epoch's
// scheme.
//
// # Every node must resolve the same key for a member
//
// The BLS key comes from the entropy committee for the epoch
// (committee.Member.BLSPub), the same set the Accumulator uses as `expected`.
// That set is only identical on every node once the pool is chain-anchored
// (entropyAnchoredPool). On the live pool, two nodes could hold different keys
// for one member, accept the reveal on some and not others, and finalise
// different entropy. So a BLS epoch whose pool is not anchored has no expected
// set, no verifier and no valid reveal, and decideEpoch takes the
// deterministic fallback for it (ErrBLSRevealNeedsAnchoredPool). "Not
// anchored" is decided from the chain (entropyAnchoredPool returns
// handled=false only when no anchor precedes the epoch's freeze cutoff), so
// every node reaches the same decision. Stage 2 also refuses to install
// without committee anchoring (Sequencer checkRevealAnchoring), so this
// covers only BLS epochs before the first anchor.
import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/JupiterMetaLabs/avc/randao"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

var (
	// ErrBLSRevealNeedsAnchoredPool means epoch's entropy committee is not drawn
	// from a chain-anchored pool, so nodes could disagree on members' keys. The
	// epoch takes the deterministic fallback.
	ErrBLSRevealNeedsAnchoredPool = errors.New("messaging: BLS reveals need a chain-anchored entropy pool for this epoch (fail closed)")

	// ErrRevealBLSKeyMismatch means this node's BLS key is not the key the
	// committee snapshot binds to its peer ID. Every reveal it produced would
	// be rejected everywhere, so it produces none.
	ErrRevealBLSKeyMismatch = errors.New("messaging: this node's BLS key is not the key its committee snapshot binds (cannot reveal)")
)

// loadRevealBLSKey returns this node's committee BLS key pair: the same cached
// key it votes and signs checkpoints with (BLS_Signer.LocalBLSKeypair), so a
// node can never vote under one key and reveal under another. A variable so
// tests need no key file.
var loadRevealBLSKey = BLS_Signer.LocalBLSKeypair

// blsRevealCommittees caches each epoch's anchored entropy committee. It
// is final once resolved (entropyAnchoredPool fails closed until it is), and
// the inbox otherwise re-derives it for every inbound reveal. Pruned with the
// inbox (pruneRevealsBelow).
var blsRevealCommittees = struct {
	mu sync.Mutex
	by map[uint64][]committee.Member
}{by: make(map[uint64][]committee.Member)}

// blsRevealCommittee returns epoch's entropy committee, requiring a
// chain-anchored pool. Every reveal path uses it: who is expected to reveal,
// who may reveal, and each member's key all come from this one set.
func blsRevealCommittee(epoch uint64) ([]committee.Member, error) {
	blsRevealCommittees.mu.Lock()
	members, ok := blsRevealCommittees.by[epoch]
	blsRevealCommittees.mu.Unlock()
	if ok {
		return members, nil
	}

	members, err := revealCommitteeSource(epoch)
	if err != nil {
		return nil, err
	}

	blsRevealCommittees.mu.Lock()
	blsRevealCommittees.by[epoch] = members
	blsRevealCommittees.mu.Unlock()
	return members, nil
}

// revealCommitteeSource resolves epoch's anchored entropy committee, uncached.
// A variable only so tests of the inbox, fold and replay mechanics can supply
// members with known BLS keys; the anchored path itself is tested end to end
// in entropy_reveal_bls_test.go.
var revealCommitteeSource = func(epoch uint64) ([]committee.Member, error) {
	// Stage 1 (no beacon) keeps failing exactly as before, with
	// ErrNoBeaconInstalled, so no Stage-1 node ever treats an epoch as an
	// unanchored fallback.
	if activeBeacon() == nil {
		return nil, ErrNoBeaconInstalled
	}
	_, anchored, err := entropyAnchoredPool(epoch)
	if err != nil {
		return nil, err
	}
	if !anchored {
		return nil, fmt.Errorf("%w: epoch %d", ErrBLSRevealNeedsAnchoredPool, epoch)
	}
	return SelectEntropyCommittee(committee.EntropyEpoch(epoch))
}

func pruneBLSRevealCommitteesBelow(epoch uint64) {
	blsRevealCommittees.mu.Lock()
	for e := range blsRevealCommittees.by {
		if e < epoch {
			delete(blsRevealCommittees.by, e)
		}
	}
	blsRevealCommittees.mu.Unlock()
}

// blsRevealVerifierFor builds epoch's verifier from its committee's bound keys.
func blsRevealVerifierFor(epoch uint64, members []committee.Member) (randao.RevealVerifier, error) {
	keys := make(map[string][]byte, len(members))
	for _, m := range members {
		if len(m.BLSPub) > 0 {
			keys[m.PeerID] = m.BLSPub
		}
	}
	return randao.NewBLSRevealVerifier(func(e uint64, peerID string) ([]byte, bool) {
		if e != epoch {
			return nil, false
		}
		k, ok := keys[peerID]
		return k, ok
	})
}

// revealVerifierFor returns the verifier for epoch's reveals: Ed25519 before
// the cut-over, and from it a BLS verifier built from the epoch's anchored
// committee.
func revealVerifierFor(epoch uint64) (randao.RevealVerifier, error) {
	if !afterEntropyCutover(epoch) {
		return ed25519RevealVerifier, nil
	}
	members, err := blsRevealCommittee(epoch)
	if err != nil {
		return nil, err
	}
	return blsRevealVerifierFor(epoch, members)
}

// revealCommitteeFor is epoch's entropy committee under the epoch's scheme:
// the anchored committee from the cut-over on, SelectEntropyCommittee before
// it (the source those epochs were revealed under).
func revealCommitteeFor(epoch uint64) ([]committee.Member, error) {
	if afterEntropyCutover(epoch) {
		return blsRevealCommittee(epoch)
	}
	return SelectEntropyCommittee(committee.EntropyEpoch(epoch))
}

// produceBLSReveal signs this node's BLS reveal for epoch, after checking its
// key is the one the snapshot binds to it. Checked on every reveal, not at
// boot: the anchored snapshot is often not available until the node has
// synced.
func produceBLSReveal(epoch uint64, peerID string) ([]byte, error) {
	members, err := blsRevealCommittee(epoch)
	if err != nil {
		return nil, fmt.Errorf("messaging: resolving entropy committee for epoch %d: %w", epoch, err)
	}
	var bound []byte
	for _, m := range members {
		if m.PeerID == peerID {
			bound = m.BLSPub
			break
		}
	}
	priv, pub, err := loadRevealBLSKey()
	if err != nil {
		return nil, fmt.Errorf("messaging: loading BLS key for epoch %d reveal: %w", epoch, err)
	}
	if len(bound) == 0 || !bytes.Equal(pub, bound) {
		return nil, fmt.Errorf("%w: epoch %d, peer %s", ErrRevealBLSKeyMismatch, epoch, peerID)
	}
	return randao.SignRevealBLS(priv, BLS_Signer.DomainChainID(), epoch, peerID)
}
