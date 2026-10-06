package Structs

// IngestValidatorVote writes an incoming validator vote (received over the
// consensus pubsub channel or a direct stream) into the v2, block-keyed vote
// CRDT — the only keyspace the tally (ProcessVotesFromCRDT ->
// processVotesFromCRDT_v2 -> avcvotes.TallyBlock) reads. Before this, a vote
// that arrived here only ever reached the legacy CRDT (no longer tallied):
// only a buddy's OWN vote, written directly by Vote/Trigger.go's SubmitVote,
// ever entered VoteCRDTLayer. See "Count every validator's vote in the buddy
// tally again" LLD, change C3.
//
// Ingest is deliberately lossless and does not check the authorized voter
// set (that happens at tally time, in processVotesFromCRDT_v2 via
// authorizedVotersFor) — see the LLD's D5. Cost is bounded independently of
// authorization by AddVote's own per-peer cap (avcvotes.MaxElementsPerPeerPerBlock)
// and by the identity check below.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gossipnode/config/PubSubMessages"
	"gossipnode/metrics"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	avctypes "github.com/JupiterMetaLabs/avc/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ValidatorVoteIngestEnabled is the rollback switch for this change.
// JMDN_VALIDATOR_VOTE_INGEST=0 restores the pre-change behaviour (received
// votes land only in the legacy CRDT, exactly as before this LLD).
var ValidatorVoteIngestEnabled = envOnStructs("JMDN_VALIDATOR_VOTE_INGEST", true)

// ErrUnsignedVote is returned when the wire vote carries no BLS signature /
// public key. D1: every counted validator vote must be signed — there is no
// unsigned-vote ingest path here (unlike Vote/Trigger.go's own-vote write,
// which still has the avcvotes.AllowUnsignedValidatorVotes seam for a
// buddy's OWN vote). A received vote with no signature cannot be verified at
// tally time regardless, so there is no reason to spend a CRDT write on it.
var ErrUnsignedVote = errors.New("validator vote ingest: missing bls_signature/bls_pub_key")

// IngestValidatorVote decodes raw (a JSON-encoded PubSubMessages.Vote, the
// same wire format SubmitVote sends) and, if it is well-formed and signed,
// writes it into layer under sender. sender MUST be the transport-
// authenticated peer — the stream's RemotePeer() for a direct send, or
// msg.Sender (libp2p's authenticated msg.GetFrom()) for a pubsub delivery —
// never a payload field (D2). avcvotes.AddVote itself also rejects a
// mismatch between the authenticated identity and rec.PeerID, so this is
// belt-and-braces, not the only guard.
//
// path identifies the receive path purely for metrics.ValidatorVoteIngestCounter
// (§4c) — "direct" (ListenerHandler.go) or "pubsub" (subscriptionService.go).
// It has no effect on ingest behavior.
//
// A nil layer or disabled flag is a silent no-op (mirrors Vote/Trigger.go's
// own `listenerNode.VoteCRDTLayer != nil` guard) so callers do not need to
// duplicate that check.
func IngestValidatorVote(layer *avctypes.Controller, sender peer.ID, raw string, path string) error {
	if !ValidatorVoteIngestEnabled {
		metrics.ValidatorVoteIngestCounter.WithLabelValues("disabled", path).Inc()
		return nil
	}
	if layer == nil || sender == "" {
		metrics.ValidatorVoteIngestCounter.WithLabelValues("disabled", path).Inc()
		return nil
	}

	var v PubSubMessages.Vote
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		metrics.ValidatorVoteIngestCounter.WithLabelValues("invalid", path).Inc()
		return fmt.Errorf("validator vote ingest: decode: %w", err)
	}
	if v.Vote != 1 && v.Vote != -1 {
		metrics.ValidatorVoteIngestCounter.WithLabelValues("invalid", path).Inc()
		return fmt.Errorf("validator vote ingest: invalid vote value %d", v.Vote)
	}
	if v.BlockHash == "" || v.Height == 0 {
		metrics.ValidatorVoteIngestCounter.WithLabelValues("invalid", path).Inc()
		return fmt.Errorf("validator vote ingest: missing block_hash/height")
	}
	if v.BLSSignature == "" || v.BLSPubKeyHex == "" {
		metrics.ValidatorVoteIngestCounter.WithLabelValues("unsigned", path).Inc()
		return ErrUnsignedVote
	}

	rec := avcvotes.VoteRecord{
		PeerID:          sender.String(),
		Vote:            v.Vote,
		BlockHash:       v.BlockHash,
		Height:          v.Height,
		BLSSignature:    v.BLSSignature,
		BLSPubKeyHex:    strings.ToLower(strings.TrimSpace(v.BLSPubKeyHex)),
		RejectionReason: v.RejectionReason,
	}

	err := avcvotes.AddVote(layer, sender, rec)
	switch {
	case err == nil:
		// "stored", not "counted": a vote only ever becomes a COUNTED one at
		// tally time (processVotesFromCRDT_v2 -> avcvotes.TallyBlock), and
		// only if sender is in that tally's authorized voter set. This label
		// means only "the write to VoteCRDTLayer succeeded."
		metrics.ValidatorVoteIngestCounter.WithLabelValues("stored", path).Inc()
		return nil
	case errors.Is(err, avcvotes.ErrHeightCompacted):
		// Expected, not a bug: a late vote for an already-converged height.
		metrics.ValidatorVoteIngestCounter.WithLabelValues("compacted", path).Inc()
		return nil
	default:
		metrics.ValidatorVoteIngestCounter.WithLabelValues("write_error", path).Inc()
		return err
	}
}
