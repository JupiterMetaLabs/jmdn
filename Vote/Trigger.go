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

	// Check the Three securituy checks from the Security Module
	status, err := Security.CheckZKBlockValidation(vt.ConsensusMessage.GetZKBlock())
	if !status || err != nil {
		vote := PubSubMessages.Vote{
			Vote:      -1,
			BlockHash: vt.ConsensusMessage.GetZKBlock().BlockHash.String(),
		}
		vt.setVote(&vote)
		fmt.Printf("failed to check ZKBlock validation: %v\n", err)
	} else if status {
		vote := PubSubMessages.Vote{
			Vote:      1,
			BlockHash: vt.ConsensusMessage.GetZKBlock().BlockHash.String(),
		}
		vt.setVote(&vote)
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

	// Try to send to multiple nodes if first attempt fails
	maxAttempts := 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Pick up the listener node using the consistent hashing with offset
		NodeToSendTo := vt.PickListnerWithOffset(listenerNode.PeerID, attempt)

		// Check if trying to send to self - skip and try next
		if NodeToSendTo.PeerID == listenerNode.PeerID && attempt < maxAttempts-1 {
			continue
		}

		// Send the message to the listener node
		err := MessagePassing.NewListenerStruct(listenerNode).
			SendMessageToPeer(NodeToSendTo.PeerID, string(messageBytes))

		if err != nil {
			// If this is not the last attempt, try again
			if attempt < maxAttempts-1 {
				continue
			}
			// Last attempt failed
			return fmt.Errorf("failed to send message to listener node after %d attempts: %v", maxAttempts, err)
		}

		// Success!
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
