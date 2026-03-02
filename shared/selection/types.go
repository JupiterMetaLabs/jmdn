package selection

import (
	"context"
	"errors"

	seednodetypes "jmdn/seednode/types"
)

// Node wraps types.Node with selection score
type Node struct {
	seednodetypes.Node
	SelectionScore float64 // 0.0 to 1.0, if >= 0.5 then eligible
}

// Factory creates selection algorithms
type Factory func(config interface{}) (Selector, error)

// Selector interface for selection algorithms
type Selector interface {
	SelectBuddy(ctx context.Context, nodeID string, nodes []Node) (*BuddyNode, error)
}

// BuddyNode represents the selected buddy with proof
type BuddyNode struct {
	Node  *Node
	Proof []byte
}

// Errors
var (
	ErrNoPeersAvailable    = errors.New("no peers available for selection")
	ErrVRFGenerationFailed = errors.New("VRF proof generation failed")
	ErrInvalidNodeID       = errors.New("invalid node ID")
)
