package selection

import (
	"context"
	"crypto/ed25519"
	"fmt"

	"github.com/JupiterMetaLabs/ion"
	seednode "gossipnode/seednode"
)

// GetBuddyNodes is the MAIN function that external code will call
// Fetches peers from directory, filters, and selects buddies
func GetBuddyNodes(
	ctx context.Context,
	nodeID string,
	privateKey ed25519.PrivateKey,
	networkSalt []byte,
	peerDirAddress string,
	numBuddies int,
) ([]*BuddyNode, error) {
	// 1. Connect to peer directory
	peerClient, err := seednode.NewClient(peerDirAddress)
	if err != nil {
		logger().Error(context.Background(), "Failed to connect to peer directory", err)
		return nil, err
	}
	// logger().Info(context.Background(), "Debugging 1\n")
	defer peerClient.Close()

	logger().Info(context.Background(), "Connected to peer directory", ion.String("address", peerDirAddress))

	// 2. Fetch buddy-eligible peers (excludes recent buddies)
	// logger().Info(context.Background(), "Debugging 2\n")
	// allNodes := make([]Node, 0)
	allNodes, err := peerClient.ListBuddyPeers(ctx)
	if err != nil {
		logger().Warn(context.Background(), "Failed to fetch buddy peers", ion.Err(err))
		return nil, err
	}

	// TODO: not needed -- ListBuddyPeers seems to be returning all nodes already
	// if err != nil || len(allNodes) == 0 {
	// 	fmt.Println("⚠️  Failed to fetch buddy peers, falling back to all active peers:", err)
	// 	// Fallback to all active peers
	// 	allNodes, err = peerClient.ListAllPeers(ctx)
	// 	logger().Info(context.Background(), "allNodes --  fallback: %+v", allNodes)
	// 	if err != nil {
	// 		return nil, err
	// 	}
	// }

	// Display first node in detail
	if len(allNodes) > 0 {
		// fmt.Println("🔍 Inspecting first node in detail:", allNodes[0])
		logger().Info(context.Background(), "%+v", allNodes[0])
	}

	logger().Info(context.Background(), "📋 Fetched %d eligible peers", len(allNodes))

	// 3. Use the nodes to select buddies
	// The selection score is already calculated in seednode.go
	buddies, err := GetBuddyNodesWithNodes(ctx, nodeID, privateKey, networkSalt, allNodes, numBuddies)
	if err != nil {
		return nil, err
	}

	// 4. Update the peer directory with selected buddies
	selectedIDs := make([]string, len(buddies))
	for i, buddy := range buddies {
		selectedIDs[i] = buddy.Node.ID
	}

	// err = peerClient.UpdateBuddies(ctx, selectedIDs)
	// if err != nil {
	// 	fmt.Println("⚠️  Failed to update buddy list on peer directory:", err)
	// 	// Non-fatal - we still return the selected buddies
	// } else {
	// 	fmt.Println("✅ Updated buddy list on peer directory")
	// }

	return buddies, nil
}

// GetBuddyNodesWithNodes - For when you already have the node list
func GetBuddyNodesWithNodes(
	ctx context.Context,
	nodeID string,
	privateKey ed25519.PrivateKey,
	networkSalt []byte,
	nodes []Node,
	numBuddies int,
) ([]*BuddyNode, error) {
	if len(nodes) == 0 {
		return nil, ErrNoPeersAvailable
	}

	// logger().Info(context.Background(), "Debugging 7\n")
	logger().Info(context.Background(), "nodes: %+v", len(nodes))

	// logger().Info(context.Background(), "🔍 Filtering %d nodes for eligibility", len(nodes))

	// 1. Filter eligible nodes
	filterConfig := DefaultFilterConfig()
	eligible := FilterEligible(nodeID, nodes, filterConfig)

	if len(eligible) == 0 {
		return nil, ErrNoPeersAvailable
	}

	// logger().Info(context.Background(), "✅ %d eligible nodes after filtering", len(eligible))
	// logger().Info(context.Background(), "Debugging 8\n")
	logger().Info(context.Background(), "eligible: %+v", len(eligible))
	// 2. Create VRF selector
	vrfConfig := &VRFConfig{
		NetworkSalt: networkSalt,
		PrivateKey:  privateKey,
	}

	selector, err := NewVRFSelector(vrfConfig)
	if err != nil {
		return nil, err
	}
	// logger().Info(context.Background(), "Debugging 9\n")
	vrfSelector := selector.(*VRFSelector)

	// 3. Select buddies using VRF algorithm
	logger().Info(context.Background(), "🎲 Selecting %d buddies using VRF", numBuddies)
	return vrfSelector.SelectMultipleBuddies(ctx, nodeID, eligible, numBuddies)
}
