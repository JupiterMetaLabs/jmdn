package messaging

// Architecture §4.4's RevealPush — the transport that carries a committee
// member's reveal to whoever is proposing the current slot. New 2026-08-20.
//
// # Why this has to exist at all
//
// §4.4 documents the gap precisely: the original design had reveals riding
// piggyback on the vote-reply message, which is a TWO-hop path (revealer →
// its buddy → sequencer), and a revealer that is not a buddy that round has no
// acknowledged channel at all — only "hope some other node forwards it". Under
// Rule 1 one silent drop takes the whole epoch to fallback with no signal
// about why. The fix §4.4 specifies, and this file implements:
//
//	RevealPush{ epoch, peerID, reveal, } pushed DIRECTLY to the current
//	proposer, once per slot across the whole reveal window — not once total.
//
// # The acknowledgment is not a message
//
// This is the part that is easy to get wrong. A proposer-issued "received" ACK
// would be worthless: a dishonest proposer can send it and still drop the
// reveal. The only ACK that means anything is the reveal appearing in a
// COMMITTED block, which every node already verifies independently (Rule 2).
// So this sender does not wait for, or trust, any reply — it keeps pushing
// every slot until either the reveal lands in a block or the cutoff arrives,
// and `RevealAlreadyLanded` is how it checks, by looking at its own
// Accumulator rather than at anything a peer told it.
//
// # What it deliberately does NOT close
//
// Per §4.4's own stated residual: this converts "one silent drop anywhere =
// free fallback" into "an adversary must control block inclusion for that
// specific revealer across the ENTIRE reveal window". It does not fully defeat
// a single sustained censoring sequencer, because with one static sequencer
// every slot in the window has the same proposer. That residual closes only
// when proposer rotation (task #8) lands and the proposer role stops being
// monopolisable. Stating it here so nobody reads this file as a complete fix.
//
// # No signature field in the message, on purpose
//
// §4.4 sketches `RevealPush{epoch, peerID, reveal, signature}`. Under
// Decision A the reveal IS the signature — a separate authenticating signature
// would be redundant, and worse, would invite someone to check the wrong one.
// The receiver verifies the reveal itself against the sender's self-certifying
// peer ID, which authenticates the payload and the identity in one step.
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"

	"gossipnode/config"
)

// revealPushTimeout bounds one push attempt. Short on purpose: a push is
// retried on the next slot anyway, so a slow peer must not hold a goroutine.
const revealPushTimeout = 5 * time.Second

// RevealPushMessage is the wire form. JSON + newline-delimited, matching the
// existing BroadcastProtocol convention in this package rather than inventing
// a second encoding style.
type RevealPushMessage struct {
	// Epoch the reveal is bound to. Carried explicitly so the receiver never
	// has to guess which epoch a reveal belongs to from its own clock — the
	// verification message includes the epoch, so a mismatch simply fails.
	Epoch uint64 `json:"epoch"`

	// PeerID of the revealing member. Self-certifying: the receiver extracts
	// the ed25519 public key from this and needs no key directory.
	PeerID string `json:"peer_id"`

	// Reveal is the 64-byte ed25519 signature (Decision A).
	Reveal []byte `json:"reveal"`
}

// HandleRevealPushStream is the receive side, registered on
// config.RevealPushProtocol at node startup.
//
// Verifies before filing (AddInboundReveal does the ed25519 check) and sends
// no reply — see this file's note on why an ACK would be worthless. A rejected
// push is logged at warn with the sender's peer ID: under Decision A a reveal
// either verifies or is forged, so a failure here is a protocol violation
// worth seeing, not a transient condition.
func HandleRevealPushStream(s network.Stream) {
	defer s.Close()
	remote := s.Conn().RemotePeer()

	line, err := bufio.NewReader(s).ReadString('\n')
	if err != nil && line == "" {
		log.Warn().Err(err).Str("from", remote.String()).
			Msg("entropy: RevealPush stream read failed")
		return
	}

	var m RevealPushMessage
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		log.Warn().Err(err).Str("from", remote.String()).
			Msg("entropy: RevealPush payload was not valid JSON")
		return
	}

	// The claimed peer ID must be the peer that actually opened the stream.
	// Without this, any node could relay a reveal attributed to someone else —
	// which would still fail the signature check, but this makes the
	// misbehaviour attributable to the sender rather than looking like the
	// named member sent something invalid.
	if m.PeerID != remote.String() {
		log.Warn().Str("from", remote.String()).Str("claimed", m.PeerID).
			Msg("entropy: RevealPush claims a peer ID that is not the sender — dropped (use the relay path for third-party reveals, not RevealPush)")
		return
	}

	if err := AddInboundReveal(m.Epoch, m.PeerID, m.Reveal); err != nil {
		log.Warn().Err(err).Str("from", remote.String()).Uint64("epoch", m.Epoch).
			Int("reveal_len", len(m.Reveal)).
			Msg("entropy: RevealPush rejected")
		return
	}

	log.Debug().Str("from", remote.String()).Uint64("epoch", m.Epoch).
		Int("inbox_now", InboxCountForEpoch(m.Epoch)).
		Msg("entropy: RevealPush accepted")
}

// RevealAlreadyLanded reports whether this node's own reveal for epoch has been
// folded from a committed block — the ONLY meaningful delivery confirmation
// (§4.4: success is observing the reveal in a committed block, never a peer's
// say-so).
//
// Returns false when the epoch's Accumulator cannot be resolved: if we cannot
// tell, we must assume it has not landed and keep pushing. Assuming success on
// an unknown is exactly how a reveal gets silently dropped.
func RevealAlreadyLanded(epoch uint64) bool {
	_, peerID, err := nodeIdentity()
	if err != nil {
		return false
	}
	defaultEntropyAccumulatorStore.mu.Lock()
	acc := defaultEntropyAccumulatorStore.accs[epoch]
	defaultEntropyAccumulatorStore.mu.Unlock()
	if acc == nil {
		return false
	}
	for _, missing := range acc.Missing() {
		if missing == peerID {
			return false // still outstanding
		}
	}
	// Not in the missing list. Only conclusive if this node was expected at
	// all — a node that is not on the committee is trivially "not missing".
	seated, err := SelfOnEntropyCommittee(epoch)
	return err == nil && seated
}

// PushOwnRevealForSlot pushes this node's reveal for slot's epoch to the given
// proposer, if there is anything to push.
//
// Call once per slot inside the reveal window. Cheap no-op in every case that
// isn't "I am seated, my reveal has not landed yet, and I know who to send it
// to" — which is the overwhelming majority of calls, since only m of P nodes
// are seated in any epoch.
//
// Errors are returned rather than only logged so a caller can count failures;
// a single failed push is not important (the next slot retries) but a peer that
// fails every slot is worth surfacing.
func PushOwnRevealForSlot(slot uint64, proposer peer.ID) error {
	epoch := EpochForSlot(slot)
	if slot >= cutoffSlotFor(epoch) {
		return nil // reveal window closed; pushing now would be pointless
	}

	_, peerID, err := nodeIdentity()
	if err != nil {
		return nil // no identity: nothing to contribute, not an error
	}
	if proposer == "" || proposer.String() == peerID {
		return nil // we ARE the proposer: our own reveal goes in via the inbox
	}
	if RevealAlreadyLanded(epoch) {
		return nil // already in a committed block — stop pushing
	}

	reveal, err := ProduceRevealForEpoch(epoch)
	if err != nil {
		return nil // not seated, or no beacon yet: nothing to push
	}

	h := getHostInstance()
	if h == nil {
		return fmt.Errorf("entropy: no libp2p host installed, cannot push reveal for epoch %d", epoch)
	}

	payload, err := json.Marshal(RevealPushMessage{Epoch: epoch, PeerID: peerID, Reveal: reveal})
	if err != nil {
		return fmt.Errorf("entropy: marshaling RevealPush for epoch %d: %w", epoch, err)
	}
	payload = append(payload, '\n')

	ctx, cancel := context.WithTimeout(context.Background(), revealPushTimeout)
	defer cancel()

	stream, err := h.NewStream(ctx, proposer, config.RevealPushProtocol)
	if err != nil {
		return fmt.Errorf("entropy: opening RevealPush stream to %s: %w", proposer, err)
	}
	defer stream.Close()

	if _, err := stream.Write(payload); err != nil {
		return fmt.Errorf("entropy: writing RevealPush to %s: %w", proposer, err)
	}

	log.Debug().Str("to", proposer.String()).Uint64("epoch", epoch).Uint64("slot", slot).
		Msg("entropy: pushed own reveal to the current slot's proposer")
	return nil
}
