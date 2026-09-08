package messaging

// The slot-based recovery deadline: when a node should stop waiting for its
// own VDF and start asking peers for the proof.
//
// # What D is, and what it is NOT
//
// D is ONLY the proof-RECOVERY deadline. It does not delay, gate, or otherwise
// influence when VDF evaluation STARTS. Evaluation begins the moment the mix
// is finalised (onEpochFinalised -> VDFSealer.Start) and continues regardless
// of anything in this file. Recovery is ADDITIVE:
//
//	mix_E finalised -> every node may evaluate -> first VALID proof wins
//	                                           -> everyone verifies and adopts
//	and independently:
//	still no entropy_E at the deadline -> ask peers -> verify -> adopt
//
// If recovery fails, local evaluation carries on and may still finish. Nothing
// here can leave a node with no path to the epoch.
//
// # Why it is expressed in SLOTS and not seconds
//
// A slot is one completed consensus round (SlotStore.AdvanceOnCommit does
// slot += period+1); there is no wall clock in it, and a round that burned
// timeouts advances the counter without doing more work. Deriving this
// deadline from an estimate of VDF runtime would therefore be both unreliable
// AND would make a consensus-adjacent decision depend on machine speed. It
// deliberately does neither: the node does not PREDICT whether it will finish,
// it OBSERVES that it has not, at a slot every node computes identically.

import (
	"sync"

	"github.com/rs/zerolog/log"

	"gossipnode/config"
)

// VDFProofRecoveryDeadlineSlots is D: how many slots before an epoch's
// boundary a node with no entropy for that epoch starts asking peers.
//
// VALID RANGE: 1 <= D <= N - RevealCutoffK.
//
// The upper bound is not a style preference, it is arithmetic. The mix for
// epoch E is finalised at cutoffSlotFor(E-1) = E*N - (N-K). Before that
// instant no node can possibly hold a proof for E, so a deadline earlier than
// it would send every node asking for something nobody has, burning requests
// and backoff on a guaranteed miss. ValidateVDFRecoveryParams enforces it.
//
// D = 5 is deliberately near the late end of the range: local evaluation gets
// almost the whole runway before recovery is attempted, which keeps requests
// rare, while 5 slots still leaves several consensus rounds for a round trip
// and verification before the boundary block needs the proof.
const VDFProofRecoveryDeadlineSlots uint64 = 5

// ValidateVDFRecoveryParams checks D against the epoch constants.
//
// Call at startup, alongside ValidateFallbackWindowParams and
// ValidateVDFTimingParams. Getting this wrong is a silent liveness bug: an
// out-of-range D produces a deadline that either never fires or fires before
// any proof can exist, and neither shows up as an error at the point of use.
func ValidateVDFRecoveryParams() error {
	if VDFProofRecoveryDeadlineSlots == 0 {
		return errVDFRecoveryDeadlineZero
	}
	if VDFProofRecoveryDeadlineSlots > N-RevealCutoffK {
		return errVDFRecoveryDeadlineTooEarly
	}
	return nil
}

// VDFRecoveryDeadlineSlot returns the slot at or after which a node lacking
// entropy for forEpoch should begin recovery.
func VDFRecoveryDeadlineSlot(forEpoch uint64) uint64 {
	boundary := EpochBoundarySlot(forEpoch)
	if boundary < VDFProofRecoveryDeadlineSlots {
		return 0
	}
	return boundary - VDFProofRecoveryDeadlineSlots
}

// VDFRecoveryTargetEpoch returns the epoch whose entropy a node standing at
// currentSlot should be worrying about.
//
// IT IS THE NEXT EPOCH, NOT THE CURRENT ONE, and this is the single easiest
// thing to get wrong here. deadline(E) = E*N - D with D <= N, so the deadline
// slot always falls inside epoch E-1. A node at slot 393 with N=50 is in epoch
// 7 and is preparing entropy for epoch 8. Checking EpochForSlot(currentSlot)
// would silently interrogate an epoch whose entropy is already settled, and
// the mechanism would appear to work while never firing.
func VDFRecoveryTargetEpoch(currentSlot uint64) uint64 {
	return EpochForSlot(currentSlot) + 1
}

// VDFProofRecoveryDispatcher launches recovery for an epoch. Installed at
// startup by whichever component owns the libp2p host and the peer set;
// messaging cannot reach either directly.
//
// The implementation MUST return immediately and do its work on a background
// goroutine — see maybeTriggerVDFProofRecovery.
type VDFProofRecoveryDispatcher func(forEpoch uint64, boundarySlot uint64)

var (
	vdfRecoveryDispatcherMu sync.Mutex
	vdfRecoveryDispatcher   VDFProofRecoveryDispatcher
)

// SetVDFProofRecoveryDispatcher installs the dispatcher. Call once at startup.
//
// Guarded for exactly the reason SetVDFProofAcceptor and SetSealerCanceller
// are: the write happens once on the startup goroutine, and the read happens
// on every block-application goroutine. That is a data race whether or not a
// test has yet been pointed at it with -race, and an unsynchronised read of an
// interface value can observe a half-written word, not merely a stale one.
func SetVDFProofRecoveryDispatcher(f VDFProofRecoveryDispatcher) {
	vdfRecoveryDispatcherMu.Lock()
	vdfRecoveryDispatcher = f
	vdfRecoveryDispatcherMu.Unlock()
}

func activeVDFRecoveryDispatcher() VDFProofRecoveryDispatcher {
	vdfRecoveryDispatcherMu.Lock()
	defer vdfRecoveryDispatcherMu.Unlock()
	return vdfRecoveryDispatcher
}

// maybeTriggerVDFProofRecovery is the deadline check.
//
// CALLED FROM THE BLOCK PATH, AND THEREFORE DOES NO I/O. It reads three
// in-memory values and, at most, hands an epoch number to the dispatcher. It
// never dials a peer, never waits on a response, never verifies a proof and
// never calls Pipeline.Accept. All of that happens on the dispatcher's own
// goroutine.
//
// LIVE PATH ONLY. It is deliberately NOT called from RecordSyncedBlockEntropy:
// a node replaying thousands of blocks during catch-up crosses many epoch
// boundaries, and firing here would enqueue one recovery per replayed
// boundary — mostly for epochs whose mix is long past its retention window, so
// every one of them would fail closed after a full round trip. A rejoining
// node recovers once, after catch-up, for the epoch it actually needs.
func maybeTriggerVDFProofRecovery(block *config.ZKBlock) {
	if block == nil || block.Slot == 0 {
		return
	}
	// Stage 1: no beacon installed means no Stage-2 entropy to be missing.
	beacon := activeBeacon()
	if beacon == nil {
		return
	}
	dispatch := activeVDFRecoveryDispatcher()
	if dispatch == nil {
		return
	}

	targetEpoch := VDFRecoveryTargetEpoch(block.Slot)
	if beacon.Has(targetEpoch) {
		return // already have it — nothing to recover
	}
	if block.Slot < VDFRecoveryDeadlineSlot(targetEpoch) {
		return // still inside the local-evaluation runway
	}

	log.Debug().
		Uint64("current_slot", block.Slot).
		Uint64("target_epoch", targetEpoch).
		Uint64("deadline_slot", VDFRecoveryDeadlineSlot(targetEpoch)).
		Msg("entropy: recovery deadline reached without entropy — dispatching an asynchronous " +
			"proof request (local VDF evaluation is unaffected and continues)")

	dispatch(targetEpoch, EpochBoundarySlot(targetEpoch))
}
