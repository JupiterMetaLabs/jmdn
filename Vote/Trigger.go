package Vote

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"

	MessagePassing "gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/Security"

	"time"

	"gossipnode/config"
	"gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.opentelemetry.io/otel/attribute"
)

type VoteTrigger struct {
	ConsensusMessage *PubSubMessages.ConsensusMessage
	Vote             *PubSubMessages.Vote
}

func NewVoteTrigger() VoteTrigger {
	return VoteTrigger{
		ConsensusMessage: nil,
		Vote:             nil,
	}
}

func (vt *VoteTrigger) SetConsensusMessage(consensusMessage *PubSubMessages.ConsensusMessage) {
	vt.ConsensusMessage = consensusMessage
}

func (vt *VoteTrigger) setVote(Vote *PubSubMessages.Vote) error {

	if Vote.GetVote() != 1 && Vote.GetVote() != -1 {
		return fmt.Errorf("invalid vote")
	}

	if Vote.BlockHash == "" {
		return fmt.Errorf("block hash required for vote")
	}

	vt.Vote = Vote
	return nil
}

func (vt *VoteTrigger) GetVote() *PubSubMessages.Vote {
	return vt.Vote
}

func (vt *VoteTrigger) ToVoteString(vote *PubSubMessages.Vote) string {
	jsonData, err := json.Marshal(vote)
	if err != nil {
		return ""
	}
	return string(jsonData)
}

func (vt *VoteTrigger) SubmitVote() error {
	// Get the Listener Node
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		return fmt.Errorf("listener node not found")
	}

	// If consensus message is not set, try to get it from global cache
	if vt.ConsensusMessage == nil {
		// This should not happen in normal flow, but handle gracefully
		return fmt.Errorf("consensus message not set for voting")
	}

	// Create trace context for vote submission
	logger_ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tracer := logger().NamedLogger.Tracer("Vote")
	spanCtx, span := tracer.Start(logger_ctx, "Vote.SubmitVote")
	defer span.End()

	zkBlock := vt.ConsensusMessage.GetZKBlock()
	blockHash := zkBlock.BlockHash.String()

	// Add span attributes for tracing
	span.SetAttributes(
		attribute.String("peer_id", listenerNode.PeerID.String()),
		attribute.String("block_hash", blockHash),
		attribute.Int("block_number", int(zkBlock.BlockNumber)),
		attribute.Int("transaction_count", len(zkBlock.Transactions)),
	)

	// Check the Three security checks from the Security Module
	status, err := Security.CheckZKBlockValidation(zkBlock)

	if !status || err != nil {
		// VOTE REJECTED (-1)
		vote := PubSubMessages.Vote{
			Vote:      -1,
			BlockHash: blockHash,
		}
		vt.setVote(&vote)

		span.SetAttributes(
			attribute.Int("vote", -1),
			attribute.String("vote_decision", "REJECT"),
		)

		if err != nil {
			span.RecordError(err)

			// 🔴 DETAILED REJECTION LOGGING WITH STRUCTURED LOGGER
			logger().NamedLogger.Error(spanCtx, "VOTE REJECTED: Block validation failed",
				err,
				ion.String("peer_id", listenerNode.PeerID.String()),
				ion.String("block_hash", blockHash),
				ion.Int("block_number", int(zkBlock.BlockNumber)),
				ion.Int("transaction_count", len(zkBlock.Transactions)),
				ion.Int("vote", -1),
				ion.String("vote_decision", "REJECT"),
				ion.String("rejection_reason", err.Error()),
				ion.String("function", "Vote.SubmitVote"))

			// Also print to console for immediate visibility
			fmt.Printf("❌ VOTE REJECTED (-1)\n")
			fmt.Printf("   Peer: %s\n", listenerNode.PeerID.String())
			fmt.Printf("   Block: %s (Number: %d)\n", blockHash, zkBlock.BlockNumber)
			fmt.Printf("   Transactions: %d\n", len(zkBlock.Transactions))
			fmt.Printf("   Rejection Reason: %v\n", err)
		} else {
			// Status is false but no error
			logger().NamedLogger.Warn(spanCtx, "VOTE REJECTED: Validation returned false without error",
				ion.String("peer_id", listenerNode.PeerID.String()),
				ion.String("block_hash", blockHash),
				ion.Int("block_number", int(zkBlock.BlockNumber)),
				ion.Int("vote", -1),
				ion.String("vote_decision", "REJECT"),
				ion.String("function", "Vote.SubmitVote"))

			fmt.Printf("❌ VOTE REJECTED (-1)\n")
			fmt.Printf("   Peer: %s\n", listenerNode.PeerID.String())
			fmt.Printf("   Block: %s (Number: %d)\n", blockHash, zkBlock.BlockNumber)
			fmt.Printf("   Rejection Reason: Validation returned false (no error details)\n")
		}
	} else if status {
		// VOTE ACCEPTED (1)
		vote := PubSubMessages.Vote{
			Vote:      1,
			BlockHash: blockHash,
		}
		vt.setVote(&vote)

		span.SetAttributes(
			attribute.Int("vote", 1),
			attribute.String("vote_decision", "ACCEPT"),
		)

		// ✅ ACCEPTANCE LOGGING
		logger().NamedLogger.Info(spanCtx, "VOTE ACCEPTED: Block validation successful",
			ion.String("peer_id", listenerNode.PeerID.String()),
			ion.String("block_hash", blockHash),
			ion.Int("block_number", int(zkBlock.BlockNumber)),
			ion.Int("transaction_count", len(zkBlock.Transactions)),
			ion.Int("vote", 1),
			ion.String("vote_decision", "ACCEPT"),
			ion.String("function", "Vote.SubmitVote"))

		fmt.Printf("✅ VOTE ACCEPTED (1) | Peer: %s | Block: %s (Number: %d)\n",
			listenerNode.PeerID.String(),
			blockHash,
			zkBlock.BlockNumber)
	} else {
		return fmt.Errorf("failed to vote, as vote is neither 1 or -1")
	}

	// Create proper message with ACK stage for vote submission
	voteMessage := PubSubMessages.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage(vt.ToVoteString(vt.Vote)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(PubSubMessages.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_SubmitVote))

	// Marshal the message to JSON
	messageBytes, err := json.Marshal(voteMessage)
	if err != nil {
		return fmt.Errorf("failed to marshal vote message: %v", err)
	}

	// Reuse existing logger_ctx from above (already created with tracer)

	// Determine the target node: prefer the sequencer (consensus creator) if known
	var targetPeerID peer.ID
	sequencerIDStr := vt.ConsensusMessage.GetSequencerID()
	if sequencerIDStr != "" {
		decoded, decErr := peer.Decode(sequencerIDStr)
		if decErr != nil {
			fmt.Printf("⚠️ Failed to decode SequencerID %q, falling back to consistent hashing: %v\n", sequencerIDStr, decErr)
		} else {
			targetPeerID = decoded
		}
	}

	// Try to send to the sequencer (or fallback to consistent hashing)
	maxAttempts := 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var sendTo peer.ID
		if targetPeerID != "" {
			// Send directly to the sequencer
			sendTo = targetPeerID
		} else {
			// Fallback: use consistent hashing (backward compatibility with old sequencer nodes)
			NodeToSendTo := vt.PickListnerWithOffset(listenerNode.PeerID, attempt)
			sendTo = NodeToSendTo.PeerID
		}

		// Check if trying to send to self - skip and try next
		if sendTo == listenerNode.PeerID && attempt < maxAttempts-1 {
			continue
		}

		// Send the message to the target node
		err := MessagePassing.NewListenerStruct(listenerNode).
			SendMessageToPeer(logger_ctx, sendTo, string(messageBytes))

		if err != nil {
			// If this is not the last attempt, try again
			if attempt < maxAttempts-1 {
				fmt.Printf("⚠️ Failed to send vote to %s (attempt %d/%d): %v\n", sendTo, attempt+1, maxAttempts, err)
				continue
			}
			// Last attempt failed
			return fmt.Errorf("failed to send vote to sequencer %s after %d attempts: %v", sendTo, maxAttempts, err)
		}

		// Success!
		fmt.Printf("✅ Vote sent to sequencer %s\n", sendTo)
		return nil
	}

	return fmt.Errorf("failed to submit vote after %d attempts", maxAttempts)
}

func (vt *VoteTrigger) PickListner(PeerID peer.ID) PubSubMessages.Buddy_PeerMultiaddr {
	return vt.PickListnerWithOffset(PeerID, 0)
}

// PickListnerWithOffset picks a listener node using consistent hashing with an offset
func (vt *VoteTrigger) PickListnerWithOffset(PeerID peer.ID, offset int) PubSubMessages.Buddy_PeerMultiaddr {
	// Node should hash its own peerID  pick one from all the keys in buddies map
	buddies := vt.ConsensusMessage.GetBuddies()
	numKeys := len(buddies)

	if numKeys == 0 {
		// Return empty buddy if no buddies exist
		return PubSubMessages.Buddy_PeerMultiaddr{}
	}

	// Get the initial selected key using consistent hashing
	baseKey := consistentHashing(PeerID, numKeys)

	// Add offset to try different nodes
	selectedKey := (baseKey + offset) % numKeys

	// if the selected Key not in the buddies map, return the first peer
	if _, ok := buddies[selectedKey]; !ok {
		if offset < numKeys {
			// Try the next key
			return vt.PickListnerWithOffset(PeerID, (offset+1)%numKeys)
		}
		return buddies[0]
	}
	return buddies[selectedKey]
}

func consistentHashing(PeerID peer.ID, num int) int {
	// Node should hash its own peerID  pick one from all the keys in buddies map
	hasher := sha256.New()
	hasher.Write([]byte(PeerID.String()))
	hashBytes := hasher.Sum(nil)
	hashInt := binary.BigEndian.Uint64(hashBytes[:8])
	return int(hashInt % uint64(num)) // 0 index would be the first peer
}
