package messaging

// The [K, K+B) fallback fold window — Architecture §4.2a as amended by the
// M4-1 finding of 2026-08-20. New file.
//
// # What this is for
//
// When an epoch cannot use its XOR mix (Rule 1: any expected member did not
// reveal), the seed comes from folding the buddy-committee BLS aggregate
// certificate of every block committed in a narrow window just after the
// reveal cutoff. avc/randao/fallback_aggsig.go holds the pure fold; this file
// holds the per-node collection of the inputs.
//
// # Why the window is [K, K+B) and not the whole epoch
//
// §4.2a originally folded every block in the epoch. That cannot coexist with
// §4.5/§7.2's requirement that Finalise() run at the cutoff slot E*N+K: the
// later blocks do not exist yet at that moment, so a fallback epoch would have
// had to wait until its final slot, leaving the VDF ~no runway precisely in the
// already-degraded case. Starting the window AT the cutoff also keeps the
// reveal/withhold decision blind — zero window blocks exist when that decision
// is made.
//
// # THIS PATH CANNOT RUN TODAY — blocker B1, and that is deliberate
//
// §4.2a describes aggSig as already-present, zero-new-storage data. Verified
// 2026-08-20 that this is not true at rest: no certificate or signer-bitmap
// field exists on config.ZKBlock, and the aggregate is a transient local
// variable discarded once a block verifies. Nothing can supply this collector,
// so FallbackSeedForEpoch fails closed on every call.
//
// That failure is the correct behaviour, not a TODO. The alternative — quietly
// falling through to some other formula — is what would ship a wrong seed into
// the next epoch's committee draw. Persisting aggSig as a hash-covered block
// field is a wire-format change that belongs with M2b (Architecture §8, §10
// decision 10), and RecordAggSigForFallback below is the exact seam it plugs
// into when it lands: call it from the same commit hooks
// foldBlockDeclaredReveals already uses, passing the verified certificate.
import (
	"errors"
	"fmt"
	"sync"

	"github.com/JupiterMetaLabs/avc/randao"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// RevealCutoffK is K — Architecture §7.2's reveal-window length in slots.
// Reveals are accepted only in the first K slots of an epoch; Finalise() runs
// at slot E*N+K. Closed value from AVC-Low-Level-Design.md §1.
const RevealCutoffK = 3

// FallbackFoldBufferB is B — how many slots after the cutoff the fallback fold
// window spans (Architecture §10 decision 11, new 2026-08-20).
//
// Upper bound, derived from §7.2's own liveness rule (T_vdf <= (N-K-B)/2 *
// s_min) at the adopted N=50, K=3, s_min=60s, T_vdf=1200s:
//
//	B <= N - K - 2*T_vdf/s_min = 50 - 3 - 40 = 7
//
// Lower bound: B must span more than one proposer's turn, or a single actor
// controls every term in the fold and the multi-contributor property the fold
// exists to provide collapses. That bound depends on the proposer-rotation
// period (task #8), which is not yet scoped — so it is NOT yet possible to
// prove B=5 satisfies it.
//
// B=5 is chosen inside the proven ceiling with two slots of margin.
// ValidateFallbackWindowParams below enforces the ceiling at startup; the
// lower bound is stated here and must be re-checked once task #8 lands.
const FallbackFoldBufferB = 5

var (
	// ErrAggSigUnavailable means the window's aggregate certificates are not
	// obtainable — today, always, because they are never persisted (B1).
	ErrAggSigUnavailable = errors.New("messaging: BLS aggregate certificates are not persisted on blocks (blocker B1), so Architecture §4.2a's fallback seed cannot be computed")
)

// aggSigStore collects per-slot aggregate certificates for fallback folding.
//
// Keyed by absolute slot, not by epoch: a slot identifies its epoch
// (slot / N), and keying by slot keeps the entry usable no matter which order
// blocks arrive in.
type aggSigStore struct {
	mu   sync.Mutex
	sigs map[uint64][]byte
}

var defaultAggSigStore = &aggSigStore{sigs: make(map[uint64][]byte)}

// RecordAggSigForFallback records one committed block's verified BLS aggregate
// certificate against its slot.
//
// NOTHING CALLS THIS YET, and that is the whole of blocker B1. When aggSig
// becomes a persisted, hash-covered block field (M2b), call this from the same
// two commit hooks foldBlockDeclaredReveals uses — broadcast.go's
// ProcessBlockLocally and blockPropagation.go's receive path — with the
// certificate that was just verified for that block.
//
// Rejects a malformed certificate rather than storing it: a wrong-length entry
// would fail the fold later, at finalisation, where the cause would be far
// harder to attribute back to the block that carried it.
func RecordAggSigForFallback(slot uint64, aggSig []byte) error {
	if len(aggSig) != randao.AggSigLen {
		return fmt.Errorf("%w: slot %d has a %d-byte aggregate, want %d",
			randao.ErrBadAggSig, slot, len(aggSig), randao.AggSigLen)
	}
	cp := make([]byte, len(aggSig))
	copy(cp, aggSig)

	defaultAggSigStore.mu.Lock()
	defaultAggSigStore.sigs[slot] = cp
	defaultAggSigStore.mu.Unlock()
	return nil
}

// ValidateFallbackWindowParams checks that the compiled-in N, K and B are
// mutually consistent — that the window closes strictly inside the epoch and
// leaves the runway the narrowing was introduced to restore.
//
// Call at startup. Getting this wrong is a silent liveness bug: the fold window
// would overrun the epoch and a fallback epoch would finalise with no VDF
// runway, which is the exact defect M4-1 identified.
func ValidateFallbackWindowParams() error {
	if _, _, err := randao.FallbackWindow(0, N, RevealCutoffK, FallbackFoldBufferB); err != nil {
		return fmt.Errorf("messaging: N=%d/K=%d/B=%d are not a usable fallback window: %w",
			N, RevealCutoffK, FallbackFoldBufferB, err)
	}
	return nil
}

// FallbackSeedForEpoch computes epoch's fallback seed per Architecture §4.2a,
// folding the aggregate certificates from slots [E*N+K, E*N+K+B).
//
// Fails closed — and today always fails — when any slot in the window has no
// recorded certificate. A partial window is never folded: allowing that would
// hand whoever caused the gap a choice among window subsets, the same
// subset-menu problem Rule 1 closes one level up.
func FallbackSeedForEpoch(epoch uint64) (randao.Seed, error) {
	start, end, err := randao.FallbackWindow(epoch, N, RevealCutoffK, FallbackFoldBufferB)
	if err != nil {
		return randao.Seed{}, err
	}

	defaultAggSigStore.mu.Lock()
	collected := make([]randao.AggSig, 0, end-start)
	missing := make([]uint64, 0)
	for slot := start; slot < end; slot++ {
		sig, ok := defaultAggSigStore.sigs[slot]
		if !ok {
			missing = append(missing, slot)
			continue
		}
		collected = append(collected, randao.AggSig{Slot: slot, Sig: sig})
	}
	defaultAggSigStore.mu.Unlock()

	if len(missing) > 0 {
		return randao.Seed{}, fmt.Errorf("%w: epoch %d window [%d,%d) is missing slots %v",
			ErrAggSigUnavailable, epoch, start, end, missing)
	}

	return randao.FallbackFromAggSigs(BLS_Signer.DomainChainID(), epoch, start, end, collected)
}

// pruneAggSigsBelow drops entries for slots before the given slot. Called after
// an epoch finalises so the store does not grow without bound; the window for
// any future epoch starts at a strictly higher slot than the one that just
// closed.
func pruneAggSigsBelow(slot uint64) {
	defaultAggSigStore.mu.Lock()
	for s := range defaultAggSigStore.sigs {
		if s < slot {
			delete(defaultAggSigStore.sigs, s)
		}
	}
	defaultAggSigStore.mu.Unlock()
}
