package Sequencer

// VDFSealer runs the epoch VDF evaluation on a background goroutine, per
// AVC-Low-Level-Design.md §1. Sealing takes ~T_vdf (target 1200-1410s) of
// unavoidable sequential computation; doing it synchronously would stall
// block commits for most of the epoch. This is deliberately isolated from the
// block-commit loop - nothing here touches Consensus.Start, matching the
// design doc's own "no consensus-flow change" instruction.
//
// NOT WIRED: the trigger call (Start, meant to fire once the entropy
// committee's reveal window closes and the mix is folded - Low-Level-Design
// §1's "on mix-ready") and the read (Result, at epoch-boundary block-build
// time) have no caller in Consensus.go yet. The entropy-committee reveal
// collection this depends on (Low-Level-Design §4, M4.1-M4.4) is not built
// either. This type is ready for that caller once it lands - see
// AVC-Low-Level-Design.md §8's build-order note: the VDF itself has zero
// dependency on anything still open, so it can be built now and wired later.

import (
	"context"
	"errors"
	"fmt"
	"gossipnode/messaging"

	"sync"
	"time"

	"github.com/JupiterMetaLabs/avc/beacon"
	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/JupiterMetaLabs/avc/vdf"
	"github.com/rs/zerolog/log"
)

// SealResult is what the background goroutine reports back.
type SealResult struct {
	ForEpoch uint64
	Proof    vdf.Proof
	Err      error
}

// VDFSealer wraps one epoch's VDF evaluation. Create a fresh VDFSealer per
// epoch - Start must be called at most once per instance, matching the
// single-buffered result channel.
type VDFSealer struct {
	pipeline *beacon.Pipeline
	resultCh chan SealResult

	// mu/latched make Result idempotent. The channel is the goroutine's
	// handoff and can only be received from ONCE; latched is what lets the
	// value be read again afterwards. See Result's own comment for why that
	// matters.
	mu      sync.Mutex
	latched *SealResult

	// cancel stops this epoch's in-flight evaluation. Nil until Start runs.
	//
	// Guarded by mu, and idempotent: context.CancelFunc may be called any
	// number of times. Cancel() is therefore safe to call from the block
	// path, from a peer-adoption handler, and from a later duplicate
	// adoption of the same proof.
	cancel context.CancelFunc

	// cancelled records that cancellation was REQUESTED, so a result that
	// lands afterwards can be recognised as stale. See Result.
	cancelled bool
}

// NewVDFSealer wraps the network's beacon pipeline for one epoch's sealing.
func NewVDFSealer(pipeline *beacon.Pipeline) *VDFSealer {
	return &VDFSealer{pipeline: pipeline, resultCh: make(chan SealResult, 1)}
}

// Start launches the sealing goroutine for forEpoch against mix and returns
// immediately - the ~20 minute evaluation runs in the background, and the
// caller's block-commit loop keeps committing normally for slots K+1..N-1 in
// the meantime.
//
// Uses Pipeline.Seal, not SealLocally: the caller needs the actual vdf.Proof
// bytes to embed in the epoch-boundary block (config.ZKBlock.VdfProof), and
// SealLocally discards them - it's the recovery path for a node that only
// needs to publish entropy, not carry the proof forward.
// vdfSealerDeadlineMultiple bounds one evaluation's wall-clock lifetime as a
// multiple of messaging.TargetVDFDelay — the design-target duration T is
// CALIBRATED to approximate on the slowest expected fleet hardware (D-46a).
// There is no portable way to turn a raw squaring count T into a wall-clock
// bound directly: that would require a live timing measurement at startup,
// which beacon_install.go's own header already rules out. TargetVDFDelay is
// the one wall-clock value this codebase already trusts as "what a correctly-
// calibrated T should take", so deriving the deadline from it (rather than
// from a disconnected, newly-invented constant) ties the bound to the same
// assumption the whole VDF design already rests on. 3x leaves generous room
// for a node slower than the calibration target without being unbounded: a
// node still running past 3x its design budget is not merely slow, something
// is wrong (a mis-set T, a stuck host), and the goroutine releasing lets
// CancelSealer/evictOldSealersLocked reclaim it instead of accumulating
// forever — exactly the D-46a failure this closes.
const vdfSealerDeadlineMultiple = 3

// vdfSealerDeadline is the per-evaluation deadline Start enforces. A var, not
// the multiplication inlined at the call site, so a test can shrink it
// without waiting out the real ~1-hour production budget. Production code
// must never assign to this — SetVDFSealerDeadlineForTest exists only for
// tests, matching this file's other *ForTest seams.
var vdfSealerDeadline = vdfSealerDeadlineMultiple * messaging.TargetVDFDelay

// SetVDFSealerDeadlineForTest overrides vdfSealerDeadline for the duration of
// a test. Returns a func that restores the previous value — call it via
// t.Cleanup, the same pattern as ClearSealerForTest's siblings.
func SetVDFSealerDeadlineForTest(d time.Duration) (restore func()) {
	prev := vdfSealerDeadline
	vdfSealerDeadline = d
	return func() { vdfSealerDeadline = prev }
}

func (s *VDFSealer) Start(forEpoch uint64, mix randao.Seed) {
	// Read vdfSealerDeadline exactly once, here, and use this local for the
	// rest of Start (including inside the goroutine below). The package var
	// exists only so a test can change it before calling Start; reading it
	// a second time from the goroutine would race against a concurrent
	// SetVDFSealerDeadlineForTest restore from a DIFFERENT test's cleanup
	// running on the main test goroutine while this one is still in flight.
	deadline := vdfSealerDeadline
	ctx, cancel := context.WithTimeout(context.Background(), deadline)

	s.mu.Lock()
	if s.cancelled {
		// D-31: this sealer was pre-cancelled by CancelSealer BEFORE Start was
		// ever called on it — a peer's proof for forEpoch was adopted while
		// this node was still waiting on its own Stage-D fold (see
		// vdf_seal_wiring.go's CancelSealer doc comment for the race this
		// closes). Launching a ~T_vdf evaluation here would be pure waste:
		// the epoch is already resolved, and SealerResultFor already reports
		// "not ready" for a cancelled sealer with no result ever latched, so
		// there is nothing this evaluation could still usefully produce.
		s.mu.Unlock()
		cancel()
		return
	}
	if s.cancel != nil {
		// Already started. Do NOT launch a second evaluation for the same
		// epoch — sealerFor keys one VDFSealer per epoch precisely so this
		// cannot happen, and a duplicate would race two goroutines onto a
		// single-slot channel.
		s.mu.Unlock()
		cancel()
		return
	}
	s.cancel = cancel
	s.mu.Unlock()

	go func() {
		defer cancel() // release the context regardless of how we exit

		proof, err := s.pipeline.SealContext(ctx, forEpoch, mix)

		if errors.Is(err, vdf.ErrEvalCancelled) {
			// vdf.EvalContext wraps ANY ctx.Err() (explicit Cancel() or this
			// deadline firing) as ErrEvalCancelled, so the two causes are
			// distinguished here, not by SealContext — see which one actually
			// fired before attributing it to a peer's proof winning the race.
			if errors.Is(err, context.DeadlineExceeded) {
				// D-46a: nobody has resolved this epoch — this evaluation
				// simply outran its budget (vdfSealerDeadlineMultiple x
				// messaging.TargetVDFDelay). No state to unwind (SealContext
				// published nothing); not delivering a result here matches
				// the cancelled-by-peer case below, so a later Result() call
				// still correctly reports "not ready" rather than a stale
				// empty proof — but the cause logged is actionable: T is
				// likely mis-calibrated for this host, or it is stuck.
				s.mu.Lock()
				s.cancelled = true
				s.mu.Unlock()
				log.Error().Uint64("for_epoch", forEpoch).
					Dur("budget", deadline).
					Msg("entropy: local VDF evaluation exceeded its deadline and was abandoned — " +
						"T is likely mis-calibrated for this host's hardware, or evaluation is stuck")
				return
			}
			// Someone else's proof was adopted first. SealContext published
			// nothing, so there is no state to unwind — just record the
			// outcome and do NOT deliver a result. Delivering one would let a
			// stale, empty proof satisfy a later Result() call and be attached
			// to a boundary block.
			s.mu.Lock()
			s.cancelled = true
			s.mu.Unlock()
			log.Info().Uint64("for_epoch", forEpoch).
				Msg("entropy: local VDF evaluation cancelled — a peer's proof for this epoch was " +
					"adopted first, so the remaining sequential work was abandoned")
			return
		}

		if err == nil {
			// Seal published the entropy into the sink as a side effect.
			// Persist both the entropy and the proof: the mix that produced
			// them is unrecoverable once this epoch ages out, and the proof is
			// what lets a peer recover the epoch from us later without a chain
			// scan.
			//
			// A PersistEpochEntropy failure now surfaces as a seal failure
			// (D-58) rather than being discarded (`_ = ...`): this goroutine
			// has no synchronous consumer to protect, unlike
			// entropy_vdf_accept.go's adopt path, so there is no liveness
			// reason to hide it. A seal this node cannot prove it holds after
			// a restart is not a successful seal — silently reporting success
			// here just moves the same failure to a later moment with no
			// diagnostic left. The goroutine itself still never crashes over
			// this; only SealResult.Err changes.
			if perr := messaging.PersistEpochEntropy(forEpoch); perr != nil {
				// Two different failures arrive here and only one is this
				// seal's fault, so they are separated rather than both being
				// reported as a failed seal.
				//
				// PersistEpochEntropy reads the PROCESS-GLOBAL beacon
				// (messaging.SetBeaconSource). In production that is the same
				// object this pipeline sealed into — beacon_install.go builds
				// one sink and hands it to both beacon.New and SetBeaconSource
				// — but nothing in the type system enforces that, and a
				// pipeline constructed with its own sink will not match the
				// global. s.pipeline.Ready asks OUR sink directly, so it
				// answers "did this seal land" independently of global wiring.
				switch {
				case errors.Is(perr, messaging.ErrNoEntropyToPersist) && s.pipeline.Ready(forEpoch):
					// Our sink holds it; the global beacon is a different
					// object. Nothing was persisted, but the seal is sound —
					// a wiring problem to surface, not a failed seal.
					log.Warn().Uint64("epoch", forEpoch).Err(perr).
						Msg("entropy: sealed entropy is in this pipeline's sink but not in the process-global " +
							"beacon — the two are not the same object, so nothing was persisted. Check SetBeaconSource wiring")
				default:
					// Either neither beacon holds it (the publish genuinely did
					// not land) or the KV write failed. Both mean this epoch
					// will not survive a restart.
					err = fmt.Errorf("seal succeeded but persisting entropy failed (epoch will not survive a restart): %w", perr)
				}
			}
			if raw, merr := proof.MarshalBinary(); merr == nil {
				_ = messaging.PersistVDFProof(forEpoch, raw)
			}
		}

		s.resultCh <- SealResult{ForEpoch: forEpoch, Proof: proof, Err: err}
	}()
}

// Cancel stops this sealer's in-flight evaluation, if any.
//
// Idempotent and safe to call concurrently with the goroutine finishing: a
// cancellation that arrives after completion is a no-op, and a result that
// arrives after cancellation is discarded by Start rather than delivered.
func (s *VDFSealer) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancelled = true
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Cancelled reports whether cancellation was requested for this sealer.
func (s *VDFSealer) Cancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled
}

// Result returns immediately, ready or not - it never blocks waiting for the
// goroutine. A node building the epoch-boundary block calls this; if the proof
// isn't ready yet, ok is false and the caller must fail closed (the existing
// ErrEntropyUnavailable pattern), never guess or wait past its own slot
// deadline.
//
// IDEMPOTENT SINCE 2026-09-03. It used to be single-shot: the receive from
// resultCh CONSUMED the value, so a second call reported not-ready forever,
// even though the evaluation had long since succeeded. Its own doc comment
// told callers to "keep the value from the first call" - and the only caller,
// Block.attachAVCConsensusFields, does not keep it. Any second build of the
// same epoch-boundary block (a round timeout and re-propose, a rejected
// block, a retried attach) therefore hit ErrVDFProofNotReady permanently and
// could not recover without a restart, which loses the sealer map entirely.
//
// The fix is a latch, not a bigger buffer: the first successful receive stores
// the result, and every later call replays it. Not-ready still reports
// not-ready, so the fail-closed contract at the boundary block is unchanged.
func (s *VDFSealer) Result() (SealResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.latched != nil {
		return *s.latched, true
	}
	select {
	case r := <-s.resultCh:
		s.latched = &r
		return r, true
	default:
		return SealResult{}, false
	}
}
