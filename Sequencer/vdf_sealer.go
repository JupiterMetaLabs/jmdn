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
	"gossipnode/messaging"

	"sync"

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
func (s *VDFSealer) Start(forEpoch uint64, mix randao.Seed) {
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
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
			// scan. Non-fatal by design — this runs on a background goroutine
			// and must never take the node down.
			_ = messaging.PersistEpochEntropy(forEpoch)
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
