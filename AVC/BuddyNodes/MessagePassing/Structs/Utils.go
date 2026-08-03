package Structs

import (
	"context"
	"encoding/json"
	"errors"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	voteaggregation "gossipnode/AVC/VoteModule"
	Publisher "gossipnode/Pubsub/Publish"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"gossipnode/config/settings"
	"gossipnode/seednode"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
)

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

// ProcessVotesFromCRDT extracts votes from CRDT, filters them by block hash,
// processes them through votemodule, and returns the aggregated result and per-peer rejection reasons.
// targetBlockHash is required - votes without matching block_hash are skipped.
// The second return value maps peerID → rejection_reason for peers that voted -1.
func ProcessVotesFromCRDT(logger_ctx context.Context, listenerNode *PubSubMessages.BuddyNode, targetBlockHash string) (int8, map[string]string, error) {
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		logger().Error(logger_ctx, "Listener node or CRDT layer not initialized", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, nil, errors.New("listener node or CRDT layer not initialized")
	}

	if targetBlockHash == "" {
		logger().Error(logger_ctx, "TargetBlockHash is required for vote processing to avoid mixing votes from different blocks", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, nil, errors.New("targetBlockHash is required for vote processing to avoid mixing votes from different blocks")
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
