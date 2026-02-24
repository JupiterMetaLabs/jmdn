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
		logger().NamedLogger.Error(logger_ctx, "Failed to unmarshal vote message", err,
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
			logger().NamedLogger.Error(logger_ctx, "Failed to add vote to local CRDT Engine", err.(error),
				ion.String("function", "Structs.SubmitMessage"))
			return errors.New("failed to add vote to local CRDT Engine: " + err.(error).Error())
		}
	} else {
		// This is a regular message, try to unmarshal as OP
		OP := &Types.OP{}
		if err := json.Unmarshal([]byte(msg.Message), OP); err != nil {
			logger().NamedLogger.Error(logger_ctx, "Failed to unmarshal message", err,
				ion.String("function", "Structs.SubmitMessage"))
			return errors.New("failed to unmarshal message: " + err.(error).Error())
		}

		// Adding data to the CRDT First - Before PubSub
		if err := ServiceLayer.Controller(ListenerNode.CRDTLayer, OP); err != nil {
			logger().NamedLogger.Error(logger_ctx, "Failed to add vote to local CRDT Engine", err.(error),
				ion.String("function", "Structs.SubmitMessage"))
			return errors.New("failed to add vote to local CRDT Engine: " + err.(error).Error())
		}
	}

	// Now Submit to the publish function in the pubsub using config.PubSub_ConsensusChannel
	if err := Publisher.Publish(logger_ctx, PubSub, config.PubSub_ConsensusChannel, msg, map[string]string{}); err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to publish message to pubsub", err,
			ion.String("function", "Structs.SubmitMessage"))
		return errors.New("failed to publish message to pubsub: %v")
	}
	return nil
}

// ProcessVotesFromCRDT extracts votes from CRDT, filters them by block hash,
// processes them through votemodule, and returns the aggregated result.
// targetBlockHash is required - votes without matching block_hash are skipped.
func ProcessVotesFromCRDT(logger_ctx context.Context, listenerNode *PubSubMessages.BuddyNode, targetBlockHash string) (int8, error) {
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		logger().NamedLogger.Error(logger_ctx, "Listener node or CRDT layer not initialized", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, errors.New("listener node or CRDT layer not initialized")
	}

	if targetBlockHash == "" {
		logger().NamedLogger.Error(logger_ctx, "TargetBlockHash is required for vote processing to avoid mixing votes from different blocks", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, errors.New("targetBlockHash is required for vote processing to avoid mixing votes from different blocks")
	}

	logger().NamedLogger.Info(logger_ctx, "Processing votes from CRDT for voting",
		ion.String("target_block_hash", targetBlockHash),
		ion.String("function", "Structs.ProcessVotesFromCRDT"))

	// Get all CRDTs to find all keys that might contain votes
	allCRDTs := listenerNode.CRDTLayer.CRDTLayer.GetAllCRDTs()
	logger().NamedLogger.Info(logger_ctx, "Found CRDT keys in storage",
		ion.Int("count", len(allCRDTs)),
		ion.String("function", "Structs.ProcessVotesFromCRDT"))

	// Map to store peer_id -> vote value and the block hash it applies to
	type peerVote struct {
		vote      int8
		blockHash string
	}
	voteData := make(map[string]peerVote)

	// Iterate through all CRDT keys
	for key := range allCRDTs {
		votes, exists := DataLayer.GetSet(listenerNode.CRDTLayer, key)
		logger().NamedLogger.Info(logger_ctx, "Key exists in CRDT",
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
				logger().NamedLogger.Error(logger_ctx, "Failed to parse vote", err,
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
				logger().NamedLogger.Error(logger_ctx, "Invalid vote value type", nil,
					ion.String("vote_value_raw", voteValueRaw.(string)),
					ion.String("function", "Structs.ProcessVotesFromCRDT"))
				continue
			}

			blockHashRaw, hasBlockHash := voteDataObj["block_hash"]
			blockHash, blockHashOK := blockHashRaw.(string)

			// Require matching block hash (targetBlockHash is always required now)
			if !hasBlockHash || !blockHashOK {
				logger().NamedLogger.Debug(logger_ctx, "Skipping peer vote without block_hash while targeting",
					ion.String("key", key),
					ion.String("target_block_hash", targetBlockHash),
					ion.String("function", "Structs.ProcessVotesFromCRDT"))
				continue
			}
			if blockHash != targetBlockHash {
				logger().NamedLogger.Debug(logger_ctx, "Skipping peer vote for block_hash",
					ion.String("key", key),
					ion.String("block_hash", blockHash),
					ion.String("target_block_hash", targetBlockHash),
					ion.String("function", "Structs.ProcessVotesFromCRDT"))
				continue
			}

			// Use the key (which is the peer ID) to store the latest vote for that block
			voteData[key] = peerVote{
				vote:      int8(voteValue),
				blockHash: blockHash,
			}
			logger().NamedLogger.Debug(logger_ctx, "Added vote for peer",
				ion.String("key", key),
				ion.Int("vote_value", int(voteValue)),
				ion.String("block_hash", blockHash),
				ion.String("function", "Structs.ProcessVotesFromCRDT"))
		}
	}

	if len(voteData) == 0 {
		logger().NamedLogger.Error(logger_ctx, "No votes found in CRDT to process", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, errors.New("no votes found in CRDT")
	}

	// Get peer weights from seed node
	client, err := seednode.NewClient(settings.Get().Network.SeedNode)
	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to create seed node client", err.(error),
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, errors.New("failed to create seed node client: " + err.(error).Error())
	}

	weights, err := client.ListWeightsofPeers()
	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to get peer weights", err.(error),
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, errors.New("failed to get peer weights: " + err.(error).Error())
	}

	// Filter weights to only include peers that voted
	filteredWeights := make(map[string]float64)
	filteredVoteData := make(map[string]int8)
	for peerID, vote := range voteData {
		if weight, exists := weights[peerID]; exists {
			filteredVoteData[peerID] = vote.vote
			filteredWeights[peerID] = weight
			logger().NamedLogger.Debug(logger_ctx, "Peer has weight and vote",
				ion.String("peer_id", peerID),
				ion.Float64("weight", weight),
				ion.Int("vote", int(vote.vote)),
				ion.String("block_hash", vote.blockHash),
				ion.String("function", "Structs.ProcessVotesFromCRDT"))
		} else {
			logger().NamedLogger.Debug(logger_ctx, "Peer not found in weights, skipping",
				ion.String("peer_id", peerID),
				ion.String("function", "Structs.ProcessVotesFromCRDT"))
		}
	}

	if len(filteredVoteData) == 0 {
		logger().NamedLogger.Error(logger_ctx, "No votes found after filtering by weights", nil,
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, errors.New("no votes found after filtering by weights")
	}

	// Call votemodule.VoteAggregation with filtered maps
	result, err := voteaggregation.VoteAggregation(filteredWeights, filteredVoteData)
	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to aggregate votes", err.(error),
			ion.String("function", "Structs.ProcessVotesFromCRDT"))
		return 0, errors.New("failed to aggregate votes: " + err.(error).Error())
	}

	logger().NamedLogger.Debug(logger_ctx, "Vote aggregation result",
		ion.Bool("result", result),
		ion.String("function", "Structs.ProcessVotesFromCRDT"))

	// Convert boolean result to int8
	if result {
		return 1, nil
	} else {
		return -1, nil
	}
}
