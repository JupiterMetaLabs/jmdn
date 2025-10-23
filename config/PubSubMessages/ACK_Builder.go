package PubSubMessages


import (
	"encoding/json"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/peer"
)

// < -- ACK Builder Pattern -- >
// Builder constructor
func NewACKBuilder() *ACK {
	return &ACK{}
}

// Chainable builder methods
func (ack *ACK) setTrueStatus() *ACK {
	ack.Status = config.Type_ACK_True
	return ack
}

func (ack *ACK) setFalseStatus() *ACK {
	ack.Status = config.Type_ACK_False
	return ack
}

func (ack *ACK) setPeerID(peerID peer.ID) *ACK {
	ack.PeerID = peerID.String()
	return ack
}

func (ack *ACK) GetPeerID() string {
	return ack.PeerID
}

func (ack *ACK) setStage(stage string) *ACK {
	ack.Stage = stage
	return ack
}

func (ack *ACK) GetStage() string {
	return ack.Stage
}

func (ack *ACK) GetStatus() string {
	return ack.Status
}

func (ack *ACK) Marshal() ([]byte, error) {
	return json.Marshal(ack)
} 

func (ack *ACK) ToString() string {
	data, err := ack.Marshal()
	if err != nil {
		return ""
	}
	return string(data)
}

// Trying to acheive the builder pattern for the ACK message
func (ack *ACK) True_ACK_Message(peerID peer.ID, Stage string) *ACK {
	return NewACKBuilder().setTrueStatus().setPeerID(peerID).setStage(Stage)
}

func (ack *ACK) False_ACK_Message(peerID peer.ID, Stage string) *ACK {
	return NewACKBuilder().setFalseStatus().setPeerID(peerID).setStage(Stage)
}