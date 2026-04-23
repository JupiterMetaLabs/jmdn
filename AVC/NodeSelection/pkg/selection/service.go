package selection

import (
	"context"
	"crypto/ed25519"
	"fmt"

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
		fmt.Println("❌ Failed to connect to peer directory:", err)
		return nil, err
	}
	// fmt.Printf("Debugging 1\n")
	defer peerClient.Close()

	fmt.Println("📡 Connected to peer directory at", peerDirAddress)

	// 2. Fetch buddy-eligible peers (excludes recent buddies)
	// fmt.Printf("Debugging 2\n")
	// allNodes := make([]Node, 0)
	allNodes, err := peerClient.ListBuddyPeers(ctx)
	if err != nil {
		fmt.Println("⚠️ WARNING  Failed to fetch buddy peers:", err)
		return nil, err
	}

	// TODO: not needed -- ListBuddyPeers seems to be returning all nodes already
	// if err != nil || len(allNodes) == 0 {
	// 	fmt.Println("⚠️  Failed to fetch buddy peers, falling back to all active peers:", err)
	// 	// Fallback to all active peers
	// 	allNodes, err = peerClient.ListAllPeers(ctx)
	// 	fmt.Printf("allNodes --  fallback: %+v\n", allNodes)
	// 	if err != nil {
	// 		return nil, err
	// 	}
	// }

	// Display first node in detail
	if len(allNodes) > 0 {
		// fmt.Println("🔍 Inspecting first node in detail:", allNodes[0])
		fmt.Printf("%+v\n", allNodes[0])
	}

	fmt.Printf("📋 Fetched %d eligible peers\n", len(allNodes))

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

	// fmt.Printf("Debugging 7\n")
	fmt.Printf("nodes: %+v\n", len(nodes))

	// fmt.Printf("🔍 Filtering %d nodes for eligibility\n", len(nodes))

	// 1. Filter eligible nodes
	filterConfig := DefaultFilterConfig()
	eligible := FilterEligible(nodeID, nodes, filterConfig)

	if len(eligible) == 0 {
		return nil, ErrNoPeersAvailable
	}

	// fmt.Printf("✅ %d eligible nodes after filtering\n", len(eligible))
	// fmt.Printf("Debugging 8\n")
	fmt.Printf("eligible: %+v\n", len(eligible))
	// 2. Create VRF selector
	vrfConfig := &VRFConfig{
		NetworkSalt: networkSalt,
		PrivateKey:  privateKey,
	}

	selector, err := NewVRFSelector(vrfConfig)
	if err != nil {
		return nil, err
	}
	// fmt.Printf("Debugging 9\n")
	vrfSelector := selector.(*VRFSelector)

	// 3. Select buddies using VRF algorithm
	fmt.Printf("🎲 Selecting %d buddies using VRF\n", numBuddies)
	return vrfSelector.SelectMultipleBuddies(ctx, nodeID, eligible, numBuddies)
}
