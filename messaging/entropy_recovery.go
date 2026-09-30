package messaging

// Restart recovery for the entropy pipeline: RANDAO reveals, mix(e), the
// fallback aggregate window, the decided-epoch watermark and Stage-E sealing.
//
// # What a restart used to lose
//
// Everything below lived only in process memory:
//
//   - the per-epoch Accumulators (reveals folded from committed blocks),
//   - defaultMixStore (mix(e), the VDF input for ENTROPY-(e+1) and the
//     independent half of every inbound proof check),
//   - the decided-epoch watermark (lastDecidedEpoch / haveDecidedAny),
//   - pendingFallback.
//
// After a restart the watermark read "nothing decided", so the first live block
// re-decided every epoch from 0. For the epoch in progress that meant:
//
//   - restart INSIDE the reveal window: the reveals committed before the restart
//     were gone, the epoch finalised "fallback" on this node while its peers
//     finalised "mixed" (or a different fallback subset) - a DIFFERENT mix, a
//     different ENTROPY, and every honest peer proof rejected;
//   - restart after the cutoff: mix(e) was gone, so this node could neither
//     seal ENTROPY-(e+1) nor verify anyone's proof for it. On the sequencer
//     that stalls the chain at the next epoch boundary.
//
// # The fix, in three parts
//
//  1. Replay (ReplayEntropyFromChain). mix(e) is a pure function of committed
//     blocks: the randao_reveals of e's window blocks, or the aggregate-signature
//     fallback over committed prev_agg_cert. At startup the node re-applies the
//     stored blocks of epochs eTip-1 and eTip, in height order, through the SAME
//     code the live path runs (fold, prev-cert, decide, VDF-accept), with the
//     watermark reset to eTip-2. Same inputs, same order, same function: the
//     same mix the fleet derived.
//
//  2. Durable mix record (DB_OPs.RecordEntropyMix, first writer wins). Written
//     at every finalisation, restored BEFORE the replay. It covers what replay
//     cannot rebuild on its own (e.g. a predecessor epoch whose entropy aged out
//     of the beacon) and doubles as a consistency check: when replay derives a
//     different value, the retained record wins (notifyEpochFinalised's existing
//     rule) and the disagreement is logged as an error.
//
//  3. Sealing resumption. Finalisations made while replaying or syncing do NOT
//     start VDF evaluations. When replay ends, the newest decided epoch is handed
//     to the Stage-E hook once; Sequencer's onEpochFinalised then reuses a
//     persisted proof (fast verify) or skips an epoch whose entropy is already
//     published and whose boundary block is already committed, and only seals
//     when the work is genuinely outstanding.
//
// # Startup window
//
// Stream handlers exist from node.NewNode() on, but replay needs the committee
// eligibility source, which main() wires much later. ArmEntropyRecovery closes
// that window: while armed, blocks are still folded (reveals, certificates,
// proofs) but no epoch is DECIDED, because a decision on a half-rebuilt
// accumulator is exactly the divergence above. RunEntropyStartupRecovery always
// disarms; a timer disarms too, so a wiring bug degrades to the old behaviour
// with a loud error rather than halting entropy forever.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

// ---------------------------------------------------------------------------
// Serialisation and modes
// ---------------------------------------------------------------------------

// entropyEffectsMu serialises every application of a block's entropy effects:
// the live receive path, the sequencer's local commit, the sync path and the
// startup replay. The steps inside (fold, then decide) are order-sensitive and
// the two live hooks were not serialised against each other.
var entropyEffectsMu sync.Mutex

var (
	// entropyReplayActive is true only while ReplayEntropyFromChain runs.
	entropyReplayActive atomic.Bool
	// entropyQuietFinalise suppresses the Stage-E hook for finalisations made
	// while replaying or syncing (they must not launch VDF evaluations).
	entropyQuietFinalise atomic.Bool
)

// PublishedEntropyFor returns the entropy this node's beacon holds for epoch.
func PublishedEntropyFor(epoch uint64) ([]byte, bool) {
	b := activeBeacon()
	if b == nil {
		return nil, false
	}
	v, err := b.EpochEntropy(committee.EntropyEpoch(epoch))
	if err != nil || len(v) == 0 {
		return nil, false
	}
	return v, true
}

// ---------------------------------------------------------------------------
// Startup gate
// ---------------------------------------------------------------------------

var (
	entropyRecoveryArmed atomic.Bool
	entropyArmTimerMu    sync.Mutex
	entropyArmTimer      *time.Timer
)

// DefaultEntropyRecoveryArmTimeout bounds how long decisions stay paused if
// RunEntropyStartupRecovery is never reached.
const DefaultEntropyRecoveryArmTimeout = 10 * time.Minute

// ArmEntropyRecovery pauses epoch DECISIONS (not folding) until
// RunEntropyStartupRecovery has rebuilt the entropy state. Call once at
// startup, before any stream handler can deliver a block.
func ArmEntropyRecovery(timeout time.Duration) {
	if timeout <= 0 {
		timeout = DefaultEntropyRecoveryArmTimeout
	}
	entropyRecoveryArmed.Store(true)
	entropyArmTimerMu.Lock()
	if entropyArmTimer != nil {
		entropyArmTimer.Stop()
	}
	entropyArmTimer = time.AfterFunc(timeout, func() {
		if entropyRecoveryArmed.CompareAndSwap(true, false) {
			log.Error().Dur("timeout", timeout).
				Msg("entropy recovery: startup replay never ran - resuming epoch decisions WITHOUT a rebuilt " +
					"accumulator. This node may finalise the current epoch differently from its peers; " +
					"investigate the startup wiring")
		}
	})
	entropyArmTimerMu.Unlock()
}

func disarmEntropyRecovery() {
	entropyRecoveryArmed.Store(false)
	entropyArmTimerMu.Lock()
	if entropyArmTimer != nil {
		entropyArmTimer.Stop()
		entropyArmTimer = nil
	}
	entropyArmTimerMu.Unlock()
}

// entropyDecisionsPaused is consulted by maybeFinaliseCompletedEpochs.
func entropyDecisionsPaused() bool {
	return entropyRecoveryArmed.Load() && !entropyReplayActive.Load()
}

// ---------------------------------------------------------------------------
// Durable mix record
// ---------------------------------------------------------------------------

// Seams for tests; production goes to the sync KV.
var (
	entropyMixPersist = func(epoch uint64, seed randao.Seed, outcome string) error {
		return DB_OPs.RecordEntropyMix(nil, DB_OPs.EntropyMixRecord{
			Epoch: epoch, SeedHex: hex.EncodeToString(seed[:]), Outcome: outcome,
		})
	}
	entropyMixLoad = func(epoch uint64) (randao.Seed, bool, error) {
		rec, found, err := DB_OPs.GetEntropyMix(nil, epoch)
		if err != nil || !found {
			return randao.Seed{}, false, err
		}
		raw, err := hex.DecodeString(rec.SeedHex)
		if err != nil || len(raw) != len(randao.Seed{}) {
			return randao.Seed{}, false, fmt.Errorf("entropy mix epoch %d: malformed record", epoch)
		}
		var s randao.Seed
		copy(s[:], raw)
		return s, true, nil
	}
)

const (
	mixOutcomeMixed    = "mixed"
	mixOutcomeFallback = "fallback"
)

func persistFinalisedMix(epoch uint64, seed randao.Seed, outcome string) {
	if err := entropyMixPersist(epoch, seed, outcome); err != nil {
		log.Error().Err(err).Uint64("epoch", epoch).Str("outcome", outcome).
			Msg("entropy: could not persist the finalised mix - a restart will have to re-derive it by replay")
	}
}

// RestoreEntropyMixes loads this node's own persisted mixes for [from, to] into
// the in-memory mix store. Returns how many were restored.
func RestoreEntropyMixes(from, to uint64) (int, error) {
	restored := 0
	var firstErr error
	for e := from; ; e++ {
		seed, ok, err := entropyMixLoad(e)
		switch {
		case err != nil:
			if firstErr == nil {
				firstErr = err
			}
			log.Error().Err(err).Uint64("epoch", e).Msg("entropy recovery: persisted mix unreadable - replay must re-derive it")
		case ok:
			if defaultMixStore.remember(e, seed) {
				restored++
			} else {
				log.Error().Uint64("epoch", e).
					Msg("entropy recovery: persisted mix disagrees with the in-memory one - keeping the in-memory value")
			}
		}
		if e == to {
			break
		}
	}
	return restored, firstErr
}

// ---------------------------------------------------------------------------
// Replay
// ---------------------------------------------------------------------------

// maxEntropyReplayBlocks caps the backward scan. Slot grows by >= 1 per block,
// so two epochs span at most 2N blocks; anything beyond means corrupt slots.
const maxEntropyReplayBlocks = 4*N + 64

// EntropyReplayReport describes one replay, for logs and tests.
type EntropyReplayReport struct {
	TipHeight, TipSlot  uint64
	FromHeight          uint64
	FromEpoch, TipEpoch uint64
	Blocks              int
	MixesRestored       int
	ResumedEpoch        uint64
	Resumed             bool
}

// ReplayEntropyFromChain rebuilds the entropy state for the last two epochs from
// stored blocks. getBlock must return committed blocks by height.
func ReplayEntropyFromChain(tipHeight uint64, getBlock BlockByHeightFn) (EntropyReplayReport, error) {
	var rep EntropyReplayReport
	if getBlock == nil {
		return rep, errors.New("entropy replay: nil block loader")
	}

	entropyEffectsMu.Lock()
	defer entropyEffectsMu.Unlock()

	tip, err := getBlock(tipHeight)
	if err != nil || tip == nil {
		return rep, fmt.Errorf("entropy replay: reading tip %d: %v", tipHeight, err)
	}
	rep.TipHeight, rep.TipSlot = tipHeight, tip.Slot
	rep.TipEpoch = EpochForSlot(tip.Slot)
	if rep.TipEpoch > 0 {
		rep.FromEpoch = rep.TipEpoch - 1
	}
	startSlot := rep.FromEpoch * N

	// Walk back to the first block of FromEpoch. Heights are contiguous and
	// slots strictly increase with height, so this is a short scan.
	blocks := []*config.ZKBlock{tip}
	rep.FromHeight = tipHeight
	for h := tipHeight; h > 0; h-- {
		b, gerr := getBlock(h - 1)
		if gerr != nil || b == nil {
			return rep, fmt.Errorf("entropy replay: committed block %d unreadable: %v", h-1, gerr)
		}
		if b.Slot < startSlot {
			break
		}
		if b.Slot >= blocks[0].Slot && b.Slot != 0 {
			return rep, fmt.Errorf("entropy replay: slot does not increase with height at %d (slot %d, next %d) - corrupt slot field",
				h-1, b.Slot, blocks[0].Slot)
		}
		blocks = append([]*config.ZKBlock{b}, blocks...)
		rep.FromHeight = h - 1
		if len(blocks) > maxEntropyReplayBlocks {
			return rep, fmt.Errorf("entropy replay: more than %d blocks in two epochs - corrupt slot field", maxEntropyReplayBlocks)
		}
	}

	// Walk FORWARD too. The caller read tipHeight before this lock was taken;
	// a block stored in between had its effects applied under the startup
	// gate (folded, not decided), and the accumulator reset below discards
	// that fold - so it must be replayed here, not left out.
	for h := tipHeight + 1; len(blocks) <= maxEntropyReplayBlocks; h++ {
		b, gerr := getBlock(h)
		if gerr != nil || b == nil {
			break // end of the stored chain
		}
		if b.Slot <= blocks[len(blocks)-1].Slot {
			return rep, fmt.Errorf("entropy replay: slot does not increase with height at %d (slot %d, previous %d) - corrupt slot field",
				h, b.Slot, blocks[len(blocks)-1].Slot)
		}
		blocks = append(blocks, b)
		rep.TipHeight, rep.TipSlot = h, b.Slot
	}
	if EpochForSlot(rep.TipSlot) != rep.TipEpoch {
		rep.TipEpoch = EpochForSlot(rep.TipSlot) // tip moved into the next epoch while starting
	}

	// Reset the per-epoch state the replay re-derives. Older pending fallbacks
	// are past their deadline by construction; newer accumulators are rebuilt
	// from scratch so the fold order is exactly the chain's.
	finaliseTrackMu.Lock()
	if rep.FromEpoch > 0 {
		lastDecidedEpoch, haveDecidedAny = rep.FromEpoch-1, true
	} else {
		lastDecidedEpoch, haveDecidedAny = 0, false
	}
	pendingFallback = make(map[uint64]struct{})
	finaliseTrackMu.Unlock()

	defaultEntropyAccumulatorStore.mu.Lock()
	for e := range defaultEntropyAccumulatorStore.accs {
		if e >= rep.FromEpoch {
			delete(defaultEntropyAccumulatorStore.accs, e)
		}
	}
	defaultEntropyAccumulatorStore.mu.Unlock()

	entropyReplayActive.Store(true)
	entropyQuietFinalise.Store(true)
	defer func() {
		entropyQuietFinalise.Store(false)
		entropyReplayActive.Store(false)
	}()
	for _, b := range blocks {
		applyEntropyEffectsLocked(b, entropyModeReplay)
	}
	rep.Blocks = len(blocks)
	return rep, nil
}

// resumeSealingAfterReplay hands the newest decided epoch in [from, to] to the
// Stage-E hook once, outside quiet mode. Returns the epoch and whether it ran.
func resumeSealingAfterReplay(from, to uint64) (uint64, bool) {
	finaliseTrackMu.Lock()
	decided, have := lastDecidedEpoch, haveDecidedAny
	finaliseTrackMu.Unlock()
	if !have {
		return 0, false
	}
	if decided < to {
		to = decided
	}
	if to < from {
		return 0, false
	}
	for e := to; ; e-- {
		if seed, ok := defaultMixStore.get(e); ok {
			epochFinalisedHookMu.Lock()
			hook := epochFinalisedHook
			epochFinalisedHookMu.Unlock()
			if hook == nil {
				return e, false
			}
			hook(e, seed)
			return e, true
		}
		if e == from || e == 0 {
			return 0, false
		}
	}
}

// RunEntropyStartupRecovery restores persisted mixes, replays the last two
// epochs and resumes sealing. It ALWAYS disarms the startup gate. Call after the
// committee eligibility source is wired (replay verifies certificates) and after
// the beacon is installed and rehydrated.
func RunEntropyStartupRecovery(beaconInstalled bool, tipHeight uint64, haveTip bool, getBlock BlockByHeightFn) (EntropyReplayReport, error) {
	defer disarmEntropyRecovery()
	if !beaconInstalled {
		return EntropyReplayReport{}, nil // Stage 1: no accumulator can be built; nothing to rebuild
	}
	if !haveTip {
		return EntropyReplayReport{}, nil // empty chain
	}
	tip, err := getBlock(tipHeight)
	if err != nil || tip == nil {
		return EntropyReplayReport{}, fmt.Errorf("entropy recovery: reading tip %d: %v", tipHeight, err)
	}
	eTip := EpochForSlot(tip.Slot)
	from := uint64(0)
	if eTip >= mixRetainEpochs {
		from = eTip - mixRetainEpochs
	}
	restored, rerr := RestoreEntropyMixes(from, eTip)
	if rerr != nil {
		log.Warn().Err(rerr).Msg("entropy recovery: some persisted mixes were unreadable; relying on replay")
	}

	rep, err := ReplayEntropyFromChain(tipHeight, getBlock)
	rep.MixesRestored = restored
	if err != nil {
		return rep, err
	}
	rep.ResumedEpoch, rep.Resumed = resumeSealingAfterReplay(rep.FromEpoch, rep.TipEpoch)
	return rep, nil
}
