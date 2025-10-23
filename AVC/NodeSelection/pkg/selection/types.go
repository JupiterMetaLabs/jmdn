package selection

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"gossipnode/AVC/NodeSelection/pkg/types"
)

// Node wraps types.Node with selection score
type Node struct {
	types.Node
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

// Registry manages selection algorithms
type Registry struct {
	mu         sync.RWMutex
	algorithms map[string]Factory
}

var DefaultRegistry = NewRegistry()

func NewRegistry() *Registry {
	return &Registry{
		algorithms: make(map[string]Factory),
	}
}

func (r *Registry) Register(name string, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.algorithms[name] = factory
}

func (r *Registry) Get(name string) (Factory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	factory, exists := r.algorithms[name]
	if !exists {
		return nil, fmt.Errorf("algorithm '%s' not found", name)
	}
	return factory, nil
}

func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.algorithms))
	for name := range r.algorithms {
		names = append(names, name)
	}
	return names
}

// Auto-register VRF algorithm
func init() {
	DefaultRegistry.Register(VRFAlgorithmName, NewVRFSelector)
}

// Errors
var (
	ErrNoPeersAvailable    = errors.New("no peers available for selection")
	ErrVRFGenerationFailed = errors.New("VRF proof generation failed")
	ErrInvalidNodeID       = errors.New("invalid node ID")
)
