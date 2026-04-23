package crdt

import (
	"time"
	// "gossipnode/config"
)

type Engine struct {
	mem *MemStore
	// db *config.PooledConnection
}

func NewEngineMemOnly(maxHeapBytes int64) *Engine {
	return &Engine{
		mem: NewMemStore(maxHeapBytes),
	}
}

// Public APIs (no DB writes)

// LWWAdd adds an element to a Last-Writer-Wins set
// If ts is empty, a new timestamp will be generated automatically
func (e *Engine) LWWAdd(nodeID, key, element string, ts VectorClock) error {
	if err := validateInput(nodeID, key, element); err != nil {
		return err
	}

	return e.mem.AppendOp(&Op{
		Key:      key,
		Kind:     OpAdd,
		NodeID:   nodeID,
		Element:  element,
		TS:       ts, // Will be used if provided, otherwise auto-generated
		WallTime: time.Now().UTC(),
	})
}

// LWWRemove removes an element from a Last-Writer-Wins set
// If ts is empty, a new timestamp will be generated automatically
func (e *Engine) LWWRemove(nodeID, key, element string, ts VectorClock) error {
	if err := validateInput(nodeID, key, element); err != nil {
		return err
	}

	return e.mem.AppendOp(&Op{
		Key:      key,
		Kind:     OpRemove,
		NodeID:   nodeID,
		Element:  element,
		TS:       ts, // Will be used if provided, otherwise auto-generated
		WallTime: time.Now().UTC(),
	})
}

// CounterInc increments a counter by the specified delta
// If ts is empty, a new timestamp will be generated automatically
func (e *Engine) CounterInc(nodeID, key string, delta uint64, ts VectorClock) error {
	if err := validateCounterInput(nodeID, key, delta); err != nil {
		return err
	}

	return e.mem.AppendOp(&Op{
		Key:      key,
		Kind:     OpCounterInc,
		NodeID:   nodeID,
		Value:    delta,
		TS:       ts, // Will be used if provided, otherwise auto-generated
		WallTime: time.Now().UTC(),
	})
}

func (e *Engine) GetSet(key string) ([]string, bool) {
	return e.mem.GetSetElements(key)
}

func (e *Engine) GetCounter(key string) (uint64, bool) {
	return e.mem.GetCounterValue(key)
}

// GetAllCRDTs returns all CRDTs from the memory store for export
// This provides access to the internal memory store's GetAllCRDTs method
func (e *Engine) GetAllCRDTs() map[string]CRDT {
	return e.mem.GetAllCRDTs()
}

// ApplyMergedCRDT applies a merged CRDT to the engine's memory store
// This is used for synchronization between nodes
func (e *Engine) ApplyMergedCRDT(key string, crdt CRDT) {
	e.mem.mu.Lock()
	defer e.mem.mu.Unlock()
	e.mem.objects[key] = crdt
}
