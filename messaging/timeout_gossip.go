package messaging

// M0/§7.1c wiring — connects the previously-standalone timeout-certificate
// primitives (timeout_certificates.go) into the live consensus flow:
//
//	timeout detected -> SignTimeoutVote -> gossip -> collect+verify ->
//	TallyTimeoutVotes -> TimeoutCertificate -> gossip -> every receiver
//	verifies + AcceptTimeoutCertificate -> PeriodStore advances
//
// Single call site: Sequencer.Consensus.BroadcastAndProcessBlock's existing
// consensusReached==false branch calls MaybeStartTimeoutFlow. Everything
// else in this file is either the gossip transport (reusing broadcast.go's
// existing flood-broadcast primitive — a new msg.Type value, not a new
// network) or bookkeeping so a certificate this node already holds can be
// handed to a rejoining peer without replaying every prior timeout.
//
// Gated OFF by default (JMDN_TIMEOUT_CERT_WIRING) — same coordinated
// fleet-wide rollout discipline as every other M-series flag in this
// package. With the flag off, MaybeStartTimeoutFlow and the two broadcast
// handlers are no-ops: nothing here sends or accepts a single message, and
// today's behaviour (a failed round is simply retried, per
// Sequencer.RejectionReport) is unchanged.
//
// SCOPE NOTE (disclosed, not silently dropped): a real network fetch-on-
// rejoin RPC ("ask a peer for the current height's latest certificate") is
// NOT built here — no such transport (gRPC or otherwise) exists anywhere in
// this repo today (checked: Sequencer, seednode). What IS built is the half
// that makes such an RPC trivial once added: LatestTimeoutCertificateFor
// answers "what's the newest certificate this node has accepted for height
// H" in O(1) from memory, and AcceptIncomingTimeoutCertificate lets any
// caller (gossip today, an RPC handler tomorrow) feed one in and jump
// straight to the right period — no replay of periods 1..N-1, because
// VerifyTimeoutCertificate's own contract is that a single certificate
// proves its entire prefix. This mirrors the precedent already set for the
// committee-snapshot seed-node work this session (docs/COMMITTEE-SNAPSHOT-
// FREEZE-TODO.md item 6): build the durable, testable core; track the wire
// transport as its own explicit TODO rather than bolting on an unaudited
// RPC surface under time pressure.
//
// MUTUAL EXCLUSION (§7.1b): a validator must never sign both a TimeoutVote
// and a normal block vote for the same (height, period). This is enforced
// two ways here: (1) self-check — MaybeStartTimeoutFlow refuses to sign a
// timeout vote for THIS node if the caller's blockVoters set says this node
// already cast a block vote this round; (2) tally-time exclusion — any
// OTHER voter present in both the block-vote set and the collected
// timeout-vote set is detected via DetectTimeoutBlockVoteEquivocation,
// reported via RecordTimeoutBlockVoteEquivocation (existing reputation
// pipeline), and excluded from the quorum tally so an equivocating vote
// cannot help reach 2/3. Enforcement (2) is only as complete as the caller's
// blockVoters knowledge: the node that ran this round's block-vote tally
// (today, the sequencer) has the real set; a pure gossip relay receiving a
// remote TimeoutVote generally does not, and passes nil — its BLS
// signature and quorum are still checked in full, only cross-referencing
// against the block-vote set is skipped on that path. Documented rather
// than silently assumed complete.
import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/rs/zerolog/log"
)

// TimeoutCertWiringEnabled gates the entire live wiring in this file.
// Default false.
var TimeoutCertWiringEnabled = envOn("JMDN_TIMEOUT_CERT_WIRING", false)

// Broadcast message types dispatched from broadcast.go's HandleBroadcastStream,
// exactly the same "vote_trigger" dispatch pattern already used there — a new
// value on the SAME flood-gossip transport, not a new network primitive.
const (
	timeoutVoteBroadcastType = "timeout_vote"
	timeoutCertBroadcastType = "timeout_certificate"
)

// timeoutRoundKey identifies one (height, period) round for local vote
// collection and certificate caching.
type timeoutRoundKey struct {
	Height uint64
	Period uint64
}

// timeoutVoteCollector accumulates TimeoutVotes per (height, period) round so
// this node can independently tally toward a TimeoutCertificate as votes
// arrive (its own and gossiped ones), and caches accepted certificates by
// height for catch-up. Deliberately package-private and separate from the
// block-vote store (Sequencer/Triggers/Maps): that store has no period axis
// and lives in a package messaging cannot import without a cycle.
type timeoutVoteCollector struct {
	mu        sync.Mutex
	votes     map[timeoutRoundKey][]TimeoutVote
	seen      map[timeoutRoundKey]map[string]bool
	certified map[timeoutRoundKey]bool          // this node already built/accepted a cert for this round
	certs     map[uint64]TimeoutCertificate      // latest accepted certificate per height
}

var defaultTimeoutVoteCollector = &timeoutVoteCollector{
	votes:     make(map[timeoutRoundKey][]TimeoutVote),
	seen:      make(map[timeoutRoundKey]map[string]bool),
	certified: make(map[timeoutRoundKey]bool),
	certs:     make(map[uint64]TimeoutCertificate),
}

// add records vote (deduped by VoterID within the round) and returns a
// snapshot of every vote collected for that round so far.
func (c *timeoutVoteCollector) add(v TimeoutVote) []TimeoutVote {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := timeoutRoundKey{v.Height, v.Period}
	if c.seen[key] == nil {
		c.seen[key] = make(map[string]bool)
	}
	if !c.seen[key][v.VoterID] {
		c.seen[key][v.VoterID] = true
		c.votes[key] = append(c.votes[key], v)
	}
	return append([]TimeoutVote(nil), c.votes[key]...)
}

// markCertified reports whether this call is the first to certify key —
// callers that lose the race get false and must not re-broadcast/re-accept.
func (c *timeoutVoteCollector) markCertified(key timeoutRoundKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.certified[key] {
		return false
	}
	c.certified[key] = true
	return true
}

// recordCert caches cert as the latest known certificate for its height,
// overwriting only with a strictly newer period (mirrors PeriodStore's own
// monotonicity rule so the cache never regresses).
func (c *timeoutVoteCollector) recordCert(cert TimeoutCertificate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.certs[cert.Height]; ok && existing.Period >= cert.Period {
		return
	}
	c.certs[cert.Height] = cert
}

func (c *timeoutVoteCollector) latestCert(height uint64) (TimeoutCertificate, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cert, ok := c.certs[height]
	return cert, ok
}

// LatestTimeoutCertificateFor returns the newest TimeoutCertificate this
// node has accepted (locally certified, or received and verified) for
// height, if any. This is the primitive a rejoin/catch-up RPC would call —
// see the SCOPE NOTE at the top of this file for why that RPC itself is not
// built yet.
func LatestTimeoutCertificateFor(height uint64) (TimeoutCertificate, bool) {
	return defaultTimeoutVoteCollector.latestCert(height)
}

// timeoutVotingPool returns the SAME pool the block-vote path uses for
// height's round (TallyTimeoutVotes/VerifyTimeoutCertificate's own doc
// comments require this: recovery must not depend on the entity that failed
// to reach consensus in the first place). The epoch is derived exactly the
// way the block-vote committee already is elsewhere in this package.
func timeoutVotingPool(height uint64) (poolSize int, pubKeys map[string][]byte, err error) {
	epoch := EpochForSlot(LiveSlotFor(height))
	eligible, err := eligibleMembersUncappedForEpoch(epoch, false)
	if err != nil {
		return 0, nil, fmt.Errorf("timeout voting pool: %w", err)
	}
	pubKeys = make(map[string][]byte, len(eligible))
	for pid, hexKey := range eligible {
		pubKeys[pid] = blsKeyBytes(hexKey)
	}
	return len(eligible), pubKeys, nil
}

// MaybeStartTimeoutFlow is the single integration point requested end-to-
// end: call it exactly where consensusReached==false is discovered (today,
// Sequencer.Consensus.BroadcastAndProcessBlock's existing else-branch,
// mirroring the pointer already left in timeout_certificates.go's TimeoutVote
// doc comment at jmdn/AVC/BFT/bft/sequencer_client.go:275-277 — that lower
// layer cannot call here directly, since messaging already imports bft and a
// reverse import would cycle; Sequencer sits above both, so it is the
// correct wiring point).
//
// height is the round's block number (the pending, not-yet-committed
// height). blockVoters is this round's block-vote peer-ID set (build from
// blsResults — every peer that returned ANY vote this round, regardless of
// Agree/Reject) so the mutual-exclusion self-check and tally-time
// equivocation exclusion (§7.1b) have real data, not an assumed-empty set.
// Pass nil only when no block-vote information is available (e.g. a receive-
// only relay path) — see the file-level MUTUAL EXCLUSION note for what that
// costs.
//
// No-op when TimeoutCertWiringEnabled is false, h is nil, or this node's own
// BLS key cannot be loaded — all logged, none fatal to the caller.
func MaybeStartTimeoutFlow(h host.Host, height uint64, blockVoters map[string]bool) {
	if !TimeoutCertWiringEnabled {
		return
	}
	if h == nil {
		log.Warn().Uint64("height", height).
			Msg("timeout flow: no host instance, cannot sign/broadcast a timeout vote")
		return
	}

	voterID := h.ID().String()
	if blockVoters[voterID] {
		// This node already cast a block vote for this round — §7.1b forbids
		// also casting a timeout vote for the same (height, period). This is
		// the self-application of the same rule TallyTimeoutVotes applies to
		// remote voters below.
		log.Warn().Uint64("height", height).Str("voter", voterID).
			Msg("timeout flow: local node already cast a block vote this round, refusing to also sign a timeout vote (§7.1b)")
		return
	}

	period := DefaultPeriodStore.PeriodFor(height) + 1

	priv, _, err := BLS_Signer.LocalBLSKeypair()
	if err != nil {
		log.Warn().Err(err).Uint64("height", height).
			Msg("timeout flow: could not load local BLS keypair, cannot sign timeout vote")
		return
	}

	vote, err := SignTimeoutVote(priv, voterID, BLS_Signer.DomainChainID(), height, period)
	if err != nil {
		log.Warn().Err(err).Uint64("height", height).Uint64("period", period).
			Msg("timeout flow: failed to sign timeout vote")
		return
	}

	log.Info().Uint64("height", height).Uint64("period", period).Str("voter", voterID).
		Msg("timeout flow: consensus not reached, signed and broadcasting timeout vote")

	recordAndMaybeCertify(h, vote, blockVoters)
	broadcastTimeoutVote(h, vote)
}

// recordAndMaybeCertify adds vote to the local collector and, if quorum is
// now reached for its round, builds, locally accepts, and broadcasts the
// resulting TimeoutCertificate.
func recordAndMaybeCertify(h host.Host, vote TimeoutVote, blockVoters map[string]bool) {
	key := timeoutRoundKey{vote.Height, vote.Period}
	votes := defaultTimeoutVoteCollector.add(vote)
	tryCertify(h, key, votes, blockVoters)
}

func tryCertify(h host.Host, key timeoutRoundKey, votes []TimeoutVote, blockVoters map[string]bool) {
	poolSize, pubKeys, err := timeoutVotingPool(key.Height)
	if err != nil {
		log.Warn().Err(err).Uint64("height", key.Height).
			Msg("timeout flow: could not resolve voting pool, deferring tally")
		return
	}

	timeoutVoters := make(map[string]bool, len(votes))
	for _, v := range votes {
		timeoutVoters[v.VoterID] = true
	}
	excluded := make(map[string]bool)
	if blockVoters != nil {
		equivocators := DetectTimeoutBlockVoteEquivocation(blockVoters, timeoutVoters)
		if len(equivocators) > 0 {
			RecordTimeoutBlockVoteEquivocation(equivocators)
			for _, p := range equivocators {
				excluded[p] = true
			}
			log.Warn().Uint64("height", key.Height).Uint64("period", key.Period).
				Strs("equivocators", equivocators).
				Msg("timeout flow: excluded voter(s) that also cast a block vote this round (§7.1b)")
		}
	}

	cert, ok, err := TallyTimeoutVotes(votes, key.Height, key.Period, poolSize, pubKeys, excluded)
	if err != nil {
		log.Warn().Err(err).Uint64("height", key.Height).Uint64("period", key.Period).
			Msg("timeout flow: tally failed")
		return
	}
	if !ok {
		return // quorum not reached yet — normal, not an error
	}

	if !defaultTimeoutVoteCollector.markCertified(key) {
		return // another concurrent call already certified this round
	}

	newPeriod, accepted, err := DefaultPeriodStore.AcceptTimeoutCertificate(*cert, poolSize, pubKeys)
	if err != nil || !accepted {
		log.Warn().Err(err).Uint64("height", key.Height).Uint64("period", key.Period).
			Msg("timeout flow: locally-built certificate was not accepted by PeriodStore")
		return
	}
	defaultTimeoutVoteCollector.recordCert(*cert)

	log.Info().Uint64("height", key.Height).Uint64("new_period", newPeriod).
		Int("signers", len(cert.SignerBitmap)).
		Msg("timeout flow: quorum reached, timeout certificate accepted, broadcasting")

	broadcastTimeoutCertificate(h, *cert)
}

// AcceptIncomingTimeoutCertificate verifies and accepts a TimeoutCertificate
// obtained directly — gossiped in, or (once built) fetched via a rejoin/
// catch-up RPC — without requiring this node to have collected the
// underlying votes itself. This is the mechanism that lets a restarted node
// jump straight to the right period: VerifyTimeoutCertificate's own
// contract is that a single certificate proves its entire prefix.
func AcceptIncomingTimeoutCertificate(cert TimeoutCertificate) (uint64, bool, error) {
	poolSize, pubKeys, err := timeoutVotingPool(cert.Height)
	if err != nil {
		return DefaultPeriodStore.PeriodFor(cert.Height), false, fmt.Errorf("accept timeout certificate: %w", err)
	}
	newPeriod, accepted, err := DefaultPeriodStore.AcceptTimeoutCertificate(cert, poolSize, pubKeys)
	if accepted {
		defaultTimeoutVoteCollector.recordCert(cert)
	}
	return newPeriod, accepted, err
}

// handleTimeoutVoteBroadcast processes an incoming TimeoutVote gossiped by
// another validator (dispatched from broadcast.go's HandleBroadcastStream).
// blockVoters is nil here — see the file-level MUTUAL EXCLUSION note for
// what that costs; the vote's own BLS signature is still fully verified by
// TallyTimeoutVotes before it can contribute to any certificate.
func handleTimeoutVoteBroadcast(h host.Host, msg BroadcastMessageStruct) {
	if !TimeoutCertWiringEnabled {
		return
	}
	var vote TimeoutVote
	if err := json.Unmarshal([]byte(msg.Data), &vote); err != nil {
		log.Warn().Err(err).Str("msg_id", msg.ID).Msg("timeout flow: failed to unmarshal gossiped timeout vote")
		return
	}
	recordAndMaybeCertify(h, vote, nil)
}

// handleTimeoutCertificateBroadcast processes an incoming TimeoutCertificate
// gossiped by another node.
func handleTimeoutCertificateBroadcast(h host.Host, msg BroadcastMessageStruct) {
	if !TimeoutCertWiringEnabled {
		return
	}
	var cert TimeoutCertificate
	if err := json.Unmarshal([]byte(msg.Data), &cert); err != nil {
		log.Warn().Err(err).Str("msg_id", msg.ID).Msg("timeout flow: failed to unmarshal gossiped timeout certificate")
		return
	}
	newPeriod, accepted, err := AcceptIncomingTimeoutCertificate(cert)
	if err != nil {
		log.Warn().Err(err).Uint64("height", cert.Height).Uint64("period", cert.Period).
			Msg("timeout flow: rejected gossiped timeout certificate")
		return
	}
	if accepted {
		log.Info().Uint64("height", cert.Height).Uint64("new_period", newPeriod).
			Msg("timeout flow: accepted gossiped timeout certificate")
	}
}

// broadcastTimeoutVote sends vote to every connected peer over the existing
// flood-broadcast transport (config.BroadcastProtocol — the same stream
// protocol BroadcastMessage/BroadcastVoteTrigger already use in
// broadcast.go). A send failure to an individual peer is logged, not fatal:
// gossip is best-effort and the vote still reaches the network via any peer
// that does connect, exactly like every other broadcast in this package.
func broadcastTimeoutVote(h host.Host, vote TimeoutVote) {
	data, err := json.Marshal(vote)
	if err != nil {
		log.Warn().Err(err).Msg("timeout flow: failed to marshal timeout vote for broadcast")
		return
	}
	sendTimeoutGossip(h, timeoutVoteBroadcastType, data)
}

// broadcastTimeoutCertificate sends cert to every connected peer, same
// transport as broadcastTimeoutVote.
func broadcastTimeoutCertificate(h host.Host, cert TimeoutCertificate) {
	data, err := json.Marshal(cert)
	if err != nil {
		log.Warn().Err(err).Msg("timeout flow: failed to marshal timeout certificate for broadcast")
		return
	}
	sendTimeoutGossip(h, timeoutCertBroadcastType, data)
}

// sendTimeoutGossip wraps data in a BroadcastMessageStruct of msgType and
// flood-sends it, reusing broadcast.go's existing message shape, message-ID
// scheme, and dedup cache (generateMessageID/markMessageSeen, same package)
// so a receiver's HandleBroadcastStream already knows how to dedup and
// re-flood it — this is a new msg.Type value on an existing transport, not a
// new one. The send loop itself is intentionally self-contained here rather
// than factored out of BroadcastMessage/BroadcastVoteTrigger: those two
// functions already duplicate this same loop independently, so a third
// copy matches this file's own existing convention rather than risking a
// shared-helper refactor of already-live broadcast code paths.
func sendTimeoutGossip(h host.Host, msgType string, data []byte) {
	if h == nil {
		return
	}
	now := time.Now().UTC()
	msg := BroadcastMessageStruct{
		Sender:    h.ID().String(),
		Content:   msgType + " broadcast",
		Timestamp: now.Unix(),
		Hops:      0,
		Type:      msgType,
		Data:      string(data),
	}
	msg.ID = generateMessageID(msg.Sender, msg.Content+string(data), now.Unix())
	markMessageSeen(msg.ID)

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		log.Warn().Err(err).Str("type", msgType).Msg("timeout flow: failed to marshal broadcast envelope")
		return
	}
	msgBytes = append(msgBytes, '\n')

	peers := h.Network().Peers()
	if len(peers) == 0 {
		log.Debug().Str("type", msgType).Msg("timeout flow: no connected peers to broadcast to")
		return
	}

	for _, pid := range peers {
		peerID := pid
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := h.NewStream(ctx, peerID, config.BroadcastProtocol)
			if err != nil {
				log.Warn().Err(err).Str("peer", peerID.String()).Str("type", msgType).
					Msg("timeout flow: failed to open broadcast stream")
				return
			}
			defer stream.Close()
			if _, err := stream.Write(msgBytes); err != nil {
				log.Warn().Err(err).Str("peer", peerID.String()).Str("type", msgType).
					Msg("timeout flow: failed to write broadcast message")
			}
		}()
	}
}
