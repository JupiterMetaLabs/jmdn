package Vote

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	MessagePassing "gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
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
	fmt.Printf("\n📊 [VoteTrigger.SubmitVote] Starting vote submission...\n")

	// Get the Listener Node
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		fmt.Printf("❌ [VoteTrigger.SubmitVote] Listener node not found\n")
		return fmt.Errorf("listener node not found")
	}
	fmt.Printf("✅ [VoteTrigger.SubmitVote] Listener node found: %s\n", listenerNode.PeerID.String())

	// If consensus message is not set, try to get it from global cache
	if vt.ConsensusMessage == nil {
		// This should not happen in normal flow, but handle gracefully
		fmt.Printf("❌ [VoteTrigger.SubmitVote] Consensus message not set for voting\n")
		return fmt.Errorf("consensus message not set for voting")
	}

	fmt.Printf("🔍 [VoteTrigger.SubmitVote] Checking ZKBlock validation...\n")
	// Check the Three checks from the Security Module
	status, err := Security.CheckZKBlockValidation(vt.ConsensusMessage.GetZKBlock())
	if !status || err != nil {
		vote := PubSubMessages.Vote{
			Vote:      -1,
			BlockHash: vt.ConsensusMessage.GetZKBlock().BlockHash.String(),
		}
		vt.setVote(&vote)
		fmt.Printf("❌ [VoteTrigger.SubmitVote] ZKBlock validation failed: %v\n", err)
		fmt.Printf("❌ [VoteTrigger.SubmitVote] Voting NO (-1)\n")
	} else if status {
		vote := PubSubMessages.Vote{
			Vote:      1,
			BlockHash: vt.ConsensusMessage.GetZKBlock().BlockHash.String(),
		}
		vt.setVote(&vote)
		fmt.Printf("✅ [VoteTrigger.SubmitVote] ZKBlock validation passed\n")
		fmt.Printf("✅ [VoteTrigger.SubmitVote] Voting YES (1)\n")
	} else {
		fmt.Printf("❌ [VoteTrigger.SubmitVote] Failed to vote, as vote is neither 1 or -1\n")
		return fmt.Errorf("failed to vote, as vote is neither 1 or -1")
	}

	// Pick up the listener node using the consistent hashing to send message to
	NodeToSendTo := vt.PickListner(listenerNode.PeerID)
	fmt.Printf("📡 [VoteTrigger.SubmitVote] Sending vote to peer: %s\n", NodeToSendTo.PeerID.String())

	// Create proper message with ACK stage for vote submission
	voteMessage := PubSubMessages.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage(vt.ToVoteString(vt.Vote)).
		SetTimestamp(time.Now().Unix()).
		SetACK(PubSubMessages.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_SubmitVote))

	// Marshal the message to JSON
	messageBytes, err := json.Marshal(voteMessage)
	if err != nil {
		fmt.Printf("❌ [VoteTrigger.SubmitVote] Failed to marshal vote message: %v\n", err)
		return fmt.Errorf("failed to marshal vote message: %v", err)
	}

	fmt.Printf("📤 [VoteTrigger.SubmitVote] Sending message to listener node...\n")
	// Send the message to the listener node
	if err := MessagePassing.NewListenerStruct(listenerNode).SendMessageToPeer(NodeToSendTo.PeerID, string(messageBytes)); err != nil {
		fmt.Printf("❌ [VoteTrigger.SubmitVote] Failed to send message to listener node: %v\n", err)
		return fmt.Errorf("failed to send message to listener node: %v", err)
	}

	fmt.Printf("✅ [VoteTrigger.SubmitVote] Vote submitted successfully!\n\n")
	return nil
}

func (vt *VoteTrigger) PickListner(PeerID peer.ID) PubSubMessages.Buddy_PeerMultiaddr {
	// Node should hash its own peerID  pick one from all the keys in buddies map
	buddies := vt.ConsensusMessage.GetBuddies()
	numKeys := len(buddies)
	// Get the all the keys in the buddies map
	selectedKey := consistentHashing(PeerID, numKeys)

	// if the selected Key not in the buddies map, return the first peer
	if _, ok := buddies[selectedKey]; !ok {
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
