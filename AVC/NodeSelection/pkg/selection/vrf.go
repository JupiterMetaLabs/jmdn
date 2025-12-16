package selection

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"
	"sync"

	"github.com/yahoo/coname/vrf"
)

const (
	VRFAlgorithmName    = "vrf"
	VRFAlgorithmVersion = "v1.0.0"
)

// VRFSelector implements VRF-based buddy selection with ASN diversity
type VRFSelector struct {
	networkSalt []byte
	privateKey  ed25519.PrivateKey
	rngPool     sync.Pool
}

// VRFConfig holds VRF-specific configuration
type VRFConfig struct {
	NetworkSalt []byte
	PrivateKey  ed25519.PrivateKey
}

// NewVRFSelector creates a new VRF selector instance
func NewVRFSelector(config interface{}) (Selector, error) {
	vrfConfig, ok := config.(*VRFConfig)
	if !ok {
		return nil, fmt.Errorf("invalid config type, expected *VRFConfig")
	}

	if len(vrfConfig.NetworkSalt) == 0 {
		return nil, fmt.Errorf("network_salt parameter required")
	}

	if len(vrfConfig.PrivateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key size")
	}

	return &VRFSelector{
		networkSalt: vrfConfig.NetworkSalt,
		privateKey:  vrfConfig.PrivateKey,
		rngPool: sync.Pool{
			New: func() interface{} {
				return rand.New(rand.NewSource(0))
			},
		},
	}, nil
}

// SelectBuddy performs VRF-based buddy selection
func (s *VRFSelector) SelectBuddy(
	ctx context.Context,
	nodeID string,
	nodes []Node,
) (*BuddyNode, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("nodeID cannot be empty")
	}

	if len(nodes) == 0 {
		return nil, ErrNoPeersAvailable
	}

	// Filter eligible nodes (score >= 0.5)
	filterConfig := DefaultFilterConfig()
	eligible := FilterEligible(nodeID, nodes, filterConfig)

	if len(eligible) == 0 {
		return nil, ErrNoPeersAvailable
	}

	// Convert private key
	privateKeyArray := (*[ed25519.PrivateKeySize]byte)(s.privateKey)

	// Generate VRF proof
	roundMessage := s.buildRoundMessage(nodeID)
	vrfHash, vrfProof := vrf.Prove(roundMessage, privateKeyArray)

	// Convert VRF hash to seed
	seed := binary.BigEndian.Uint64(vrfHash[:8])

	// Shuffle
	s.fisherYatesShuffle(eligible, seed)

	// Select first node
	selectedNode := &eligible[0]

	return &BuddyNode{
		Node:  selectedNode,
		Proof: vrfProof,
	}, nil
}

// buildRoundMessage creates the message for VRF
func (s *VRFSelector) buildRoundMessage(nodeID string) []byte {
	return []byte(fmt.Sprintf("%s:%s", nodeID, string(s.networkSalt)))
}

// fisherYatesShuffle performs Fisher-Yates shuffle
func (s *VRFSelector) fisherYatesShuffle(nodes []Node, seed uint64) {
	rng := s.rngPool.Get().(*rand.Rand)
	rng.Seed(int64(seed))

	n := len(nodes)
	for i := n - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		nodes[i], nodes[j] = nodes[j], nodes[i]
	}

	s.rngPool.Put(rng)
}

// shuffleStrings shuffles a string slice
func (s *VRFSelector) shuffleStrings(strs []string, seed uint64) {
	rng := s.rngPool.Get().(*rand.Rand)
	rng.Seed(int64(seed))

	n := len(strs)
	for i := n - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		strs[i], strs[j] = strs[j], strs[i]
	}

	s.rngPool.Put(rng)
}

func (s *VRFSelector) SelectMultipleBuddies(
	ctx context.Context,
	nodeID string,
	nodes []Node,
	k int,
) ([]*BuddyNode, error) {
	if k <= 0 {
		return nil, fmt.Errorf("k must be positive, got %d", k)
	}

	if len(nodes) == 0 {
		return nil, ErrNoPeersAvailable
	}

	if nodeID == "" {
		return nil, fmt.Errorf("nodeID cannot be empty")
	}

	// Filter eligible nodes (score >= 0.5)
	filterConfig := DefaultFilterConfig()
	eligible := FilterEligible(nodeID, nodes, filterConfig)

	if len(eligible) == 0 {
		return nil, ErrNoPeersAvailable
	}

	// Cap k to available nodes
	if k > len(eligible) {
		k = len(eligible)
	}

	// Convert private key
	privateKeyArray := (*[ed25519.PrivateKeySize]byte)(s.privateKey)

	// Generate VRF proof
	roundMessage := s.buildRoundMessage(nodeID)
	vrfHash, vrfProof := vrf.Prove(roundMessage, privateKeyArray)

	// Convert VRF hash to seed
	seed := binary.BigEndian.Uint64(vrfHash[:8])

	// Copy nodes for shuffling
	nodesCopy := make([]Node, len(eligible))
	copy(nodesCopy, eligible)

	// Apply Fisher-Yates shuffle first for VRF randomness
	s.fisherYatesShuffle(nodesCopy, seed)

	// Then select with region diversity and selection score priority
	selected := s.selectWithRegionDiversity(nodesCopy, k, seed)

	if len(selected) == 0 {
		return nil, ErrNoPeersAvailable
	}

	// Create buddy nodes from selected
	buddies := make([]*BuddyNode, len(selected))
	for i := 0; i < len(selected); i++ {
		buddies[i] = &BuddyNode{
			Node:  &selected[i],
			Proof: vrfProof,
		}
	}

	return buddies, nil
}

// selectWithRegionDiversity selects k nodes ensuring region diversity with selection score priority
// NOTE: nodes passed here are already pre-filtered by FilterEligible (score >= config.MinSelectionScore)
func (s *VRFSelector) selectWithRegionDiversity(nodes []Node, k int, seed uint64) []Node {
	if k >= len(nodes) {
		return nodes
	}

	// Use the already-filtered nodes directly (no redundant filtering)
	eligibleNodes := nodes

	if len(eligibleNodes) == 0 {
		return []Node{}
	}

	if k >= len(eligibleNodes) {
		return eligibleNodes
	}

	// Group nodes by region
	regionGroups := GroupNodesByRegion(eligibleNodes)

	// Sort nodes within each region by selection score (descending)
	for region := range regionGroups {
		sort.Slice(regionGroups[region], func(i, j int) bool {
			return regionGroups[region][i].SelectionScore > regionGroups[region][j].SelectionScore
		})
	}

	// Get region list
	regions := make([]string, 0, len(regionGroups))
	for region := range regionGroups {
		regions = append(regions, region)
	}

	// Shuffle regions for fairness
	s.shuffleStrings(regions, seed)

	selected := make([]Node, 0, k)
	selectedIDs := make(map[string]bool) // Track selected node IDs
	regionCounts := make(map[string]int)

	// Round-robin selection across regions
	for len(selected) < k {
		selectedThisRound := false

		for _, region := range regions {
			if len(selected) >= k {
				break
			}

			nodesInRegion := regionGroups[region]

			// Skip if we've already selected all nodes from this region
			if regionCounts[region] >= len(nodesInRegion) {
				continue
			}

			// Find next unselected node from this region (already sorted by score)
			for idx := regionCounts[region]; idx < len(nodesInRegion); idx++ {
				node := nodesInRegion[idx]

				// Skip if already selected
				if selectedIDs[node.PeerId] {
					regionCounts[region]++
					continue
				}

				// Select this node
				selected = append(selected, node)
				selectedIDs[node.PeerId] = true
				regionCounts[region]++
				selectedThisRound = true
				break
			}

			if len(selected) >= k {
				break
			}
		}

		// Safety: if no nodes selected in this round, break
		if !selectedThisRound {
			break
		}
	}

	return selected
}
