package messaging

// VDF proof PULL — request/response recovery for a node that has no entropy
// for an epoch and cannot wait for its own evaluation.
//
// # Why a pull path exists at all
//
// A VDF proof travels in exactly one place: the epoch-boundary block. A node
// that was offline, restarted, or still syncing when that block passed has no
// second chance at it — the push path simply never fires again. Before this,
// such a node's only options were to finish its own ~T_vdf evaluation or go
// without entropy for the epoch entirely.
//
// # What is and is not trusted
//
// The response carries the PROOF ONLY. It never carries entropy and never
// carries a mix, and this is the whole security argument: the requester
// verifies the proof against the mix IT retained itself. A mix supplied by the
// same party as the proof would verify any proof that party chose, which is
// precisely the steering beacon.Pipeline.Accept exists to prevent.
//
// So a malicious responder can waste one short-lived stream and nothing more.
// A malicious REQUESTER can waste one bounded read of local KV.
//
// # One validation path
//
// A pulled proof re-enters the SAME function a block-carried proof does —
// VerifyAndAcceptVDFProof — by being placed on a synthetic boundary block
// carrying only the fields those checks read. There is deliberately no second
// verifier.
//
// WHICH OF THOSE CHECKS ACTUALLY BITE HERE — this is not symmetric with the
// block path, and the difference matters to anyone changing either side.
// VerifyAndAcceptVDFProof runs five checks. Two of them validate a PROPOSER'S
// DECLARATION, and on this path there is no proposer: the synthetic block's
// Slot and SeedEpoch are both computed locally by the dispatcher, from the same
// epoch, using the same functions the checks re-apply. Since
// EpochBoundarySlot(e) = e*N and EpochForSlot(s) = s/N are exact inverses:
//
//	CHECK 1 (proof sits on its epoch's boundary slot)  e*N != e*N   -> never fires
//	CHECK 2 (declared SeedEpoch matches the slot)      (e*N)/N != e -> never fires
//
// They are not redundant on the block path, where a proposer supplies Slot and
// SeedEpoch independently and either can lie. Here they are tautologies.
//
// CONSEQUENCE, and the reason this is written down: on the pull path the epoch
// binding of a pulled proof rests ENTIRELY on CHECK 3 and what follows it —
// FinalisedMixFor(E-1) loads the mix THIS node finalised, and vdf.Verify
// re-derives the challenge from that mix, so a proof sealed for any epoch other
// than E cannot verify. That is sufficient. But it is a single mechanism, not
// three: a future change that relaxes CHECK 3 (say, accepting a mix from
// elsewhere when the local one has aged out) would remove the ONLY thing
// binding a pulled proof to its epoch, while the comment above it still lists
// two other checks that appear to cover the same property and do not.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

// vdfProofRequestTimeout bounds one request end-to-end (dial + write + read).
// Short on purpose: a recovering node asks several peers, so one unresponsive
// peer must not stall the others. Same value and same reasoning as the
// timeout-certificate rejoin path.
const vdfProofRequestTimeout = 5 * time.Second

// maxVDFProofRequestBytes bounds what the responder will read from a stream.
// A request is a single small JSON object; anything larger is malformed or
// hostile, and reading it would let a peer use the handler as a memory sink.
const maxVDFProofRequestBytes = 1 << 10

// maxRecoveryPeers caps how many peers ONE recovery round will ask.
//
// The caller passes h.Network().Peers() — every connected peer, unfiltered and
// in map-iteration order. Asking all of them sequentially is bounded only by
// 5s * len(peers), and an unresponsive peer costs the full timeout. On a
// churny network that product runs to minutes, while recoveryInFlight holds the
// per-epoch latch for the entire sweep and the deadline fires only
// VDFProofRecoveryDeadlineSlots slots before the boundary. A sweep that outlives
// its own runway defeats the mechanism: the proof lands after the block that
// needed it, and no second attempt could start in the meantime.
//
// 12 is chosen against the failure it prevents, not against a topology guess:
// a proof is held by every node that sealed or adopted the epoch, so if any
// meaningful fraction of the connected set has it, a dozen random draws find
// one with overwhelming probability. If none of twelve has it, a thirteenth is
// far less useful than failing closed on time and letting local evaluation
// finish — which is exactly what the no-recovery path already does safely.
const maxRecoveryPeers = 12

// maxRecoveryRoundBudget bounds ONE round end-to-end, independent of the
// per-peer timeout and of how many peers are asked. Belt to maxRecoveryPeers'
// braces: the cap bounds the COUNT, this bounds the TIME, and neither alone is
// sufficient if the other is later tuned.
const maxRecoveryRoundBudget = 30 * time.Second

// pickRecoveryPeers shuffles and caps the candidate set.
//
// Shuffling is load-bearing, not cosmetic. h.Network().Peers() returns peers in
// Go map order, which is randomised per iteration but NOT uniform across a
// process's lifetime in any way this code should rely on; more importantly, an
// uncapped caller previously walked whatever order it got. With a cap, taking a
// deterministic prefix would let one unlucky set of dead peers starve recovery
// every single epoch. A fresh shuffle makes each round an independent draw, so
// repeated failure requires repeated bad luck rather than one bad neighbourhood.
//
// Copies before shuffling: the slice belongs to the caller (and libp2p), and
// reordering it in place would be a visible side effect on shared state.
func pickRecoveryPeers(peers []peer.ID, limit int) []peer.ID {
	out := make([]peer.ID, len(peers))
	copy(out, peers)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// VDFProofRequest is the wire request: "do you have the VDF proof for this
// entropy epoch?". Nothing else is needed — the responder answers strictly
// from its own already-verified store and trusts nothing the requester claims.
type VDFProofRequest struct {
	Epoch uint64 `json:"epoch"`
}

// VDFProofResponse is the wire response.
//
// Found=false with an empty Proof is a NORMAL answer, not an error: most nodes
// hold proofs for only a handful of recent epochs, and an epoch this responder
// never sealed or adopted is simply not available here.
//
// Proof is exactly the encoding vdf.Proof.MarshalBinary produces — the same
// bytes the boundary block carries — so the requester feeds it to the existing
// verification path unchanged. There is deliberately no second encoding.
type VDFProofResponse struct {
	Found bool   `json:"found"`
	Epoch uint64 `json:"epoch"`
	Proof []byte `json:"proof,omitempty"`
}

// HandleVDFProofRequestStream is the receive side, registered on
// config.VDFProofRequestProtocol at node startup (node/node.go).
//
// Bounded work by construction: one direct KV read of vdf_proof:<epoch>. It
// does NOT scan the chain, enumerate history, or start an evaluation — a
// request handler that could be made to do any of those would be a denial-of-
// service surface, and answering "which block was epoch E's boundary?" from
// block storage would require exactly such a scan.
func HandleVDFProofRequestStream(s network.Stream) {
	defer s.Close()
	remote := s.Conn().RemotePeer()

	_ = s.SetReadDeadline(time.Now().Add(vdfProofRequestTimeout))
	reader := bufio.NewReader(&io_LimitedStream{s: s, remaining: maxVDFProofRequestBytes})
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		log.Warn().Err(err).Str("from", remote.String()).
			Msg("vdf proof pull: request stream read failed")
		return
	}

	var req VDFProofRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		log.Warn().Err(err).Str("from", remote.String()).
			Msg("vdf proof pull: request payload was not valid JSON")
		return
	}

	resp := VDFProofResponse{Epoch: req.Epoch}
	if encoded, ok := LookupVDFProof(req.Epoch); ok {
		// Refuse to serve anything oversized even from our own store: the
		// bound is part of the wire contract, not just an input filter.
		if len(encoded) <= DB_OPs.MaxVDFProofBytes {
			resp.Found = true
			resp.Proof = encoded
		} else {
			log.Error().Uint64("epoch", req.Epoch).Int("bytes", len(encoded)).
				Msg("vdf proof pull: stored proof exceeds the wire maximum — refusing to serve it")
		}
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		log.Warn().Err(err).Uint64("epoch", req.Epoch).
			Msg("vdf proof pull: marshaling response failed")
		return
	}
	payload = append(payload, '\n')

	_ = s.SetWriteDeadline(time.Now().Add(vdfProofRequestTimeout))
	if _, err := s.Write(payload); err != nil {
		log.Warn().Err(err).Str("to", remote.String()).Uint64("epoch", req.Epoch).
			Msg("vdf proof pull: writing response failed")
		return
	}

	log.Debug().Str("to", remote.String()).Uint64("epoch", req.Epoch).Bool("found", resp.Found).
		Msg("vdf proof pull: answered request")
}

// io_LimitedStream caps how many bytes the handler will read from a peer.
// A plain io.LimitReader would do, but the stream must stay available for the
// write half, so the limit wraps only the read side.
type io_LimitedStream struct {
	s         network.Stream
	remaining int
}

func (l *io_LimitedStream) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, fmt.Errorf("vdf proof pull: request exceeded %d bytes", maxVDFProofRequestBytes)
	}
	if len(p) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.s.Read(p)
	l.remaining -= n
	return n, err
}

// requestVDFProofFromPeer asks exactly one peer over a fresh stream.
//
// Returns the raw encoded proof; the CALLER verifies it. Nothing this function
// returns has been trusted or acted upon yet.
func requestVDFProofFromPeer(ctx context.Context, h host.Host, p peer.ID, epoch uint64) ([]byte, bool, error) {
	payload, err := json.Marshal(VDFProofRequest{Epoch: epoch})
	if err != nil {
		return nil, false, fmt.Errorf("vdf proof pull: marshaling request: %w", err)
	}
	payload = append(payload, '\n')

	stream, err := h.NewStream(ctx, p, config.VDFProofRequestProtocol)
	if err != nil {
		return nil, false, fmt.Errorf("vdf proof pull: opening stream to %s: %w", p, err)
	}
	defer stream.Close()

	// Stream deadlines come from ctx when it carries one, so the caller's budget
	// actually bounds this call. ctx alone does NOT: NewStream honours it, but
	// libp2p stream reads and writes are governed by their own deadlines, so a
	// hardcoded value here would let one peer cost dial + write + read no matter
	// what deadline the caller set. Falls back to the constant for a ctx with no
	// deadline, which is what the tests pass.
	deadline := time.Now().Add(vdfProofRequestTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	_ = stream.SetWriteDeadline(deadline)
	if _, err := stream.Write(payload); err != nil {
		return nil, false, fmt.Errorf("vdf proof pull: writing request to %s: %w", p, err)
	}

	_ = stream.SetReadDeadline(deadline)
	line, err := bufio.NewReader(&io_LimitedStream{
		s: stream, remaining: DB_OPs.MaxVDFProofBytes * 2,
	}).ReadString('\n')
	if err != nil && line == "" {
		return nil, false, fmt.Errorf("vdf proof pull: reading response from %s: %w", p, err)
	}

	var resp VDFProofResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return nil, false, fmt.Errorf("vdf proof pull: response from %s was not valid JSON: %w", p, err)
	}
	if !resp.Found || len(resp.Proof) == 0 {
		return nil, false, nil
	}
	if resp.Epoch != epoch {
		// Answering a different epoch than asked is not a protocol we accept;
		// the epoch binding is re-checked during verification anyway, but
		// rejecting here keeps the mismatch attributable to the responder.
		return nil, false, fmt.Errorf("vdf proof pull: %s answered for epoch %d, asked for %d",
			p, resp.Epoch, epoch)
	}
	if len(resp.Proof) > DB_OPs.MaxVDFProofBytes {
		return nil, false, fmt.Errorf("vdf proof pull: %s returned %d bytes, over the %d maximum",
			p, len(resp.Proof), DB_OPs.MaxVDFProofBytes)
	}
	return resp.Proof, true, nil
}

// recoveryInFlight deduplicates recovery per epoch.
//
// The deadline is reached on every block until entropy arrives, so without
// this a node would open a fresh round of streams per block. An entry is
// removed only when the attempt finishes, so a slow round trip does not spawn
// a second one.
var (
	recoveryMu       sync.Mutex
	recoveryInFlight = make(map[uint64]struct{})
)

func beginRecovery(epoch uint64) bool {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	if _, busy := recoveryInFlight[epoch]; busy {
		return false
	}
	recoveryInFlight[epoch] = struct{}{}
	return true
}

func endRecovery(epoch uint64) {
	recoveryMu.Lock()
	delete(recoveryInFlight, epoch)
	recoveryMu.Unlock()
}

// RecoverVDFProofFromPeers asks peers for epoch's proof and adopts the first
// one that VERIFIES.
//
// "First valid proof wins" means exactly that: not the first packet received.
// Every response goes through VerifyAndAcceptVDFProof, so a peer that answers
// fastest with a bad proof loses to a slower peer with a good one.
//
// MUST be called from a background goroutine — never from the block path. It
// dials peers and blocks on I/O.
func RecoverVDFProofFromPeers(h host.Host, peers []peer.ID, epoch uint64, boundarySlot uint64) (adopted bool) {
	if h == nil || len(peers) == 0 {
		return false
	}
	if !beginRecovery(epoch) {
		return false // already trying
	}
	defer endRecovery(epoch)

	candidates := pickRecoveryPeers(peers, maxRecoveryPeers)

	// One budget for the whole round. Without it the round costs
	// vdfProofRequestTimeout * len(candidates) in the worst case, which can
	// outlive the runway the deadline left us.
	roundDeadline := time.Now().Add(maxRecoveryRoundBudget)

	log.Info().Uint64("epoch", epoch).
		Int("connected_peers", len(peers)).Int("asking", len(candidates)).
		Dur("round_budget", maxRecoveryRoundBudget).
		Msg("entropy: recovery deadline reached without entropy for this epoch — asking a bounded " +
			"random sample of peers for the VDF proof (local evaluation continues regardless)")

	for _, p := range candidates {
		if remaining := time.Until(roundDeadline); remaining <= 0 {
			log.Warn().Uint64("epoch", epoch).Dur("budget", maxRecoveryRoundBudget).
				Msg("entropy: recovery round budget exhausted before a usable proof was found — " +
					"abandoning this round so the next block can start a fresh one; local " +
					"evaluation is unaffected")
			break
		}

		// Never let one peer consume more than what is left of the round.
		perPeer := vdfProofRequestTimeout
		if remaining := time.Until(roundDeadline); remaining < perPeer {
			perPeer = remaining
		}

		ctx, cancel := context.WithTimeout(context.Background(), perPeer)
		encoded, found, err := requestVDFProofFromPeer(ctx, h, p, epoch)
		cancel()

		if err != nil {
			log.Debug().Err(err).Str("peer", p.String()).Uint64("epoch", epoch).
				Msg("entropy: proof request failed, trying next peer")
			continue
		}
		if !found {
			continue
		}

		// THE SINGLE VALIDATION PATH. The pulled proof is placed on a synthetic
		// block carrying only the fields the five checks read, so it is
		// verified by exactly the same code a block-carried proof is — against
		// this node's OWN mix, never anything the peer supplied.
		synthetic := &config.ZKBlock{
			BlockNumber: 0, // not a real block; used only to carry the proof
			Slot:        boundarySlot,
			SeedEpoch:   epoch,
			VdfProof:    encoded,
		}
		if verr := VerifyAndAcceptVDFProof(synthetic); verr != nil {
			log.Warn().Err(verr).Str("peer", p.String()).Uint64("epoch", epoch).
				Msg("entropy: peer's VDF proof failed verification — rejected, nothing published, " +
					"trying next peer")
			continue
		}

		log.Info().Str("peer", p.String()).Uint64("epoch", epoch).
			Msg("entropy: RECOVERED epoch entropy from a peer's VDF proof — verified locally against " +
				"our own mix and adopted in milliseconds instead of a full evaluation")
		return true
	}

	log.Warn().Uint64("epoch", epoch).
		Int("asked", len(candidates)).Int("connected_peers", len(peers)).
		Msg("entropy: no peer in this round's sample produced a usable VDF proof for this epoch — " +
			"local evaluation continues and this node fails closed for the epoch until it " +
			"completes. A later block re-fires the deadline and draws a fresh sample")
	return false
}
