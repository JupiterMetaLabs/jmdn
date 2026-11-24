package messaging

// Stage B/C of the M4 pipeline — REWRITTEN 2026-08-20 for Architecture §4.3
// "Decision A": reveals are signatures, not commit-reveal secrets. From the
// chain's entropy cut-over (entropy_cutover.go, 2026-10-22 audit A-2) the
// signature is a BLS signature under the member's committee key
// (entropy_reveal_bls.go). Epochs before the cut-over keep the Ed25519 reveal,
// which a member could grind by choosing its nonce, only so a chain's
// existing history still verifies.
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
// A signature reveal has none of these. It is a deterministic signature over a
// domain-separated, epoch-bound message, so there is no secret, no commit
// phase, and nothing to persist: a node that restarts mid-epoch simply
// re-derives the identical reveal. See avc/randao/bls_reveal_verifier.go and
// ed25519_reveal_verifier.go.
//
// # When this is live
//
// Only under Stage 2. Sequencer.InstallAVCBeaconFromEnv installs the beacon
// (SetBeaconSource); without it entropyAccumulatorFor fails closed with
// ErrNoBeaconInstalled. With it, a proposer fills block.RandaoReveals from the
// reveal inbox (Block/consensus_fields.go → RevealsForBlock), and every node
// folds them here on commit, sync and replay (entropy_block_effects.go). From
// the entropy cut-over, an epoch whose committee pool is not chain-anchored
// has no verifier and takes the fallback (entropy_reveal_bls.go).
//
// # A wire-field naming wart, deliberately not fixed here
//
// config.Reveal.Secret (json "secret") now carries a 64-byte SIGNATURE
// (Ed25519 before the cut-over, BLS from it), which is public, not a secret. The name is wrong under Decision
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

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/rs/zerolog/log"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
)

// entropyAccumulatorStore is the epoch-keyed store of Accumulators — same
// map+mutex shape as SlotStore/ProposerPeriods.
//
// There is no longer a paired Round to keep in sync with each Accumulator
// (that was the previous version's epochRound); the verifier is stateless, so
// an Accumulator is the whole per-epoch state.
//
// newest/seeded bound accs the same way mixStore bounds its own map (D-47):
// before this, nothing ever removed an entry in normal operation — only
// RunEntropyStartupRecovery's replay reset cleared entries, and only on that
// one resync path — so accs grew by one entry per epoch forever.
type entropyAccumulatorStore struct {
	mu     sync.Mutex
	accs   map[uint64]*randao.Accumulator
	newest uint64
	seeded bool
}

var defaultEntropyAccumulatorStore = &entropyAccumulatorStore{
	accs: make(map[uint64]*randao.Accumulator),
}

// ed25519RevealVerifier checks the reveals of epochs BEFORE the entropy
// cut-over (entropy_cutover.go). It needs no configuration: an ed25519 peer ID
// self-certifies its own public key. From the cut-over on it is never used:
// revealVerifierFor picks the BLS verifier by epoch.
var ed25519RevealVerifier randao.RevealVerifier = randao.NewEd25519RevealVerifier()

// entropyAccumulatorFor returns epoch's Accumulator, constructing it on first
// use.
//
// Fails closed if the epoch's entropy committee cannot be resolved: no beacon
// installed, or - from the cut-over - no chain-anchored pool for the epoch
// (blsRevealCommittee). Constructing against a fabricated stand-in committee
// would be exactly the kind of placeholder this codebase avoids elsewhere.
func entropyAccumulatorFor(epoch uint64) (*randao.Accumulator, error) {
	defaultEntropyAccumulatorStore.mu.Lock()
	defer defaultEntropyAccumulatorStore.mu.Unlock()

	if acc, ok := defaultEntropyAccumulatorStore.accs[epoch]; ok {
		return acc, nil
	}

	// From the cut-over, reveals are BLS signatures under each member's
	// snapshot-bound key, so the committee must come from the anchored pool:
	// then the expected set and the members' keys are the same on every node
	// (entropy_reveal_bls.go). Such an epoch with no anchored pool returns
	// ErrBLSRevealNeedsAnchoredPool, which decideEpoch turns into the
	// deterministic fallback. Before the cut-over the epoch keeps the rules it
	// was revealed under: the Ed25519 verifier and SelectEntropyCommittee.
	var members []committee.Member
	var verifier randao.RevealVerifier
	var err error
	if afterEntropyCutover(epoch) {
		if members, err = blsRevealCommittee(epoch); err != nil {
			return nil, err
		}
		if verifier, err = blsRevealVerifierFor(epoch, members); err != nil {
			return nil, err
		}
	} else {
		if members, err = SelectEntropyCommittee(committee.EntropyEpoch(epoch)); err != nil {
			return nil, err
		}
		verifier = ed25519RevealVerifier
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
		BLS_Signer.DomainChainID(), epoch, expected, prevSeed, verifier, randao.Options{},
	)
	if err != nil {
		return nil, err
	}

	defaultEntropyAccumulatorStore.accs[epoch] = acc
	if !defaultEntropyAccumulatorStore.seeded || epoch > defaultEntropyAccumulatorStore.newest {
		defaultEntropyAccumulatorStore.newest = epoch
		defaultEntropyAccumulatorStore.seeded = true
	}
	// D-47: bound accs the same way mixStore bounds mixes — same retention
	// window, since both are per-epoch state for the same recent window a
	// lagging peer's boundary block still needs to be verifiable within.
	if retain := mixRetainEpochs(); defaultEntropyAccumulatorStore.newest >= retain {
		cutoff := defaultEntropyAccumulatorStore.newest - retain
		for e := range defaultEntropyAccumulatorStore.accs {
			if e < cutoff {
				delete(defaultEntropyAccumulatorStore.accs, e)
			}
		}
	}
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
		// r.Secret carries the member's reveal signature (Ed25519 before the
		// cut-over, BLS from it) — see this file's note on the field name.
		if err := acc.Fold(block.BlockNumber, r.ProposerID, r.Secret); err != nil {
			log.Error().Err(err).Str("proposer", r.ProposerID).
				Uint64("height", block.BlockNumber).Uint64("epoch", epoch).
				Int("reveal_len", len(r.Secret)).
				Msg("entropy: declared reveal failed to fold (rejected by the epoch's reveal verifier, or already folded) — block was already persisted by this point, see this file's ordering-hazard note")
		}
	}
}
