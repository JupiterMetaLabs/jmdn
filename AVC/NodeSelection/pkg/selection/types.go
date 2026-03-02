package selection

import (
	"fmt"
	"sync"

	sharedselection "jmdn/shared/selection"
)

// Re-export types from shared package for convenience
type Node = sharedselection.Node
type BuddyNode = sharedselection.BuddyNode
type Factory = sharedselection.Factory
type Selector = sharedselection.Selector

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

// Re-export errors from shared package for convenience
var (
	ErrNoPeersAvailable    = sharedselection.ErrNoPeersAvailable
	ErrVRFGenerationFailed = sharedselection.ErrVRFGenerationFailed
	ErrInvalidNodeID       = sharedselection.ErrInvalidNodeID
)
