package messaging

// Stage B/C of the M4 pipeline — REWRITTEN 2026-08-20 for Architecture §4.3
// "Decision A": reveals are ed25519 signatures, not commit-reveal secrets.
//
// # What changed from the previous version, and why
//
// The previous version built each epoch's Accumulator on a *randao.Round
// wrapped in a *randao.RoundVerifier — the commit-reveal path. That path is
// superseded and its adapter is deleted. Three concrete problems went away
// with it, none of which were fixable within commit-reveal:
//
//   - It needed a commit phase nothing implemented, so every VerifyReveal call
//     on a freshly constructed Round returned false (ErrNoCommitment). The
//     pipeline could not have worked even with real reveals arriving.
//   - It needed a durable secret store between commit and reveal. A crash
//     between the two meant the member could never reveal, and under Rule 1
//     one such loss takes the whole epoch to fallback.
//   - Round.AddReveal is one-shot per participant, so anything that touched it
//     before Accumulator.Fold permanently broke the legitimate fold — a sharp
//     edge that needed a documented discipline to avoid.
//
// An ed25519 reveal has none of these. It is a deterministic signature over a
// domain-separated, epoch-bound message, so there is no secret, no commit
// phase, and nothing to persist: a node that restarts mid-epoch simply
// re-derives the identical reveal. See avc/randao/ed25519_reveal_verifier.go.
//
// # What is still NOT live, stated plainly rather than implied
//
// This is real wiring, not a stub, but the pipeline remains inert end-to-end
// for reasons that are upstream of this file:
//
//   - block.RandaoReveals is empty on every real block today, because nothing
//     yet attaches a produced reveal to a proposal (see
//     entropy_reveal_produce.go — the producer exists now; the transport that
//     carries its output into a block does not).
//   - entropyAccumulatorFor depends on SelectEntropyCommittee (Stage A), which
//     needs a live BeaconSource (Stage F). SetBeaconSource still has no
//     production caller, so this fails closed on every real block.
//
// The difference from the previous version is that the failure is now purely a
// wiring gap. There is no longer a missing *mechanism*.
//
// # A wire-field naming wart, deliberately not fixed here
//
// config.Reveal.Secret (json "secret") now carries a 64-byte ed25519
// SIGNATURE, which is public, not a secret. The name is wrong under Decision
// A. It is left alone on purpose: config/ZKBlock.go is consensus-critical, and
// renaming a field there is a wire-format change that belongs with M2a/M2b
// (Architecture §8) so all the block-field changes land as one deliberate
// migration rather than a cosmetic drive-by. Read every use of .Secret in this
// package as "the reveal bytes".
//
// # Ordering hazard, unchanged and still disclosed
//
// By the time the commit hooks below fire, DB_OPs.StoreZKBlock has already
// run — the block is persisted. A Fold failure is logged, not turned into
// retroactive rejection. Moving this into the pre-storage gate
// (blockPropagation.go's validateRemoteBlock, which already runs
// checkBodyBinding and CheckBlockHash before storage) is a separate change
// this pass does not make.
import (
	"sync"

	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/rs/zerolog/log"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
)

// entropyAccumulatorStore is the epoch-keyed store of Accumulators — same
// map+mutex shape as SlotStore/PeriodStore.
//
// There is no longer a paired Round to keep in sync with each Accumulator
// (that was the previous version's epochRound); the verifier is stateless, so
// an Accumulator is the whole per-epoch state.
type entropyAccumulatorStore struct {
	mu   sync.Mutex
	accs map[uint64]*randao.Accumulator
}

var defaultEntropyAccumulatorStore = &entropyAccumulatorStore{
	accs: make(map[uint64]*randao.Accumulator),
}

// revealVerifier is the single shared, stateless Decision-A verifier. It needs
// no configuration: an ed25519 peer ID self-certifies its own public key, so
// there is nothing to inject, register, or snapshot for signature checking.
//
// Membership — WHO was expected to reveal — is a separate question, answered by
// the expected set below, and it carries the same open dependency
// (Architecture §10 decision 2) that every candidate reveal mechanism does.
var revealVerifier randao.RevealVerifier = randao.NewEd25519RevealVerifier()

// entropyAccumulatorFor returns epoch's Accumulator, constructing it on first
// use.
//
// Fails closed if the entropy committee for this epoch cannot be resolved
// (SelectEntropyCommittee, Stage A) — which today means always, since Stage F
// is unbuilt. Constructing against a fabricated stand-in committee would be
// exactly the kind of placeholder this codebase avoids elsewhere.
func entropyAccumulatorFor(epoch uint64) (*randao.Accumulator, error) {
	defaultEntropyAccumulatorStore.mu.Lock()
	defer defaultEntropyAccumulatorStore.mu.Unlock()

	if acc, ok := defaultEntropyAccumulatorStore.accs[epoch]; ok {
		return acc, nil
	}

	members, err := SelectEntropyCommittee(epoch)
	if err != nil {
		return nil, err
	}
	expected := make([]string, 0, len(members))
	for _, m := range members {
		expected = append(expected, m.PeerID)
	}

	// prevSeed stays the zero value, and under Decision A that is now
	// provably harmless rather than a latent bug. Its only consumer anywhere
	// is randao.Fallback(), reached solely from Accumulator.Finalise()'s
	// fallback branch — and finaliseEpoch ALWAYS replaces that branch's seed
	// before returning it (see entropy_finalise.go's fallback selection). No
	// value derived from prevSeed can reach the beacon.
	//
	// If that override is ever removed, this line becomes load-bearing and
	// must source the real previous epoch's entropy first.
	var prevSeed randao.Seed

	acc, err := randao.NewAccumulator(
		BLS_Signer.DomainChainID(), epoch, expected, prevSeed, revealVerifier, randao.Options{},
	)
	if err != nil {
		return nil, err
	}

	defaultEntropyAccumulatorStore.accs[epoch] = acc
	return acc, nil
}

// foldBlockDeclaredReveals folds every reveal `block` declares into its epoch's
// Accumulator. Called once per committed block from the hooks M0.1 added
// (broadcast.go's ProcessBlockLocally, blockPropagation.go's receive path) —
// Rule 2 (Architecture §4.2, "the block declares the set") requires a reveal
// to count only once it is in a committed block, which is exactly when those
// hooks fire.
//
// The epoch comes from block.Slot (set by Block/consensus_fields.go's
// attachAVCConsensusFields), not from live store state, so this stays correct
// regardless of ordering against DefaultSlotStore.AdvanceOnCommit.
func foldBlockDeclaredReveals(block *config.ZKBlock) {
	if len(block.RandaoReveals) == 0 {
		return
	}
	epoch := EpochForSlot(block.Slot)

	acc, err := entropyAccumulatorFor(epoch)
	if err != nil {
		log.Error().Err(err).Uint64("height", block.BlockNumber).Uint64("epoch", epoch).
			Int("reveal_count", len(block.RandaoReveals)).
			Msg("entropy: block declares RandaoReveals but this epoch's Accumulator could not be constructed — reveals dropped, entropy for this epoch unaffected by this block")
		return
	}

	for _, r := range block.RandaoReveals {
		// r.Secret carries the ed25519 signature under Decision A — see this
		// file's note on the field name.
		if err := acc.Fold(block.BlockNumber, r.ProposerID, r.Secret); err != nil {
			log.Error().Err(err).Str("proposer", r.ProposerID).
				Uint64("height", block.BlockNumber).Uint64("epoch", epoch).
				Int("reveal_len", len(r.Secret)).
				Msg("entropy: declared reveal failed to fold (rejected by the ed25519 verifier, or already folded) — block was already persisted by this point, see this file's ordering-hazard note")
		}
	}
}
