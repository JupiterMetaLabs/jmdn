package Router

import (
	AppContext "gossipnode/config/Context"
	"fmt"
	"gossipnode/AVC/NodeSelection/pkg/selection"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"gossipnode/node"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

type NodeselectionRouter struct{}

const (
	NodeselectionRouterAppContext = "avc.node.selection.router"
)

const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
const networkSalt = "test-salt"

func NewNodeselectionRouter() *NodeselectionRouter {
	return &NodeselectionRouter{}
}

func (r *NodeselectionRouter) GetBuddyNodes(number int) ([]*selection.BuddyNode, error) {
	ctx, cancel := AppContext.GetAppContext(NodeselectionRouterAppContext).NewChildContextWithTimeout(10*time.Second)
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

	// Get the seednode URL from config
	seedNodeURL := config.GetSeedNodeURL()
	if seedNodeURL == "" {
		return nil, fmt.Errorf("no seednode URL configured - use -seednode flag to specify a seed node")
	}

	buddies, err := selection.GetBuddyNodes(ctx, peerID, privateKey, []byte(networkSalt), seedNodeURL, number)

	if err != nil {
		return nil, err
	}

	// Remove current peerID from the buddies list if it exists
	filteredBuddies := make([]*selection.BuddyNode, 0, len(buddies))
	for _, buddy := range buddies {
		if buddy.Node.PeerId != peerID {
			filteredBuddies = append(filteredBuddies, buddy)
		}
	}

	// Debugging
	for _, buddy := range filteredBuddies {
		fmt.Println("buddy", buddy.Node.PeerId)
	}

	return filteredBuddies, nil
}

func (r *NodeselectionRouter) GetBuddyNodesFromList(peers []*selection.BuddyNode) ([]PubSubMessages.Buddy_PeerMultiaddr, error) {
	peerIDs := make([]PubSubMessages.Buddy_PeerMultiaddr, 0)
	for _, node := range peers {
		peerID, err := peer.Decode(node.Node.PeerId)
		if err != nil {
			return nil, fmt.Errorf("failed to decode peer ID for node: %v", node.Node.PeerId)
		}
		// Use all multiaddrs from the Multiaddrs slice
		if len(node.Node.Multiaddrs) == 0 {
			return nil, fmt.Errorf("no multiaddrs available for node: %v", node.Node.PeerId)
		}

		// Create a Buddy_PeerMultiaddr entry for each multiaddr
		for _, addrStr := range node.Node.Multiaddrs {
			multiAddress, err := multiaddr.NewMultiaddr(addrStr)
			if err != nil {
				return nil, fmt.Errorf("failed to create multiaddress '%s' for node %v: %w", addrStr, node.Node.PeerId, err)
			}

			if peerID == "" || multiAddress == nil {
				return nil, fmt.Errorf("failed to get peer ID or multiaddress for node: %v", node.Node.PeerId)
			}

			peerIDs = append(peerIDs, PubSubMessages.Buddy_PeerMultiaddr{
				PeerID:    peerID,
				Multiaddr: multiAddress,
			})
		}
	}
	return peerIDs, nil
}
