package messaging

// Pool-wide timeout voting (fix 3 of the committee-v2 stall).
//
// THE DEFECT
//
// A TimeoutCertificate needs a 2/3 quorum of the WHOLE eligible pool
// (timeoutVotingPool -> eligibleMembersUncapped: 29 peers -> 20 signatures).
// But the only production signer of a TimeoutVote was the sequencer itself,
// through MaybeStartTimeoutFlow's single call site in the failed-round branch
// of Sequencer.BroadcastAndProcessBlock. Every other node only relayed and
// tallied. One signature can never reach 20, so no certificate ever formed,
// Period never advanced, and - because the committee seed is
// H(entropy, PrevHash, Height, Period) and the slot is committed+Period+1 - a
// stuck height re-drew the SAME seats forever.
//
// THE FIX
//
// When a round fails, the sequencer floods a TimeoutRequest for (height,
// period): "round p at height h did not reach consensus". It is signed with
// the sequencer's libp2p identity key and verified against the PINNED
// sequencer peer id (consensus.sequencer_pinned_peer_id), whose public key is
// embedded in the id itself - so the request authenticates end to end no
// matter how many relays flood it. Every node that verifies it, is on the same
// round, and has not signed a block result for that round, signs its OWN
// TimeoutVote for (h, p+1) and gossips it. The existing collector/tally then
// reaches the pool-wide quorum and the existing PeriodStore advances.
//
// WHY THE TRIGGER IS THE SEQUENCER, NOT A LOCAL TIMER
//
// The sequencer is the only party that assembles block certificates (buddies
// return their signatures to it on request). An honest sequencer therefore
// either commits round p or declares it failed - never both. A local timer on
// each node would let a slow-but-successful round be timed out by 20 nodes
// whose clocks fired first, which is exactly the both-certificates race
// roundlock exists to prevent. A Byzantine sequencer could already fork the
// chain by proposing two blocks; this adds no new power to it.
//
// WHAT STILL HOLDS
//
//   - Period advances ONLY through a verified, quorum-backed certificate
//     (PeriodStore.AcceptTimeoutCertificate) - a request alone moves nothing.
//   - A node never signs both sides of a round (internal/roundlock).
//   - Gated by JMDN_TIMEOUT_CERT_WIRING like the rest of this flow. Off = no-op.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/internal/roundlock"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"
)

// TimeoutRequestDomain separates a TimeoutRequest's signed bytes from every
// other signed message (TimeoutVoteDomain, the block-vote prefixes).
const TimeoutRequestDomain = "jmdt/timeout-request/v1"

const timeoutRequestBroadcastType = "timeout_request"

// TimeoutRequest is the sequencer's signed declaration that round
// (Height, Period) failed to reach consensus.
type TimeoutRequest struct {
	Height    uint64
	Period    uint64
	Sequencer string // peer id of the signer; informational - verification uses the pin
	Sig       []byte // libp2p identity-key signature over CanonicalTimeoutRequestMessage
}

// CanonicalTimeoutRequestMessage is the exact byte string signed and verified.
func CanonicalTimeoutRequestMessage(chainID, height, period uint64) []byte {
	return fmt.Appendf(nil, "%s:chain=%d:h=%d:p=%d", TimeoutRequestDomain, chainID, height, period)
}

// SignTimeoutRequest signs a request for round (height, period).
func SignTimeoutRequest(priv crypto.PrivKey, sequencerID string, chainID, height, period uint64) (TimeoutRequest, error) {
	if priv == nil {
		return TimeoutRequest{}, errors.New("timeout request: nil signing key")
	}
	sig, err := priv.Sign(CanonicalTimeoutRequestMessage(chainID, height, period))
	if err != nil {
		return TimeoutRequest{}, fmt.Errorf("timeout request: sign: %w", err)
	}
	return TimeoutRequest{Height: height, Period: period, Sequencer: sequencerID, Sig: sig}, nil
}

// Errors returned by VerifyTimeoutRequest. All mean "do not act on this".
var (
	ErrTimeoutRequestNoPin     = errors.New("timeout request: no pinned sequencer peer id configured (fail closed)")
	ErrTimeoutRequestBadPin    = errors.New("timeout request: pinned sequencer peer id has no extractable public key (fail closed)")
	ErrTimeoutRequestSignature = errors.New("timeout request: signature does not verify against the pinned sequencer")
)

// VerifyTimeoutRequest checks req was signed by the pinned sequencer for this
// chain. The Sequencer field is ignored: the pinned id is the only authority.
func VerifyTimeoutRequest(req TimeoutRequest, pinned string, chainID uint64) error {
	pinned = strings.TrimSpace(pinned)
	if pinned == "" {
		return ErrTimeoutRequestNoPin
	}
	pid, err := peer.Decode(pinned)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTimeoutRequestBadPin, err)
	}
	pub, err := pid.ExtractPublicKey()
	if err != nil || pub == nil {
		return ErrTimeoutRequestBadPin
	}
	ok, err := pub.Verify(CanonicalTimeoutRequestMessage(chainID, req.Height, req.Period), req.Sig)
	if err != nil || !ok {
		return ErrTimeoutRequestSignature
	}
	return nil
}

// timeoutRequestAction is what a node should do with a verified request.
type timeoutRequestAction int

const (
	actSign timeoutRequestAction = iota
	actIgnoreSelf
	actIgnoreStale    // request is for a period this node has already moved past
	actIgnoreAhead    // request is for a period this node has not reached (missing a certificate)
	actRefuseBlockSig // this node already signed a block result for the round
	// actAlreadySigned: this node already signed its OWN timeout vote for this
	// round. (F-2) msg.ID/Timestamp on the broadcast envelope are not covered
	// by TimeoutRequest.Sig (only chain/height/period are signed), so a relay
	// can re-wrap an already-seen, validly-signed request under a fresh
	// envelope and evade the broadcast layer's isMessageSeen dedup. Before
	// this case existed, decideTimeoutRequest fell through to TryLock, which
	// is intentionally idempotent for the SAME side (so a retried vote-result
	// request is not refused) - so a replayed request re-signed and
	// re-broadcast every time it arrived. This case is checked first so a
	// replay is a no-op instead.
	actAlreadySigned
)

func (a timeoutRequestAction) String() string {
	switch a {
	case actSign:
		return "sign"
	case actIgnoreSelf:
		return "ignore_self"
	case actIgnoreStale:
		return "ignore_stale_period"
	case actIgnoreAhead:
		return "ignore_ahead_period"
	case actRefuseBlockSig:
		return "refuse_already_signed_block"
	case actAlreadySigned:
		return "already_signed_timeout"
	default:
		return "unknown"
	}
}

// decideTimeoutRequest is the pure decision for a VERIFIED request. It locks
// the round in ledger only when the answer is actSign, so a refusal leaves the
// node free to sign a block result for the round later.
func decideTimeoutRequest(req TimeoutRequest, selfID, pinned string, localPeriod uint64, ledger *roundlock.Ledger) timeoutRequestAction {
	if selfID != "" && selfID == strings.TrimSpace(pinned) {
		return actIgnoreSelf // the sequencer signs its own vote in MaybeStartTimeoutFlow
	}
	if req.Period < localPeriod {
		return actIgnoreStale
	}
	if req.Period > localPeriod {
		return actIgnoreAhead
	}
	r := roundlock.Round{Height: req.Height, Period: req.Period}
	// F-2 replay guard: check what THIS node already signed for the round
	// before touching TryLock. TryLock is deliberately idempotent for a
	// repeated SAME-side call (a retried vote-result must not be refused),
	// so it cannot itself distinguish "first request" from "Nth replay" - the
	// caller has to. A prior Block signature still falls through to TryLock
	// below, which correctly refuses it (actRefuseBlockSig); only a prior
	// Timeout signature short-circuits here.
	if side, signed := ledger.Signed(r); signed && side == roundlock.Timeout {
		return actAlreadySigned
	}
	if ok, _ := ledger.TryLock(r, roundlock.Timeout); !ok {
		return actRefuseBlockSig
	}
	return actSign
}

// timeoutRequestPin and timeoutRequestBLSKey are the handler's two external
// inputs. Package variables so tests can drive the real handler without
// global settings or an on-disk BLS key; production uses the functions below.
var (
	timeoutRequestPin    = pinnedSequencerID
	timeoutRequestBLSKey = func() ([]byte, error) {
		priv, _, err := BLS_Signer.LocalBLSKeypair()
		return priv, err
	}
)

// pinnedSequencerID reads consensus.sequencer_pinned_peer_id.
func pinnedSequencerID() string {
	if !settings.IsLoaded() {
		return ""
	}
	return strings.TrimSpace(settings.Get().Consensus.SequencerPinnedPeerID)
}

// broadcastTimeoutRequest signs and floods a request for round (height,
// period) using this host's identity key. Called by MaybeStartTimeoutFlow on
// the sequencer. Best-effort: failures are logged, never fatal.
func broadcastTimeoutRequest(h host.Host, height, period uint64) {
	if h == nil {
		return
	}
	priv := h.Peerstore().PrivKey(h.ID())
	if priv == nil {
		log.Warn().Uint64("height", height).Msg("timeout request: host identity key unavailable, cannot sign request")
		return
	}
	req, err := SignTimeoutRequest(priv, h.ID().String(), BLS_Signer.DomainChainID(), height, period)
	if err != nil {
		log.Warn().Err(err).Uint64("height", height).Msg("timeout request: signing failed")
		return
	}
	data, err := json.Marshal(req)
	if err != nil {
		log.Warn().Err(err).Msg("timeout request: marshal failed")
		return
	}
	log.Info().Uint64("height", height).Uint64("period", period).
		Msg("timeout request: round failed, asking the pool to sign timeout votes")
	sendTimeoutGossip(h, timeoutRequestBroadcastType, data)
}

// handleTimeoutRequestBroadcast is the receive side (dispatched from
// broadcast.go's HandleBroadcastStream). On a verified request for this
// node's current round it signs and gossips this node's own TimeoutVote.
func handleTimeoutRequestBroadcast(h host.Host, msg BroadcastMessageStruct) {
	if !TimeoutCertWiringEnabled || h == nil {
		return
	}
	var req TimeoutRequest
	if err := json.Unmarshal([]byte(msg.Data), &req); err != nil {
		log.Warn().Err(err).Str("msg_id", msg.ID).Msg("timeout request: failed to unmarshal")
		return
	}
	pinned := timeoutRequestPin()
	if err := VerifyTimeoutRequest(req, pinned, BLS_Signer.DomainChainID()); err != nil {
		log.Warn().Err(err).Uint64("height", req.Height).Uint64("period", req.Period).
			Msg("timeout request: rejected")
		return
	}

	localPeriod := DefaultPeriodStore.PeriodFor(req.Height)
	action := decideTimeoutRequest(req, h.ID().String(), pinned, localPeriod, roundlock.Default)
	if action != actSign {
		log.Info().Uint64("height", req.Height).Uint64("period", req.Period).
			Uint64("local_period", localPeriod).Str("action", action.String()).
			Msg("timeout request: not signing")
		return
	}

	priv, err := timeoutRequestBLSKey()
	if err != nil {
		log.Warn().Err(err).Uint64("height", req.Height).
			Msg("timeout request: no local BLS key, cannot sign a timeout vote")
		return
	}
	vote, err := SignTimeoutVote(priv, h.ID().String(), BLS_Signer.DomainChainID(), req.Height, req.Period+1)
	if err != nil {
		log.Warn().Err(err).Uint64("height", req.Height).Msg("timeout request: failed to sign timeout vote")
		return
	}
	log.Info().Uint64("height", req.Height).Uint64("period", req.Period+1).
		Msg("timeout request: verified; signed and broadcasting this node's timeout vote")
	// F-5: this node cannot know who cast block votes for the round it is
	// timing out - blockVoters is produced by the SEQUENCER's state machine
	// (Sequencer/consensus_statemachine.go), and this handler runs on every
	// pool node, not just the sequencer. That is an information limit, not an
	// oversight: passing nil here means tryCertify's §7.1b layer-2
	// equivocation exclusion does not run on this path (it only ever ran on
	// the pre-fix sequencer-only signer, which always had blockVoters). The
	// remaining defense is layer 1, roundlock's honest self-restraint - which
	// does not catch a Byzantine node that signs both sides deliberately. See
	// the review's F-5 for the severity discussion; not closed by this PR.
	var blockVotersUnknown map[string]bool // intentionally nil - see comment above
	recordAndMaybeCertify(h, vote, blockVotersUnknown)
	broadcastTimeoutVote(h, vote)
}

// periodCatchUpMaxPeers bounds how many peers a node asks for a missing
// certificate before verifying a block (each ask is bounded by
// timeoutCertRejoinTimeout), so the admission path cannot stall for long.
const periodCatchUpMaxPeers = 3

// ensurePeriodForBlock fetches a missing TimeoutCertificate when b claims a
// later Period for its height than this node has accepted.
//
// Without it, a node that missed the certificate gossip (briefly offline,
// restarted - PeriodStore is in-memory) fails closed on EVERY block of the new
// period with period_not_synced, and never recovers on its own:
// RequestLatestTimeoutCertificateFromPeers had no production caller. This is
// that caller. The certificate it adopts is fully re-verified
// (AcceptIncomingTimeoutCertificate), so a peer cannot push this node to a
// period that was never certified; b.Period is only the hint to go and look.
//
// No-op when the wiring is off, or when this node is already at or past the
// block's period.
func ensurePeriodForBlock(h host.Host, b *config.ZKBlock) {
	if !TimeoutCertWiringEnabled || h == nil || b == nil {
		return
	}
	height, claimed := b.BlockNumber, b.Period
	if claimed <= DefaultPeriodStore.PeriodFor(height) {
		return
	}
	peers := h.Network().Peers()
	if len(peers) > periodCatchUpMaxPeers {
		peers = peers[:periodCatchUpMaxPeers]
	}
	newPeriod, adopted, err := RequestLatestTimeoutCertificateFromPeers(h, peers, height)
	log.Info().Uint64("height", height).Uint64("claimed_period", claimed).
		Uint64("new_period", newPeriod).Bool("adopted", adopted).Err(err).
		Msg("period catch-up: block claims a later period than this node holds")
}
