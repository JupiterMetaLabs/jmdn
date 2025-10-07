package crdt

import (
	"errors"
	"fmt"
	"gossipnode/DB_OPs"
	"strings"
)

// validateInput validates common input parameters for set operations
func validateInput(nodeID, key, element string) error {
	if strings.TrimSpace(nodeID) == "" {
		return errors.New("nodeID cannot be empty")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("key cannot be empty")
	}
	if strings.TrimSpace(element) == "" {
		return errors.New("element cannot be empty")
	}

	// Check for reasonable length limits
	if len(nodeID) > 256 {
		return errors.New("nodeID too long (max 256 characters)")
	}
	if len(key) > 256 {
		return errors.New("key too long (max 256 characters)")
	}
	if len(element) > 1024 {
		return errors.New("element too long (max 1024 characters)")
	}

	return nil
}

// validateCounterInput validates input parameters for counter operations
func validateCounterInput(nodeID, key string, delta uint64) error {
	if strings.TrimSpace(nodeID) == "" {
		return errors.New("nodeID cannot be empty")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("key cannot be empty")
	}
	if delta == 0 {
		return errors.New("delta cannot be zero")
	}
	if delta > 1000000 { // Reasonable upper limit
		return errors.New("delta too large (max 1,000,000)")
	}

	// Check for reasonable length limits
	if len(nodeID) > 256 {
		return errors.New("nodeID too long (max 256 characters)")
	}
	if len(key) > 256 {
		return errors.New("key too long (max 256 characters)")
	}

	return nil
}


// SnapshotAll creates a snapshot of all CRDT objects
// encode: function to encode CRDT objects to bytes
// Returns error if encoding or database operation fails
func (e *Engine) SnapshotAll(encode func(map[string]CRDT) ([]byte, error)) error {
	if encode == nil {
		return errors.New("encode function cannot be nil")
	}

	e.mem.mu.RLock()
	defer e.mem.mu.RUnlock()

	blob, err := encode(e.mem.objects)
	if err != nil {
		return fmt.Errorf("failed to encode CRDT objects: %w", err)
	}

	err = DB_OPs.Create(nil, "crdt:snapshot:", blob)
	if err != nil {
		return fmt.Errorf("failed to create snapshot: %w", err)
	}
	return nil
}

