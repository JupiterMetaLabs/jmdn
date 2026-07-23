package Router

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"gossipnode/AVC/NodeSelection/pkg/selection"
	"gossipnode/config/PubSubMessages"
	"gossipnode/config/settings"
	"gossipnode/node"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

type NodeselectionRouter struct{}

// SECURITY (JMDN-001): the VRF key material for node/committee selection MUST be
// secret and per-network. It was previously hardcoded to the public BIP39 test
// mnemonic ("abandon abandon ... about") with salt "test-salt" — a value known
// to the entire world, which makes every VRF output predictable AND forgeable
// (any attacker can derive the same key, reproduce the proofs, and bias which
// nodes are selected). We now REQUIRE operator-provided secret material via
// environment and refuse to run selection with an insecure default.
//
//	JMDN_NODE_SELECTION_MNEMONIC — the network's secret BIP39 mnemonic
//	JMDN_NETWORK_SALT            — the network's VRF domain-separation salt
//
// TODO(JMDN-001): derive the selection key from this node's own libp2p private
// key (peer.json) instead of a shared mnemonic, so no secret is shared across
// nodes at all.
func selectionKeyMaterial() (mnemonic string, salt string, err error) {
	mnemonic = strings.TrimSpace(os.Getenv("JMDN_NODE_SELECTION_MNEMONIC"))
	salt = strings.TrimSpace(os.Getenv("JMDN_NETWORK_SALT"))
	if mnemonic == "" {
		return "", "", fmt.Errorf("JMDN_NODE_SELECTION_MNEMONIC is not set: refusing to use the public BIP39 test mnemonic for VRF selection (predictable and forgeable)")
	}
	if salt == "" {
		return "", "", fmt.Errorf("JMDN_NETWORK_SALT is not set: refusing to use a default VRF salt")
	}
	return mnemonic, salt, nil
}

func NewNodeselectionRouter() *NodeselectionRouter {
	return &NodeselectionRouter{}
}

func (r *NodeselectionRouter) GetBuddyNodes(number int) ([]*selection.BuddyNode, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mnemonic, networkSalt, err := selectionKeyMaterial()
	if err != nil {
		return nil, err
	}
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
	seedNodeURL := settings.Get().Network.SeedNode
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
