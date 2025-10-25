package Router

import (
	"context"
	"fmt"
	"gossipnode/AVC/NodeSelection/pkg/selection"
	"gossipnode/node"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type NodeselectionRouter struct{}

const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
const networkSalt = "test-salt"
const peerDirAddress = "34.174.233.203:17002"

func NewNodeselectionRouter() *NodeselectionRouter {
	return &NodeselectionRouter{}
}

func (r *NodeselectionRouter) GetBuddyNodes(number int) ([]*selection.BuddyNode, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, privateKey, err := selection.GenerateKeysFromMnemonic(mnemonic)
	if err != nil {
		return nil, err
	}
	var peerID string
	peerID = node.GetPeerID()
	if peerID == "" {
		fmt.Println("No peer ID found, falling back to reading from peer.json")
		// Fallback to reading from peer.json
		peerID = node.GetPeerIDFromJSON()
		if peerID == "" {
			return nil, fmt.Errorf("failed to get peer ID")
		}
	}

	fmt.Println("peerID:", peerID)
	buddies, err := selection.GetBuddyNodes(ctx, peerID, privateKey, []byte(networkSalt), peerDirAddress, number)
	if err != nil {
		return nil, err
	}

	return buddies, nil
}

func (r *NodeselectionRouter) GetBuddyNodesFromList(peers []*selection.BuddyNode) ([]peer.ID, error) {
	peerIDs := make([]peer.ID, 0)
	for _, node := range peers {
		peerID := peer.ID(node.Node.ID)
		if peerID == "" {
			return nil, fmt.Errorf("failed to get peer ID for node: %v", node.Node.ID)
		}
		peerIDs = append(peerIDs, peerID)
	}
	return peerIDs, nil
}