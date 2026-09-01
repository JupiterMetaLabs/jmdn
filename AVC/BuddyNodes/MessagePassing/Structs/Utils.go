package Structs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	voteaggregation "gossipnode/AVC/VoteModule"
	Publisher "gossipnode/Pubsub/Publish"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"gossipnode/config/settings"
	"gossipnode/seednode"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
)

// voteCRDTV2Enabled mirrors Vote.VoteCRDTDualWrite (Vote/vote_crdt_v2.go) —
// same env var, same default. Duplicated rather than imported: Vote ->
// MessagePassing -> Structs already exists (Vote/Trigger.go imports
// MessagePassing; MessagePassing/ListenerHandler.go imports Structs), so
// Structs -> Vote would be an import cycle. The two must never disagree:
// Stage 4's entire revert story (docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md §10 —
// "readers -> TallyBlock | high | flag off") depends on the read side and
// the write side flipping together. This duplication pattern (an env-flag
// helper copied per package rather than shared) already exists in Security,
// messaging, Vote, and internal/reputation — see Vote/vote_crdt_v2.go's own
// comment on envOn.
var voteCRDTV2Enabled = envOnStructs("JMDN_VOTE_CRDT_V2", false)

func envOnStructs(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

type UtilsBuddyNode struct {
	BuddyNode *PubSubMessages.BuddyNode
}

// GetBuddyNodes returns a copy of the current buddy nodes list
func (buddy *UtilsBuddyNode) GetBuddyNodes() []peer.ID {
	buddy.BuddyNode.Mutex.RLock()
	defer buddy.BuddyNode.Mutex.RUnlock()

	nodes := make([]peer.ID, len(buddy.BuddyNode.BuddyNodes.Buddies_Nodes))
	copy(nodes, buddy.BuddyNode.BuddyNodes.Buddies_Nodes)
	return nodes
}

// GetBuddyNodesCount returns the number of buddy nodes (excluding self)
func (buddy *UtilsBuddyNode) GetBuddyNodesCount() int {
	buddy.BuddyNode.Mutex.RLock()
	defer buddy.BuddyNode.Mutex.RUnlock()

	count := 0
	for _, peerID := range buddy.BuddyNode.BuddyNodes.Buddies_Nodes {
		if peerID != buddy.BuddyNode.PeerID {
			count++
		}
	}
	return count
}

// GetMetadata returns a copy of the current metadata
func (buddy *UtilsBuddyNode) GetMetadata() PubSubMessages.MetaData {
	buddy.BuddyNode.Mutex.RLock()
	defer buddy.BuddyNode.Mutex.RUnlock()
	return PubSubMessages.MetaData{
		Received:  buddy.BuddyNode.MetaData.Received,
		Sent:      buddy.BuddyNode.MetaData.Sent,
		Total:     buddy.BuddyNode.MetaData.Total,
		UpdatedAt: buddy.BuddyNode.MetaData.UpdatedAt,
	}
}

func SubmitMessage(logger_ctx context.Context, msg *PubSubMessages.Message, PubSub *PubSubMessages.GossipPubSub, ListenerNode *PubSubMessages.BuddyNode) error {
	// Check if this is a vote message
	var voteData map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Message), &voteData); err != nil {
		logger().Error(logger_ctx, "Failed to unmarshal vote message", err,
			ion.String("function", "Structs.SubmitMessage"))
		return errors.New("failed to unmarshal vote message: %v")
	}

	// Check if this is a vote message by looking for vote field
	if _, exists := voteData["vote"]; exists {

		// Create OP struct for vote
		OP := &Types.OP{
			NodeID: msg.Sender,
			OpType: int8(1), // 1 for add, -1 for remove
			KeyValue: Types.KeyValue{
				Key:   msg.Sender.String(), // key would be the peer id of the sender
				Value: msg.Message,         // Store the full vote message as value
			},
		}

		// Adding data to the CRDT First - Before PubSub
		if err := ServiceLayer.Controller(ListenerNode.CRDTLayer, OP); err != nil {
			logger().Error(logger_ctx, "Failed to add vote to local CRDT Engine", err.(error),
				ion.String("function", "Structs.SubmitMessage"))
			return errors.New("failed to add vote to local CRDT Engine: " + err.(error).Error())
		}
	} else {
		// This is a regular message, try to unmarshal as OP
		OP := &Types.OP{}
		if err := json.Unmarshal([]byte(msg.Message), OP); err != nil {
			logger().Error(logger_ctx, "Failed to unmarshal message", err,
				ion.String("function", "Structs.SubmitMessage"))
			return errors.New("failed to unmarshal message: " + err.Error())
		}

		// Adding data to the CRDT First - Before PubSub
		if err := ServiceLayer.Controller(ListenerNode.CRDTLayer, OP); err != nil {
			logger().Error(logger_ctx, "Failed to add vote to local CRDT Engine", err.(error),
				ion.String("function", "Structs.SubmitMessage"))
			return errors.New("failed to add vote to local CRDT Engine: " + err.(error).Error())
		}
	}

	// Now Submit to the publish function in the pubsub using config.PubSub_ConsensusChannel
	if err := Publisher.Publish(logger_ctx, PubSub, config.PubSub_ConsensusChannel, msg, map[string]string{}); err != nil {
		logger().Error(logger_ctx, "Failed to publish message to pubsub", err,
			ion.String("function", "Structs.SubmitMessage"))
		return errors.New("failed to publish message to pubsub: %v")
	}
	return nil
}

// ProcessVotesFromCRDT extracts votes for one block and returns the
// aggregated decision (1 accept / -1 reject) and per-peer rejection reasons.
// targetBlockHash is required - votes without matching block_hash are skipped.
// The second return value maps peerID -> rejection_reason for peers that voted -1.
//
// Stage 4 (JMDN-CRDT-VOTE-MIGRATION-LLD.md §6): gated by the same
// JMDN_VOTE_CRDT_V2 flag as the write side (Vote.VoteCRDTDualWrite) so this
// stage stays revertible by a single flag flip, per the LLD's §10 build-order
// table ("4 | readers -> TallyBlock | high | flag off"):
//   - flag OFF (default today, since Stage 2's dual-write also defaults
//     off): legacy peer-keyed read, UNCHANGED from before Stage 4 — reads
//     listenerNode.CRDTLayer, decides via the seed-node-weighted
//     voteaggregation.VoteAggregation. This remains the only path that runs
//     in production until the fleet flips JMDN_VOTE_CRDT_V2 on.
//   - flag ON: new block-keyed read via avcvotes.TallyBlock against
//     listenerNode.VoteCRDTLayer, decided by the unweighted
//     voteaggregation.MajorityDecision (Gap 2 — reputation weight must never
//     multiply an already-cast vote) and preserving RejectionReason per peer
//     from the typed VoteRecord instead of an untyped map (Gap 1).
//
// height is now a required parameter (it was not before Stage 4) because
// TallyBlock needs it and every call site has it available; threaded
// unconditionally on both the legacy and v2 paths so no call site carries
// two different signatures depending on the flag.
func ProcessVotesFromCRDT(logger_ctx context.Context, listenerNode *PubSubMessages.BuddyNode, targetBlockHash string, height uint64) (int8, map[string]string, *VoteCertificate, *avcvotes.VoteCertificate, error) {
	if listenerNode == nil {
		logger().Error(logger_ctx, "Listener node not initialized", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, nil, nil, nil, errors.New("listener node not initialized")
	}

	if targetBlockHash == "" {
		logger().Error(logger_ctx, "TargetBlockHash is required for vote processing to avoid mixing votes from different blocks", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, nil, nil, nil, errors.New("targetBlockHash is required for vote processing to avoid mixing votes from different blocks")
	}

	if voteCRDTV2Enabled {
		return processVotesFromCRDT_v2(logger_ctx, listenerNode, targetBlockHash, height)
	}
	// Legacy path has no per-vote BLS signature to aggregate (types.Vote/
	// PubSubMessages.Vote carries no signature field), so it never produces a
	// certificate — nil, not an empty one, since "no certificate available"
	// and "certificate with zero signers" are different states.
	result, rejectionReasons, err := processVotesFromCRDT_legacy(logger_ctx, listenerNode, targetBlockHash)
	return result, rejectionReasons, nil, nil, err
}

// processVotesFromCRDT_v2 is the Stage 4 read path: block-keyed CRDT via
// avcvotes.TallyBlock, unweighted majority via MajorityDecision (Gap 2),
// RejectionReason preserved per peer (Gap 1). Only reachable when
// voteCRDTV2Enabled is true — see ProcessVotesFromCRDT's doc comment.
func processVotesFromCRDT_v2(logger_ctx context.Context, listenerNode *PubSubMessages.BuddyNode, targetBlockHash string, height uint64) (int8, map[string]string, *VoteCertificate, *avcvotes.VoteCertificate, error) {
	if listenerNode.VoteCRDTLayer == nil {
		logger().Error(logger_ctx, "Vote CRDT layer not initialized (v2 path)", nil,
			ion.String("function", "Structs.processVotesFromCRDT_v2"))
		return 0, nil, nil, nil, errors.New("vote CRDT layer not initialized")
	}

	authorized, err := authorizedCommittee()
	if err != nil {
		logger().Error(logger_ctx, "Failed to resolve authorized committee (v2 path)", err,
			ion.String("function", "Structs.processVotesFromCRDT_v2"))
		return 0, nil, nil, nil, err
	}

	tally, err := avcvotes.TallyBlock(listenerNode.VoteCRDTLayer, height, targetBlockHash, authorized)
	if err != nil {
		logger().Error(logger_ctx, "TallyBlock failed (v2 path)", err,
			ion.String("target_block_hash", targetBlockHash),
			ion.String("function", "Structs.processVotesFromCRDT_v2"))
		return 0, nil, nil, nil, err
	}

	// Stage 5 (JMDN-CRDT-VOTE-MIGRATION-LLD.md §7): TallyBlock authenticates
	// (the voter's claimed pubkey matches the committee record) but does not
	// cryptographically verify the BLS signature itself — that division of
	// labor is documented on TallyBlock. Drop every (peer, value) pair whose
	// signature does not actually verify before anything downstream counts
	// it or reports it as an equivocation, so a forged element can never be
	// counted and can never manufacture a false equivocation charge against
	// a real peer.
	verified, droppedForgeries := verifyTallySignatures(tally, BLS_Signer.DomainChainID(), height, targetBlockHash)
	if droppedForgeries > 0 {
		logger().Error(logger_ctx, "Dropped votes with invalid BLS signatures (v2 path)", nil,
			ion.Int("dropped", droppedForgeries),
			ion.String("target_block_hash", targetBlockHash),
			ion.String("function", "Structs.processVotesFromCRDT_v2"))
	}
	tally = verified

	// Equivocation reporting (reputation side-effect) is an A4 concern,
	// explicitly deferred by the user ("later we will think of the A4
	// reputation weighting"). reporter == nil is a valid, documented no-op
	// (avc/crdt/votes/equivocation.go) — verdicts are still computed and
	// faulted peers are still excluded from SingleVotePeers() below; only
	// the reputation write is skipped for now.
	avcvotes.ApplyEquivocationPolicy(tally, targetBlockHash, height, nil)

	single := tally.SingleVotePeers()

	// Gap 1: recover RejectionReason per -1 voter from the typed VoteRecord
	// backing that peer's single counted vote. Equivocating peers (2+
	// distinct values) are excluded from `single` already, so they never
	// reach here — an equivocator's "reason" would be ambiguous anyway
	// (which of its conflicting votes would it belong to).
	rejectionReasons := make(map[string]string, len(single))
	for peerID, voteVal := range single {
		if voteVal != -1 {
			continue
		}
		for _, rec := range tally.Signatures[peerID] {
			if rec.Vote == -1 && rec.RejectionReason != "" {
				rejectionReasons[peerID] = rec.RejectionReason
				break
			}
		}
	}

	if len(single) == 0 {
		logger().Error(logger_ctx, "No authorized single-vote peers found in vote CRDT (v2 path)", nil,
			ion.String("target_block_hash", targetBlockHash),
			ion.String("function", "Structs.processVotesFromCRDT_v2"))
		return 0, rejectionReasons, nil, nil, errors.New("no votes found in CRDT")
	}

	// Phase 1.5 (VALIDATOR-SCALE-VOTE-AGGREGATION-LLD.md §12.5): aggregate the
	// YES voters' already-verified signatures into a certificate, carried as
	// additional evidence. Best-effort and non-fatal — a failure here must
	// never fail the vote decision itself, since the sequencer does not act
	// on this yet (deliberately deferred; see the doc's exit list).
	cert, certErr := buildVoteCertificate(tally, single)
	if certErr != nil {
		logger().Error(logger_ctx, "Failed to build vote certificate (v2 path, non-fatal)", certErr,
			ion.String("target_block_hash", targetBlockHash),
			ion.String("function", "Structs.processVotesFromCRDT_v2"))
	}

	// §5 (VALIDATOR-SCALE-VOTE-AGGREGATION-LLD.md): the bitmap-capable,
	// full-validator-scale certificate (§4/§6, avcvotes.BuildVoteCertificate),
	// alongside Phase 1.5's simpler signer-list one above — not a replacement
	// for it. Reuses `authorized`, the same eligible set already resolved for
	// TallyBlock above, as SnapshotOrder's input: today that's the 7-buddy
	// committee, so this exercises the real §0->§4 pipeline end-to-end now,
	// safely, without needing pinning — nothing downstream reads or depends
	// on this value yet. Best-effort and non-fatal, same discipline as the
	// certificate above: a failure here must never fail the vote decision.
	var validatorCert *avcvotes.VoteCertificate
	_, index := avcvotes.SnapshotOrder(authorized)
	if built, validatorCertErr := avcvotes.BuildVoteCertificate(single, tally.Signatures, index); validatorCertErr != nil {
		logger().Error(logger_ctx, "Failed to build validator-scale vote certificate (v2 path, non-fatal)", validatorCertErr,
			ion.String("target_block_hash", targetBlockHash),
			ion.String("function", "Structs.processVotesFromCRDT_v2"))
	} else {
		validatorCert = &built
	}

	// Gap 2: plain majority over authorized, non-equivocating votes — no
	// weight parameter. Reputation/stake weight must never multiply an
	// already-cast validator vote; see MajorityDecision's doc
	// (AVC/VoteModule/vote_validation.go) for why.
	accepted, err := voteaggregation.MajorityDecision(single)
	if err != nil {
		logger().Error(logger_ctx, "MajorityDecision failed (v2 path)", err,
			ion.String("function", "Structs.processVotesFromCRDT_v2"))
		return 0, rejectionReasons, cert, validatorCert, err
	}

	logger().Debug(logger_ctx, "Vote decision (v2 path)",
		ion.Bool("accepted", accepted),
		ion.Int("single_vote_peers", len(single)),
		ion.String("function", "Structs.processVotesFromCRDT_v2"))

	if accepted {
		return 1, rejectionReasons, cert, validatorCert, nil
	}
	return -1, rejectionReasons, cert, validatorCert, nil
}

// verifyTallySignatures is Stage 5 (JMDN-CRDT-VOTE-MIGRATION-LLD.md §7): it
// re-verifies the BLS signature backing every (peer, value) pair TallyBlock
// authenticated, and returns a tally containing only the pairs that
// actually verify, plus how many were dropped.
//
// Every pair is checked, not just single-vote peers: an equivocating peer's
// two conflicting values must BOTH verify before either counts as real
// evidence — otherwise a single forged element under a real committee
// member's peer ID could manufacture a false equivocation charge against
// them once ApplyEquivocationPolicy runs. "Only verify votes you are
// counting" (the LLD's own CPU-DoS caution) still holds: this only ever
// verifies what TallyBlock already authenticated against the committee
// snapshot, bounded by the same maxElementsPerPeerPerBlock ingest cap
// AddVote enforces — never every element ever written.
//
// AuthorizedVotesByPeer[peerID][i] and Signatures[peerID][i] are written in
// lockstep by TallyBlock (same append, same loop iteration), so indexing
// both by i is safe by construction, not by convention.
func verifyTallySignatures(tally avcvotes.BlockTally, chainID, height uint64, blockHash string) (verified avcvotes.BlockTally, dropped int) {
	verified = avcvotes.BlockTally{
		AuthorizedVotesByPeer: make(map[string][]int8, len(tally.AuthorizedVotesByPeer)),
		Signatures:            make(map[string][]avcvotes.VoteRecord, len(tally.Signatures)),
		SkippedUnauthorized:   tally.SkippedUnauthorized,
		MalformedVotes:        tally.MalformedVotes,
		MalformedSignatures:   tally.MalformedSignatures,
	}

	// Flatten every (peerID, value, record) pair into an independent task
	// list first. Each pair's outcome depends ONLY on its own inputs to
	// BLS_Verifier.VerifyForBlock (chainID/height/blockHash/vote/pubkey/sig
	// are all copied into the task, nothing is shared with any other pair),
	// so verifying them concurrently changes nothing about WHAT is checked
	// — only the order/timing of when each check runs. The i>=len(recs)
	// mismatch case is not a crypto op, so it is still counted inline here,
	// exactly as before.
	tasks := make([]tallySigTask, 0, len(tally.Signatures))
	for peerID, values := range tally.AuthorizedVotesByPeer {
		recs := tally.Signatures[peerID]
		for i, v := range values {
			if i >= len(recs) {
				// TallyBlock never produces this — Signatures[peerID] is
				// appended in the same iteration as AuthorizedVotesByPeer[peerID]
				// — but a missing record can't be verified either way, so
				// drop it rather than assume it is valid.
				dropped++
				continue
			}
			tasks = append(tasks, tallySigTask{
				peerID: peerID,
				vote:   v,
				rec:    recs[i],
				// Evaluated here, once, on the single-threaded task-building
				// pass — not inside a worker — so the flag is read at a
				// deterministic point and every worker sees a fixed decision.
				unsigned: avcvotes.AllowUnsignedValidatorVotes && avcvotes.IsUnsignedValidatorVote(recs[i]),
			})
		}
	}

	verifiedOK := verifyTallySigTasksConcurrently(tasks, chainID, height, blockHash)

	// Reduction is single-threaded and runs strictly after every worker has
	// returned (verifyTallySigTasksConcurrently blocks on its WaitGroup) —
	// tally.AuthorizedVotesByPeer / tally.Signatures are never written to
	// from more than one goroutine, and are built here in a fixed order
	// (task list order, not goroutine completion order), so the resulting
	// maps' CONTENT is identical every run for identical input regardless
	// of how the scheduler interleaves the workers.
	for i, task := range tasks {
		if !verifiedOK[i] {
			dropped++
			continue
		}
		verified.AuthorizedVotesByPeer[task.peerID] = append(verified.AuthorizedVotesByPeer[task.peerID], task.vote)
		verified.Signatures[task.peerID] = append(verified.Signatures[task.peerID], task.rec)
	}

	return verified, dropped
}

// tallySigTask is one independently-verifiable (peer, vote, record) pair —
// the unit of work verifyTallySigTasksConcurrently distributes across its
// bounded worker pool.
type tallySigTask struct {
	peerID string
	vote   int8
	rec    avcvotes.VoteRecord

	// unsigned marks a task admitted through the unsigned normal-validator
	// seam (avcvotes.AllowUnsignedValidatorVotes, default off): there is no
	// signature to verify, so the worker must not call VerifyForBlock with an
	// empty signature and count the inevitable failure as a dropped forgery.
	// Always false with the flag off, which keeps the pre-seam behavior
	// (unsigned records reach the verifier, fail, and are dropped) intact.
	unsigned bool
}

// verifyTallySignaturesWorkers bounds the worker pool used by
// verifyTallySigTasksConcurrently. BLS verification (BLS_Verifier.VerifyForBlock)
// is pure CPU-bound work with no I/O to overlap, so GOMAXPROCS is the natural
// default — more workers than cores cannot do more work per wall-clock
// second, only add scheduling overhead. Var (not const) so tests and
// benchmarks can override it to measure different worker counts, per the
// requirement to actually measure rather than assume a speedup.
var verifyTallySignaturesWorkers = runtime.GOMAXPROCS(0)

// verifyTallySigTasksConcurrently verifies every task's BLS signature on a
// bounded worker pool and returns, for each task index, whether it verified.
// The number of workers spawned is min(verifyTallySignaturesWorkers,
// len(tasks)) — never more goroutines than there is work, and zero
// goroutines at all for an empty task list — satisfying "bounded" in both
// directions, not just an upper cap.
//
// Race-safety by construction, not by locking: `tasks` is read-only for the
// whole call (built once, before any goroutine starts) and `results` is
// written by index, with each index owned by exactly ONE task/goroutine —
// no two goroutines ever write the same slice element, so no mutex is
// needed on `results` itself. The only synchronization is the WaitGroup
// gating the caller's read of `results` until every writer has finished,
// which is what makes those non-overlapping writes safe to read afterward
// under the Go memory model. Intended to be verified with `go test -race`.
func verifyTallySigTasksConcurrently(tasks []tallySigTask, chainID, height uint64, blockHash string) []bool {
	results := make([]bool, len(tasks))
	if len(tasks) == 0 {
		return results
	}

	workers := verifyTallySignaturesWorkers
	if workers < 1 {
		workers = 1
	}
	if workers > len(tasks) {
		workers = len(tasks)
	}

	jobs := make(chan int, len(tasks))
	for i := range tasks {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for idx := range jobs {
				task := tasks[idx]
				if task.unsigned {
					// Nothing to verify by design (unsigned normal-validator
					// vote). Admitted, not "verified" — the authorization
					// decision for these already happened in TallyBlock's
					// seam; re-deriving it here would duplicate that policy in
					// a second place.
					results[idx] = true
					continue
				}
				resp := BLS_Signer.BLSresponse{PeerID: task.peerID, PubKey: task.rec.BLSPubKeyHex, Signature: task.rec.BLSSignature}
				results[idx] = BLS_Verifier.VerifyForBlock(resp, chainID, height, blockHash, task.vote) == nil
			}
		}()
	}
	wg.Wait()

	return results
}

// processVotesFromCRDT_legacy is the pre-Stage-4 read path, byte-identical
// in behavior to ProcessVotesFromCRDT before this stage. Kept verbatim (not
// deleted) so JMDN_VOTE_CRDT_V2=off — the default — is a true no-op change,
// per the LLD's revertibility requirement. Do not add Stage 4 concepts
// (RejectionReason typing, MajorityDecision, TallyBlock) here; that would
// defeat the point of keeping a flag-off path.
func processVotesFromCRDT_legacy(logger_ctx context.Context, listenerNode *PubSubMessages.BuddyNode, targetBlockHash string) (int8, map[string]string, error) {
	if listenerNode.CRDTLayer == nil {
		logger().Error(logger_ctx, "Listener node or CRDT layer not initialized", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, nil, errors.New("listener node or CRDT layer not initialized")
	}

	logger().Info(logger_ctx, "Processing votes from CRDT for voting",
		ion.String("target_block_hash", targetBlockHash),
		ion.String("function", "Structs.ProcessVotesFromCRDT"))

	// Get all CRDTs to find all keys that might contain votes
	allCRDTs := listenerNode.CRDTLayer.CRDTLayer.GetAllCRDTs()
	logger().Info(logger_ctx, "Found CRDT keys in storage",
		ion.Int("count", len(allCRDTs)),
		ion.String("function", "Structs.ProcessVotesFromCRDT"))

	// Map to store peer_id -> vote value, block hash, and optional rejection reason
	type peerVote struct {
		vote            int8
		blockHash       string
		rejectionReason string
	}
	voteData := make(map[string]peerVote)

	// Iterate through all CRDT keys
	for key := range allCRDTs {
		votes, exists := DataLayer.GetSet(listenerNode.CRDTLayer, key)
		logger().Info(logger_ctx, "Key exists in CRDT",
			ion.String("key", key),
			ion.Bool("exists", exists),
			ion.String("function", "Structs.ProcessVotesFromCRDT"))

		if !exists || len(votes) == 0 {
			continue
		}

		// Parse each vote and extract vote value
		for _, voteStr := range votes {
			var voteDataObj map[string]interface{}
			if err := json.Unmarshal([]byte(voteStr), &voteDataObj); err != nil {
				logger().Error(logger_ctx, "Failed to parse vote", err,
					ion.String("vote_str", voteStr),
					ion.String("function", "Structs.ProcessVotesFromCRDT"))
				continue
			}

			// Check if this is a vote message
			voteValueRaw, isVote := voteDataObj["vote"]
			if !isVote {
				continue
			}

			voteValue, ok := voteValueRaw.(float64)
			if !ok {
				logger().Error(logger_ctx, "Invalid vote value type", nil,
					ion.String("vote_value_raw", voteValueRaw.(string)),
					ion.String("function", "Structs.ProcessVotesFromCRDT"))
				continue
			}

			blockHashRaw, hasBlockHash := voteDataObj["block_hash"]
			blockHash, blockHashOK := blockHashRaw.(string)

			// Require matching block hash (targetBlockHash is always required now)
			if !hasBlockHash || !blockHashOK {
				logger().Debug(logger_ctx, "Skipping peer vote without block_hash while targeting",
					ion.String("key", key),
					ion.String("target_block_hash", targetBlockHash),
					ion.String("function", "Structs.ProcessVotesFromCRDT"))
				continue
			}
			if blockHash != targetBlockHash {
				logger().Debug(logger_ctx, "Skipping peer vote for block_hash",
					ion.String("key", key),
					ion.String("block_hash", blockHash),
					ion.String("target_block_hash", targetBlockHash),
					ion.String("function", "Structs.ProcessVotesFromCRDT"))
				continue
			}

			// Extract optional rejection reason (present when vote == -1)
			rejectionReason := ""
			if r, ok := voteDataObj["rejection_reason"].(string); ok {
				rejectionReason = r
			}

			// Use the key (which is the peer ID) to store the latest vote for that block
			voteData[key] = peerVote{
				vote:            int8(voteValue),
				blockHash:       blockHash,
				rejectionReason: rejectionReason,
			}
			logger().Debug(logger_ctx, "Added vote for peer",
				ion.String("key", key),
				ion.Int("vote_value", int(voteValue)),
				ion.String("block_hash", blockHash),
				ion.String("function", "Structs.ProcessVotesFromCRDT"))
		}
	}

	if len(voteData) == 0 {
		logger().Error(logger_ctx, "No votes found in CRDT to process", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, nil, errors.New("no votes found in CRDT")
	}

	// Get peer weights from seed node
	client, err := seednode.NewClient(settings.Get().Network.SeedNode)
	if err != nil {
		logger().Error(logger_ctx, "Failed to create seed node client", err,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, nil, errors.New("failed to create seed node client: " + err.Error())
	}
	// seednode.Client owns a grpc.ClientConn. This runs once per vote-aggregation
	// round, so without the close the buddy accumulates a connection (and its
	// goroutines and file descriptor) every round. Safe as a defer: this is at
	// function-body scope, past both CRDT loops above.
	defer client.Close()

	weights, err := client.ListWeightsofPeers()
	if err != nil {
		// The seed enforces sequencer-only auth on the peer-list read. A buddy is
		// NOT the sequencer, so it cannot fetch weights — but it still must
		// aggregate and sign, or consensus stalls. Fall back to EQUAL weights (1.0
		// per voting peer) instead of aborting. The authoritative committee
		// membership / 2f+1 check still runs on the sequencer's VerifyCertificate.
		// A follow-up would allow committee members to read the peer list on the
		// seed.
		logger().Warn(logger_ctx, "Peer weights unavailable from seed; falling back to EQUAL weights for aggregation",
			ion.String("error", err.Error()),
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		weights = nil
	}

	// Filter weights to only include peers that voted; collect rejection reasons.
	// When weights are unavailable (seed denied the read), use equal weight 1.0.
	filteredWeights := make(map[string]float64)
	filteredVoteData := make(map[string]int8)
	rejectionReasons := make(map[string]string)
	for peerID, vote := range voteData {
		weight := 1.0
		exists := true
		if weights != nil {
			weight, exists = weights[peerID]
		}
		if exists {
			filteredVoteData[peerID] = vote.vote
			filteredWeights[peerID] = weight
			if vote.vote == -1 && vote.rejectionReason != "" {
				rejectionReasons[peerID] = vote.rejectionReason
			}
			logger().Debug(logger_ctx, "Peer has weight and vote",
				ion.String("peer_id", peerID),
				ion.Float64("weight", weight),
				ion.Int("vote", int(vote.vote)),
				ion.String("block_hash", vote.blockHash),
				ion.String("function", "Structs.ProcessVotesFromCRDT"))
		} else {
			logger().Debug(logger_ctx, "Peer not found in weights, skipping",
				ion.String("peer_id", peerID),
				ion.String("function", "Structs.ProcessVotesFromCRDT"))
		}
	}

	if len(filteredVoteData) == 0 {
		logger().Error(logger_ctx, "No votes found after filtering by weights", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, nil, errors.New("no votes found after filtering by weights")
	}

	// Call votemodule.VoteAggregation with filtered maps
	result, err := voteaggregation.VoteAggregation(filteredWeights, filteredVoteData)
	if err != nil {
		logger().Error(logger_ctx, "Failed to aggregate votes", err,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, nil, errors.New("failed to aggregate votes: " + err.Error())
	}

	logger().Debug(logger_ctx, "Vote aggregation result",
		ion.Bool("result", result),
		ion.String("function", "Structs.ProcessVotesFromCRDT"))

	// Convert boolean result to int8
	if result {
		return 1, rejectionReasons, nil
	} else {
		return -1, rejectionReasons, nil
	}
}
