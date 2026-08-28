package messaging

// Count-based fallback signer collection — Architecture §4.2a as amended by
// the M4-1 finding of 2026-08-20 and the count-based-collection amendment of
// 2026-08-24. New file.
//
// # What this is for
//
// When an epoch cannot use its XOR mix (Rule 1: any expected member did not
// reveal), the seed comes from folding the buddy-committee BLS aggregate
// certificates of FallbackFoldBufferB (=5) committed blocks after the reveal
// cutoff, skipping any slot whose round timed out. avc/randao/fallback_aggsig.go
// holds the pure fold; this file holds the per-node collection of the inputs.
//
// # Why collection starts at K and stops after B signers, not a fixed range
//
// §4.2a originally folded every block in the epoch. That cannot coexist with
// §4.5/§7.2's requirement that Finalise() run at the cutoff slot E*N+K: the
// later blocks do not exist yet at that moment, so a fallback epoch would have
// had to wait until its final slot, leaving the VDF ~no runway precisely in
// the already-degraded case. Starting collection AT the cutoff also keeps the
// reveal/withhold decision blind — zero signers exist when that decision is
// made.
//
// The 2026-08-24 amendment: the original design required a certificate at
// EVERY slot in a fixed [K, K+B) range — one timed-out round anywhere inside
// it made the fold permanently uncomputable for the whole epoch (a halt
// vector). B is a count of required signers, not a slot range width; slots
// with no committed block (timeouts) are simply skipped, and collection
// widens into FallbackFoldMaxSlotOffset — a separate, independently-derived
// slot deadline — until either B signers are found or the deadline passes.
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
	"sort"
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
//
// B is a COUNT, not a slot number — deliberately. Renaming it to a slot
// boundary (i.e. "collect until slot K+B, however many signers that holds")
// reintroduces the exact halt bug this design amends: a single timeout inside
// a B-wide slot range leaves the fold short. See FallbackFoldMaxSlotOffset
// below for the slot quantity this design actually needs.
const FallbackFoldBufferB = 5

// FallbackFoldMaxSlotOffset bounds how far past the cutoff K collection may
// run before giving up — the LIVENESS half of the design; FallbackFoldBufferB
// above is the SECURITY half (how many signers are required). The two are
// independent numbers on purpose: conflating them (treating B itself as a
// slot boundary) is the halt bug named in FallbackFoldBufferB's comment.
//
// Derived from the same liveness rule that bounds B (T_vdf <= (n-k-maxOffset)/2
// * s_min) at the adopted N=50, K=3, s_min=60s, T_vdf=1200s:
//
//	maxOffset <= N - K - 2*T_vdf/s_min = 50 - 3 - 40 = 7
//
// Set to the ceiling itself: with FallbackFoldBufferB=5, this gives up to 2
// timed-out slots of tolerance while collecting the 5 required signers.
const FallbackFoldMaxSlotOffset = 7

var (
	// ErrAggSigUnavailable means the window's aggregate certificates are not
	// obtainable — today, always, because they are never persisted (B1).
	ErrAggSigUnavailable = errors.New("messaging: BLS aggregate certificates are not persisted on blocks (blocker B1), so Architecture §4.2a's fallback seed cannot be computed")

	// ErrFallbackNotYetReady means fewer than FallbackFoldBufferB signers have
	// been collected, but FallbackFoldMaxSlotOffset has not been reached
	// either. The caller must keep the epoch pending and retry on the next
	// committed block — see entropy_finalise.go's resolvePendingFallbacks.
	ErrFallbackNotYetReady = errors.New("messaging: fallback signer collection not yet complete")

	// ErrFallbackDeadlineExceeded means the collection deadline slot passed
	// with fewer than FallbackFoldBufferB signers collected. The epoch
	// produces no seed, permanently — there is no later point at which
	// retrying could still help, since doing so would mean using signers from
	// slots the liveness bound has already ruled unsafe to wait for.
	ErrFallbackDeadlineExceeded = errors.New("messaging: fallback collection deadline exceeded before enough signers were collected")
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
	if _, _, err := randao.FallbackCollectionBounds(0, N, RevealCutoffK, FallbackFoldMaxSlotOffset); err != nil {
		return fmt.Errorf("messaging: N=%d/K=%d/MaxOffset=%d are not a usable fallback collection range: %w",
			N, RevealCutoffK, FallbackFoldMaxSlotOffset, err)
	}
	if FallbackFoldMaxSlotOffset < FallbackFoldBufferB {
		return fmt.Errorf("messaging: MaxOffset=%d is smaller than B=%d — even zero timeouts could never collect enough signers before the deadline",
			FallbackFoldMaxSlotOffset, FallbackFoldBufferB)
	}
	return nil
}

// FallbackSeedForEpoch attempts epoch's fallback seed at currentSlot.
//
// Three outcomes, and the caller (entropy_finalise.go) must handle all three
// distinctly:
//   - enough signers collected: returns the seed.
//   - fewer than FallbackFoldBufferB collected, FallbackFoldMaxSlotOffset not
//     yet reached: returns ErrFallbackNotYetReady. The epoch stays pending;
//     call again on the next committed block.
//   - fewer collected, deadline reached: returns ErrFallbackDeadlineExceeded.
//     The epoch produces no seed, and must not be retried again.
//
// This replaces the old all-or-nothing FallbackSeedForEpoch(epoch), which
// required a certificate at every slot in a fixed B-wide window — a single
// missing slot (whether from a timeout or from simply not having reached that
// slot yet) was indistinguishable from a permanent failure. Distinguishing
// "not there yet" from "never coming" is what makes the two-phase pending
// design in entropy_finalise.go possible.
func FallbackSeedForEpoch(epoch, currentSlot uint64) (randao.Seed, error) {
	start, deadline, err := randao.FallbackCollectionBounds(epoch, N, RevealCutoffK, FallbackFoldMaxSlotOffset)
	if err != nil {
		return randao.Seed{}, err
	}

	defaultAggSigStore.mu.Lock()
	slots := make([]uint64, 0, len(defaultAggSigStore.sigs))
	for s := range defaultAggSigStore.sigs {
		if s >= start && s < deadline {
			slots = append(slots, s)
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })

	collected := make([]randao.AggSig, 0, FallbackFoldBufferB)
	for _, s := range slots {
		if uint64(len(collected)) == FallbackFoldBufferB {
			break
		}
		collected = append(collected, randao.AggSig{Slot: s, Sig: defaultAggSigStore.sigs[s]})
	}
	defaultAggSigStore.mu.Unlock()

	if uint64(len(collected)) == FallbackFoldBufferB {
		return randao.FallbackFromCommittedSigners(BLS_Signer.DomainChainID(), epoch, start, deadline, FallbackFoldBufferB, collected)
	}
	if currentSlot < deadline {
		return randao.Seed{}, fmt.Errorf("%w: epoch %d has %d of %d signers, deadline slot %d (currentSlot=%d)",
			ErrFallbackNotYetReady, epoch, len(collected), FallbackFoldBufferB, deadline, currentSlot)
	}
	return randao.Seed{}, fmt.Errorf("%w: epoch %d reached slot %d (deadline %d) with only %d of %d signers",
		ErrFallbackDeadlineExceeded, epoch, currentSlot, deadline, len(collected), FallbackFoldBufferB)
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
