package Vote

import (
	"encoding/json"
	"fmt"
	"gossipnode/config/PubSubMessages"
	"gossipnode/Pubsub/Publish"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/peer"
)

type VoteTrigger struct{
	ConsensusMessage *PubSubMessages.ConsensusMessage
	Vote *PubSubMessages.Vote
}

func NewVoteTrigger() VoteTrigger {
	return VoteTrigger{
		ConsensusMessage: nil,
		Vote: nil,
	}
}

func (vt *VoteTrigger) SetConsensusMessage(consensusMessage *PubSubMessages.ConsensusMessage) {
	vt.ConsensusMessage = consensusMessage
}

func (vt *VoteTrigger) SetVote(Vote *PubSubMessages.Vote) error {

	if Vote.GetVote() != 1 && Vote.GetVote() != -1 {
		return fmt.Errorf("invalid vote")
	}

	vt.Vote = Vote
	return nil
}

func (vt *VoteTrigger) GetVote() *PubSubMessages.Vote {
	return vt.Vote
}

func (vt *VoteTrigger) SubmitVote() error{
	// Get the Listener Node
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		return fmt.Errorf("listener node not found")
	}

	// Create a new message
	message := &PubSubMessages.Message{
		Sender: listenerNode.PeerID,
		Message: convertVoteToString(vt.Vote),
	}

	// Submit the message to the listener node
	if err := Publish.Publish(listenerNode.PubSub, config.PubSub_ConsensusChannel, message, map[string]string{}); err != nil {
		return fmt.Errorf("failed to publish message to pubsub: %v", err)
	}
	return nil
}

func Consistanthashing(PeerID peer.ID){
	// Node should hash its own peerID 
}

func convertVoteToString(vote *PubSubMessages.Vote) string {
	jsonData, err := json.Marshal(vote)
	if err != nil {
		return ""
	}
	return string(jsonData)
}