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
// # Where the signers come from (blocker B1, resolved)
//
// §4.2a assumed aggSig was already stored; it was not (verified 2026-08-20),
// so this collector once had no input and FallbackSeedForEpoch failed closed
// on every call. That gap is closed: a block inside a fold window carries its
// parent's buddy-committee certificate (config.ZKBlock.PrevAggCert, covered by
// the block hash), and VerifyAndRecordPrevCert (entropy_aggsig.go), run from
// the same commit hooks as foldBlockDeclaredReveals, verifies it, derives the
// aggregate locally and records it here via RecordAggSigForFallback.
// RecoverAggSigStoreAtStartup rebuilds the store from committed blocks after a
// restart.
//
// It requires consensus.flags.agg_cert (JMDN_AVC_AGG_CERT), ON by default and
// fleet-wide, and the Stage-2 beacon refuses to install without it. With it
// off nothing is recorded and FallbackSeedForEpoch still fails closed - never
// falling through to some other formula, which would ship a wrong seed into
// the next epoch's committee draw.
import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/JupiterMetaLabs/avc/committee"
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
	errVDFRecoveryDeadlineZero = errors.New(
		"messaging: VDFProofRecoveryDeadlineSlots must be >= 1; a deadline of 0 never fires")

	errVDFRecoveryDeadlineTooEarly = errors.New(
		"messaging: VDFProofRecoveryDeadlineSlots exceeds N-RevealCutoffK; the deadline would fall " +
			"before the epoch's mix is finalised, so no peer could hold a proof yet")

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
// Called by VerifyAndRecordPrevCert (entropy_aggsig.go) on every committed
// block, with the parent certificate that block carries, once verified.
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

	collected := collectedAggSigs(start, deadline)

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

// collectedAggSigs returns the recorded aggregates in [start, deadline), lowest
// slot first, at most FallbackFoldBufferB of them.
func collectedAggSigs(start, deadline uint64) []randao.AggSig {
	defaultAggSigStore.mu.Lock()
	defer defaultAggSigStore.mu.Unlock()
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
	return collected
}

// domainFallbackLastResort separates the no-signer last-resort seed from every
// other hash in the entropy pipeline.
const domainFallbackLastResort = "jmdt/randao-fallback-last-resort/v1"

// ErrLastResortNoEntropy means an epoch reached its fallback deadline with no
// signer collected and this node does not hold the epoch's entropy, so not
// even the last-resort seed can be derived. The epoch stays without a seed.
var ErrLastResortNoEntropy = errors.New("messaging: last-resort fallback needs the epoch's entropy, which this node does not hold")

// LastResortSeedForEpoch derives the seed for an epoch whose fallback reached
// its deadline with fewer than FallbackFoldBufferB signers
// (ErrFallbackDeadlineExceeded).
//
// Before this, such an epoch produced no seed at all: no mix, so no VDF, so
// the next epoch's boundary block could never carry a proof
// (ErrVDFProofNotReady) and the chain halted for good. Members withholding
// reveals, plus a window short of certificates, could therefore halt the chain,
// not just bias it. Now:
//
//   - 1 to B-1 signers collected: fold exactly those, with the same fold and
//     the same lowest-slot-first selection as the full fallback.
//   - none collected: H(domain, chainID, epoch, ENTROPY(epoch)). ENTROPY(epoch)
//     is the value that seated this epoch's committees, fixed by the VDF before
//     the window opened, so no proposer can choose it.
//   - none collected and this node does not hold ENTROPY(epoch) (Stage 1, or
//     it never adopted the epoch): ErrLastResortNoEntropy, as before.
//
// Deterministic across the fleet: the signers are the certificates committed
// blocks carried, recorded before the epoch is resolved
// (applyEntropyEffectsLocked runs VerifyAndRecordPrevCert first), and the
// replay after a restart runs the same code over the same blocks.
//
// The trade-off: with fewer signers a dishonest sequencer has fewer terms to
// beat, so the seed is easier to bias than a full fold. It also controls the
// count: by leaving PrevAggCert out of window blocks it picks between 0 and B
// signers (and which ones), and with a short VDF it can evaluate every option
// before publishing. Before this, leaving them out halted the chain, which
// hurt the sequencer too. Acceptable with one trusted sequencer; revisit T
// before running several. That is the price of not halting, and only paid
// when the window was already short.
func LastResortSeedForEpoch(epoch uint64) (randao.Seed, error) {
	start, deadline, err := randao.FallbackCollectionBounds(epoch, N, RevealCutoffK, FallbackFoldMaxSlotOffset)
	if err != nil {
		return randao.Seed{}, err
	}
	if collected := collectedAggSigs(start, deadline); len(collected) > 0 {
		return randao.FallbackFromCommittedSigners(BLS_Signer.DomainChainID(), epoch, start, deadline, uint64(len(collected)), collected)
	}

	beacon := activeBeacon()
	if beacon == nil {
		return randao.Seed{}, fmt.Errorf("%w: epoch %d (no beacon installed)", ErrLastResortNoEntropy, epoch)
	}
	entropy, err := beacon.EpochEntropy(committee.EntropyEpoch(epoch))
	if err != nil {
		return randao.Seed{}, fmt.Errorf("%w: epoch %d: %v", ErrLastResortNoEntropy, epoch, err)
	}
	return lastResortHash(BLS_Signer.DomainChainID(), epoch, entropy), nil
}

func lastResortHash(chainID, epoch uint64, entropy []byte) randao.Seed {
	var buf [8]byte
	h := sha256.New()
	h.Write([]byte(domainFallbackLastResort))
	binary.BigEndian.PutUint64(buf[:], chainID)
	h.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], epoch)
	h.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], uint64(len(entropy)))
	h.Write(buf[:])
	h.Write(entropy)
	var out randao.Seed
	copy(out[:], h.Sum(nil))
	return out
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
