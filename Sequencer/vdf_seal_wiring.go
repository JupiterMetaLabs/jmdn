package Sequencer

// Stage E of the M4 pipeline (AVC-M4-Entropy-Reveal-Pipeline-Design.md §E) —
// wires messaging's Stage-D "epoch just finalised" event into
// VDFSealer.Start, per Low-Level-Design §1's "on mix-ready" trigger
// (VDF-Implementation-Handoff.md §5's snippet). vdf_sealer.go's own header
// comment says this trigger call "has no caller in Consensus.go yet" — this
// file is that caller.
//
// The trigger arrives via messaging.SetEpochFinalisedHook rather than a
// direct call, because messaging cannot import Sequencer: Sequencer already
// imports messaging (consensus_statemachine.go's
// messaging.BroadcastBlockToEveryNode* calls), so the reverse import would
// cycle. InstallEpochFinalisedHook registers this file's callback with that
// seam; call it once at startup (InstallAVCBeaconFromEnv, Stage F, does
// this for you as part of installing the beacon).
//
// STILL NOT LIVE ON ITS OWN: onEpochFinalised only starts sealing once a
// real *beacon.Pipeline has been installed via SetVDFPipeline, which only
// happens once Stage F's two crypto parameters (a provenance-verified VDF
// group modulus, a fleet-calibrated difficulty T) are actually supplied —
// see beacon_install.go's header for exactly why those are not, and must
// not be, invented here. Until then, onEpochFinalised logs and returns
// without starting anything — a mix is computed (Stage D succeeded) but
// never gets sealed or published.
import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/JupiterMetaLabs/avc/beacon"
	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/rs/zerolog/log"

	"gossipnode/messaging"
)

// ErrVDFProofNotReady is returned by Block.attachAVCConsensusFields (via
// SealerResultFor) when an epoch-boundary block is being built but that
// epoch's VDF sealing has not finished yet. Callers MUST fail closed on it —
// same discipline as committee.ErrEntropyUnavailable: proposing a
// boundary block with a missing/zero VdfProof would let whichever node
// happens to have raced ahead choose the committee for every node that
// didn't.
var ErrVDFProofNotReady = errors.New("Sequencer: VDF proof for this epoch's boundary block is not ready yet (fail closed)")

var (
	vdfPipelineMu sync.Mutex
	vdfPipeline   *beacon.Pipeline
)

// SetVDFPipeline installs the network's beacon pipeline. Call once at
// startup — never with a nil group, a zero difficulty, or a modulus this
// process generated itself; beacon.New already fails closed on the first
// two, and avc/vdf.NewRSAGroup's own doc comment explains why the third can
// silently produce a VDF with no real delay, undetectably.
func SetVDFPipeline(p *beacon.Pipeline) {
	vdfPipelineMu.Lock()
	vdfPipeline = p
	vdfPipelineMu.Unlock()
}

func activeVDFPipeline() *beacon.Pipeline {
	vdfPipelineMu.Lock()
	defer vdfPipelineMu.Unlock()
	return vdfPipeline
}

var (
	vdfSealersMu sync.Mutex
	vdfSealers   = make(map[uint64]*VDFSealer)

	// evictedBelow is a monotonic watermark: every epoch strictly less than
	// this value has already been evicted from vdfSealers (or the package is
	// fresh and none ever existed). It exists because eviction alone does not
	// stop a LATER call from resurrecting an evicted epoch: sealerFor's
	// map-miss path cannot tell "this epoch is brand new" apart from "this
	// epoch's sealer was evicted", and building a fresh (non-cancelled)
	// sealer for the latter would relaunch a full ~T_vdf evaluation for an
	// epoch this node has already moved well past. See sealerFor and
	// CancelSealer below for the two places this is consulted.
	evictedBelow uint64
)

// D-31: configuredBeaconRetainEpochs re-derives the SAME
// JMDN_AVC_BEACON_RETAIN_EPOCHS value beacon_install.go's
// InstallAVCBeaconFromEnv already computes (same env var, same default,
// same parse rule, verified against beacon_install.go:296-301) EXCEPT for
// error handling: this is retention bookkeeping consulted on every
// sealerFor/CancelSealer call on an already-running node, which must never
// abort over a malformed value another code path (beacon_install.go) already
// validated at startup. Duplicating this three-line env read here rather
// than exporting and sharing it is exactly the kind of divergence-prone
// pattern D-47 (still Open as of this fix) already flags for the sibling
// mix-store window (messaging/entropy_mix_store.go's hardcoded
// mixRetainEpochs, which does NOT read this env var at all) — this map
// inherits that same duplication risk, it does not resolve it. D-47 remains
// its own, separate, unaddressed finding.
func configuredBeaconRetainEpochs() uint64 {
	retain := uint64(committee.MinRetainedEpochs)
	if retainStr := strings.TrimSpace(os.Getenv("JMDN_AVC_BEACON_RETAIN_EPOCHS")); retainStr != "" {
		if r, err := strconv.ParseUint(retainStr, 10, 64); err == nil {
			retain = r
		}
	}
	return retain
}

// evictOldSealersLocked bounds vdfSealers — D-31's second half: before this,
// nothing ever removed an entry outside tests (the only delete(vdfSealers,
// ...) in this file is ClearSealerForTest), so the map grew by one entry per
// epoch for the life of the process.
//
// "newest" is computed from the map's OWN current contents plus the epoch
// just being touched, rather than from a separately-tracked package variable:
// a separate counter would not reset when a test swaps vdfSealers for a fresh
// map (see vdf_seal_wiring_test.go's resetVDFWiringState), and would then
// evict small test epoch numbers immediately using a stale "newest" left
// over from an unrelated earlier test. Deriving it from live map keys makes
// this self-contained and exactly as test-swappable as vdfSealers itself.
//
// Caller must hold vdfSealersMu.
func evictOldSealersLocked(touchedEpoch uint64) {
	retain := configuredBeaconRetainEpochs()
	newest := touchedEpoch
	for e := range vdfSealers {
		if e > newest {
			newest = e
		}
	}
	if newest < retain {
		return
	}
	cutoff := newest - retain
	// Advance the resurrection-guard watermark alongside the actual deletes
	// below -- see evictedBelow's own comment for why this is required, not
	// just bookkeeping: without it, sealerFor/CancelSealer cannot tell an
	// evicted epoch apart from a brand-new one.
	if cutoff > evictedBelow {
		evictedBelow = cutoff
	}
	for e := range vdfSealers {
		if e < cutoff {
			delete(vdfSealers, e)
		}
	}
}

// InstallEpochFinalisedHook registers this file's sealing trigger with
// messaging's Stage-D seam. Call once at startup, any time relative to
// SetVDFPipeline — onEpochFinalised reads activeVDFPipeline() fresh on every
// invocation rather than capturing it at registration time, so the two
// calls' order doesn't matter.
func InstallEpochFinalisedHook() {
	messaging.SetEpochFinalisedHook(onEpochFinalised)
}

// onEpochFinalised is messaging's Stage-D callback: closedEpoch's
// Accumulator was just finalised to seed. Starts sealing for
// closedEpoch+1 — beacon.Pipeline.Seal's own doc comment is explicit that
// forEpoch must be "the epoch AFTER the one whose reveals produced the
// mix", which is exactly closedEpoch+1 here.
func onEpochFinalised(closedEpoch uint64, seed randao.Seed) {
	forEpoch := closedEpoch + 1

	// A bootstrapped successor epoch already has its (config-pinned) entropy.
	// Sealing would try to Publish a DIFFERENT value for the same epoch, which
	// BeaconSource refuses -> SealResult.Err -> boundary-block 503. Skip; every
	// node skips the same epochs because the set comes from config, not from
	// when this node started. See beacon_bootstrap.go.
	if IsBootstrapEpoch(forEpoch) {
		log.Info().Uint64("closed_epoch", closedEpoch).Uint64("for_epoch", forEpoch).
			Msg("entropy: epoch finalised but its successor is a bootstrap epoch (consensus.entropy_bootstrap) — sealing skipped, pinned bootstrap value stays authoritative")
		return
	}

	pipeline := activeVDFPipeline()
	if pipeline == nil {
		log.Warn().Uint64("closed_epoch", closedEpoch).Uint64("for_epoch", forEpoch).
			Msg("entropy: epoch finalised but no VDF pipeline installed yet (Stage F not wired) — mix computed, sealing skipped, entropy for this epoch will never be published")
		return
	}

	sealer := sealerFor(forEpoch, pipeline)
	sealer.Start(forEpoch, seed)
	log.Info().Uint64("closed_epoch", closedEpoch).Uint64("for_epoch", forEpoch).
		Msg("entropy: VDF sealing started in background (target ~1200-1410s, VDF-Implementation-Handoff.md §0) for the newly finalised epoch")
}

// sealerFor returns forEpoch's VDFSealer, constructing it on first use.
// VDFSealer.Start must be called at most once per instance (its own doc
// comment — single-buffered result channel); keying by forEpoch here is
// what makes that true across repeated/replayed onEpochFinalised calls.
func sealerFor(forEpoch uint64, pipeline *beacon.Pipeline) *VDFSealer {
	vdfSealersMu.Lock()
	defer vdfSealersMu.Unlock()
	if s, ok := vdfSealers[forEpoch]; ok {
		// D-31: this is also the path a CancelSealer-planted, pre-cancelled
		// placeholder (see CancelSealer below) is returned through — sealerFor
		// must return the SAME object, not replace it with a fresh one, or
		// Start's s.cancelled check would never see the cancellation.
		return s
	}
	if forEpoch < evictedBelow {
		// This epoch's sealer (if it ever had one) was already evicted, and
		// the node has moved on far enough that a legitimate new request for
		// it should never occur -- onEpochFinalised only ever advances
		// forward. Refuse to build a fresh sealer: return a pre-cancelled one
		// that is NOT stored in vdfSealers, so Start() on it is a no-op and
		// SealerResultFor keeps reporting "not ready" for this epoch -- the
		// correct fail-closed answer for an epoch this stale -- instead of
		// silently launching a full ~T_vdf evaluation for a chain position
		// long since resolved (or, on a replayed/duplicate finalisation
		// event, never actually needed at all).
		return &VDFSealer{resultCh: make(chan SealResult, 1), cancelled: true}
	}
	s := NewVDFSealer(pipeline)
	vdfSealers[forEpoch] = s
	evictOldSealersLocked(forEpoch)
	return s
}

// CancelSealer stops forEpoch's in-flight VDF evaluation, if one is running.
//
// Called when this node adopts a peer's proof for forEpoch: the evaluation is
// then redundant, and on a T calibrated to minutes the remaining sequential
// work is the single largest avoidable CPU cost in the entropy path.
//
// Safe and idempotent when no sealer exists, when it has already finished, and
// when called repeatedly (a duplicate proof for the same epoch arrives often —
// once per peer that gossips the boundary block).
//
// The sealer entry is deliberately NOT removed from vdfSealers: a cancelled
// epoch must keep reporting "not ready" through SealerResultFor rather than
// silently restarting, and sealerFor's per-epoch keying is what prevents a
// second evaluation being launched for an epoch already decided.
func CancelSealer(forEpoch uint64) {
	vdfSealersMu.Lock()
	s, ok := vdfSealers[forEpoch]
	if !ok {
		if forEpoch < evictedBelow {
			// Already evicted and past the point any legitimate caller
			// should still be resolving -- nothing to cancel, and planting a
			// placeholder here would just be transient map noise removed on
			// the next eviction pass anyway. See sealerFor's matching check
			// (same evictedBelow watermark) for the full reasoning.
			vdfSealersMu.Unlock()
			return
		}
		// D-31: a peer's proof for forEpoch can be adopted before THIS node's
		// own onEpochFinalised has fired for it — Stage D's fold timing is not
		// fleet-synchronised, so a late/slow node can still be waiting on its
		// own mix while a faster peer has already sealed and gossiped. Before
		// this, that ordering silently dropped the cancellation here (bare
		// `return`): there was no VDFSealer yet to mark, so nothing stopped
		// the LATER onEpochFinalised call from creating a fresh one and
		// launching a full ~T_vdf evaluation for an epoch already resolved —
		// exactly the wasted CPU this function's own doc comment says it
		// exists to avoid.
		//
		// Planting an already-cancelled placeholder here closes that: sealerFor
		// returns THIS SAME object on its later map hit rather than replacing
		// it, and Start (vdf_sealer.go) now checks s.cancelled before ever
		// launching the goroutine.
		s = &VDFSealer{resultCh: make(chan SealResult, 1), cancelled: true}
		vdfSealers[forEpoch] = s
		evictOldSealersLocked(forEpoch)
		vdfSealersMu.Unlock()
		return
	}
	vdfSealersMu.Unlock()
	s.Cancel()
}

// SealerCancelledForTest reports whether forEpoch's sealer was cancelled.
// Test-only.
func SealerCancelledForTest(forEpoch uint64) bool {
	vdfSealersMu.Lock()
	s, ok := vdfSealers[forEpoch]
	vdfSealersMu.Unlock()
	return ok && s.Cancelled()
}

// SealerResultFor returns forEpoch's sealing result, if a sealer was started
// for it and has finished. This is the read side of the
// VDF-Implementation-Handoff.md §5/§6 pattern. WIRED: Block/consensus_fields.go
// calls this on the epoch-boundary block to attach VdfProof, returning
// ErrVDFProofNotReady (fail closed) if ok is false.
func SealerResultFor(forEpoch uint64) (SealResult, bool) {
	vdfSealersMu.Lock()
	s, ok := vdfSealers[forEpoch]
	vdfSealersMu.Unlock()
	if !ok {
		return SealResult{}, false
	}
	return s.Result()
}

// SeedSealResultForTest injects a completed (or failed) SealResult for
// forEpoch directly into vdfSealers, bypassing Start and the background
// goroutine entirely. Test-only — lets a test in another package (e.g.
// Block/consensus_fields_test.go) exercise SealerResultFor's ok=true and
// ok=false paths without running a real ~20-minute VDF evaluation. The
// ok=false ("not ready") path needs no seam at all: simply don't seed a
// result for that epoch, and SealerResultFor already returns
// (SealResult{}, false) for any epoch with no registered sealer. Overwrites
// any sealer already registered for forEpoch.
// ClearSealerForTest removes forEpoch's registered sealer. Test-only.
//
// Needed because vdfSealers is package-level state and SeedSealResultForTest
// writes into it. Until Result was made idempotent (2026-09-03) the drain
// itself acted as accidental cleanup: a seeded result was consumed by the
// first read, so it could not leak into a later test that expected
// "not ready". With the latch that accident is gone, and tests must clean up
// explicitly — which they should always have done.
func ClearSealerForTest(forEpoch uint64) {
	vdfSealersMu.Lock()
	delete(vdfSealers, forEpoch)
	vdfSealersMu.Unlock()
}

func SeedSealResultForTest(forEpoch uint64, result SealResult) {
	s := &VDFSealer{resultCh: make(chan SealResult, 1)}
	s.resultCh <- result
	vdfSealersMu.Lock()
	vdfSealers[forEpoch] = s
	vdfSealersMu.Unlock()
}
