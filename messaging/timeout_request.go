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
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/internal/roundlock"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"
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
		// F-3: decideTimeoutRequest returning actSign means its own TryLock
		// call already reserved this round as Timeout-signed on INTENT. No
		// vote was actually produced, so release it - otherwise this node can
		// never sign a BLOCK result for the round either (roundlock refuses
		// the other side based on the reservation, not on an actual
		// signature), stranding it from both certificates for the rest of
		// this process's life. See roundlock.Ledger.Release's doc comment.
		roundlock.Default.Release(roundlock.Round{Height: req.Height, Period: req.Period}, roundlock.Timeout)
		log.Warn().Err(err).Uint64("height", req.Height).
			Msg("timeout request: no local BLS key, cannot sign a timeout vote")
		return
	}
	vote, err := SignTimeoutVote(priv, h.ID().String(), BLS_Signer.DomainChainID(), req.Height, req.Period+1)
	if err != nil {
		// F-3: same as above - release the reservation this attempt did not
		// use.
		roundlock.Default.Release(roundlock.Round{Height: req.Height, Period: req.Period}, roundlock.Timeout)
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

// periodCatchUpMaxPeers bounds how many peers ONE catch-up attempt asks.
// Peers are queried in parallel (see timeout_rejoin.go), so this bounds
// fan-out per attempt, not wall-clock time.
const periodCatchUpMaxPeers = 3

// periodCatchUpCooldown is the PRIMARY mitigation for F-1 (2026-09-25 audit,
// PR #159/#158 review): it bounds how often this node will attempt a network
// catch-up for the SAME height, no matter how many blocks or relays repeat
// the claim, or how many distinct (still-plausible) periods they claim for
// it. b.Period is unauthenticated at this call site (see ensurePeriodForBlock's
// comment) - but b.BlockNumber is not: checkLinkage (consensus_hardening.go),
// which runs before this ever executes and defaults on
// (JMDN_ENFORCE_BLOCK_LINKAGE=true), fail-closed-rejects any block whose
// BlockNumber isn't exactly this node's local tip+1. So the exploitable
// height at any moment is a single, slowly-advancing value, not an
// attacker's free choice - a per-height cooldown is a direct match for that
// surface, not a coarse approximation of one.
const periodCatchUpCooldown = 10 * time.Second

// periodCatchUpRetainHeights bounds periodCatchUpLimiter's memory the same
// way internal/roundlock.Ledger bounds its own - see that package's doc
// comment for the reasoning; it applies identically here.
const periodCatchUpRetainHeights = 1024

// periodCatchUpMaxJump is a defense-in-depth sanity bound ONLY. It exists to
// reject obviously-impossible claims (e.g. a forged near-max-uint64 Period)
// for free, before any other work. It is NOT the primary defense: the
// realistic attack claims exactly local+1, far below any sane bound placed
// here - periodCatchUpCooldown above is the real defense. Kept generous
// rather than tight (e.g. "5") because a genuinely stalled height can
// legitimately accumulate many periods - that is the exact case #159's
// pool-wide timeout voting exists to resolve - and a tight bound would
// strand an honestly-lagging node's own catch-up.
const periodCatchUpMaxJump = 100_000

// periodCatchUpLimiter rate-limits ensurePeriodForBlock's network fetches
// per height. Same shape as internal/roundlock.Ledger: mutex-guarded map,
// pruned by height so memory stays bounded regardless of how long the node
// runs or how many heights are ever claimed against it.
type periodCatchUpLimiter struct {
	mu        sync.Mutex
	lastTry   map[uint64]time.Time
	maxHeight uint64
}

func newPeriodCatchUpLimiter() *periodCatchUpLimiter {
	return &periodCatchUpLimiter{lastTry: make(map[uint64]time.Time)}
}

// defaultPeriodCatchUpLimiter is the process-wide limiter ensurePeriodForBlock
// uses. Package-level, process-lifetime state - resetPeriodCatchUpLimiterForTest
// below exists specifically so tests can isolate it, matching how this file's
// other package-level state (DefaultPeriodStore, TimeoutCertWiringEnabled,
// TimeoutCertRejoinEnabled) is already saved/restored per-test.
var defaultPeriodCatchUpLimiter = newPeriodCatchUpLimiter()

// resetPeriodCatchUpLimiterForTest replaces defaultPeriodCatchUpLimiter with a
// fresh, empty one and returns a restore function for t.Cleanup. Exists so a
// test's call(s) to ensurePeriodForBlock are never rate-limited by another
// test's prior use of the same height - without this, two tests (or two
// calls in one test) that reuse a height value across the same `go test`
// process would silently and non-deterministically interfere with each
// other via this shared limiter.
func resetPeriodCatchUpLimiterForTest() (restore func()) {
	prev := defaultPeriodCatchUpLimiter
	defaultPeriodCatchUpLimiter = newPeriodCatchUpLimiter()
	return func() { defaultPeriodCatchUpLimiter = prev }
}

// allow reports whether a catch-up attempt for height may proceed now, and
// if so records it so a call for the same height within the cooldown is
// refused without doing any network work.
func (l *periodCatchUpLimiter) allow(height uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, ok := l.lastTry[height]; ok && time.Since(last) < periodCatchUpCooldown {
		return false
	}
	l.lastTry[height] = time.Now()
	if height > l.maxHeight {
		l.maxHeight = height
		if l.maxHeight > periodCatchUpRetainHeights {
			floor := l.maxHeight - periodCatchUpRetainHeights
			for h := range l.lastTry {
				if h < floor {
					delete(l.lastTry, h)
				}
			}
		}
	}
	return true
}

// periodCatchUpGroup collapses concurrent ensurePeriodForBlock calls for the
// SAME height (several blocks/relays landing at once) into one outbound
// fetch, so a burst of duplicates costs one network round, not N.
var periodCatchUpGroup singleflight.Group

// randomPeers returns up to n peers chosen uniformly at random from h's
// currently connected peers. h.Network().Peers() has no defined order, so
// always taking its first n (the previous behaviour) could pick the same
// unresponsive peers on every call; randomizing spreads that risk instead of
// concentrating repeated catch-up load on whichever peers happen to sort
// first.
func randomPeers(h host.Host, n int) []peer.ID {
	peers := h.Network().Peers()
	if len(peers) <= n {
		return peers
	}
	shuffled := make([]peer.ID, len(peers))
	copy(shuffled, peers)
	rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	return shuffled[:n]
}

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
// Runs BEFORE certificate verification (see verifyBlockCertificate), on
// purpose - a node that hasn't caught up can't verify a cert built against a
// period it doesn't hold, so this can't be moved after verification. That
// means b.Period is, by construction, an UNAUTHENTICATED hint at this point;
// F-1 (2026-09-25 audit) is about bounding the cost of that hint being a
// lie:
//   - periodCatchUpMaxJump rejects impossible values for free (cheap, not
//     the real defense - see its own comment);
//   - defaultPeriodCatchUpLimiter caps this node to one network attempt per
//     height per periodCatchUpCooldown, however many times/ways it's claimed
//     (the real defense, and see periodCatchUpCooldown's comment for why a
//     per-height cooldown matches this attack surface specifically);
//   - periodCatchUpGroup collapses concurrent callers for the same height;
//   - RequestLatestTimeoutCertificateFromPeers (timeout_rejoin.go) now
//     queries its peers in parallel, bounding one attempt's wall-clock time
//     to timeoutCertRejoinTimeout regardless of peer count, instead of that
//     timeout multiplied by peer count.
//
// No-op when the wiring is off, or when this node is already at or past the
// block's period.
func ensurePeriodForBlock(h host.Host, b *config.ZKBlock) {
	if !TimeoutCertWiringEnabled || h == nil || b == nil {
		return
	}
	height, claimed := b.BlockNumber, b.Period
	local := DefaultPeriodStore.PeriodFor(height)
	if claimed <= local {
		return
	}
	if claimed-local > periodCatchUpMaxJump {
		log.Warn().Uint64("height", height).Uint64("claimed_period", claimed).
			Uint64("local_period", local).
			Msg("period catch-up: implausible period jump, ignoring without a network call")
		return
	}
	if !defaultPeriodCatchUpLimiter.allow(height) {
		log.Debug().Uint64("height", height).Uint64("claimed_period", claimed).
			Msg("period catch-up: cooldown active for this height, skipping network call")
		return
	}

	key := strconv.FormatUint(height, 10)
	_, _, _ = periodCatchUpGroup.Do(key, func() (interface{}, error) {
		peers := randomPeers(h, periodCatchUpMaxPeers)
		newPeriod, adopted, err := RequestLatestTimeoutCertificateFromPeers(h, peers, height)
		log.Info().Uint64("height", height).Uint64("claimed_period", claimed).
			Uint64("new_period", newPeriod).Bool("adopted", adopted).Err(err).
			Msg("period catch-up: block claims a later period than this node holds")
		return nil, nil
	})
}
