package crdt

import (
	"gossipnode/DB_OPs"
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
	return e.mem.AppendOp(&Op{
		Key:      key,
		Kind:     OpAdd,
		NodeID:   nodeID,
		Element:  element,
		TS:       ts, // Will be used if provided, otherwise auto-generated
		WallTime: time.Now(),
	})
}

// LWWRemove removes an element from a Last-Writer-Wins set
// If ts is empty, a new timestamp will be generated automatically
func (e *Engine) LWWRemove(nodeID, key, element string, ts VectorClock) error {
	return e.mem.AppendOp(&Op{
		Key:      key,
		Kind:     OpRemove,
		NodeID:   nodeID,
		Element:  element,
		TS:       ts, // Will be used if provided, otherwise auto-generated
		WallTime: time.Now(),
	})
}

// CounterInc increments a counter by the specified delta
// If ts is empty, a new timestamp will be generated automatically
func (e *Engine) CounterInc(nodeID, key string, delta uint64, ts VectorClock) error {
	return e.mem.AppendOp(&Op{
		Key:      key,
		Kind:     OpCounterInc,
		NodeID:   nodeID,
		Value:    delta,
		TS:       ts, // Will be used if provided, otherwise auto-generated
		WallTime: time.Now(),
	})
}

func (e *Engine) GetSet(key string) ([]string, bool) {
	return e.mem.GetSetElements(key)
}

func (e *Engine) GetCounter(key string) (uint64, bool) {
	return e.mem.GetCounterValue(key)
}

// Pseudocode; implement your own encoder/DB write batching
func (e *Engine) SnapshotAll(encode func(map[string]CRDT) ([]byte, error)) error {
	e.mem.mu.RLock()
	defer e.mem.mu.RUnlock()
	blob, err := encode(e.mem.objects)
	if err != nil {
		return err
	}
	// Single key snapshot (or per-key if you prefer). Crucially: not per op.
	return DB_OPs.Create(nil, "crdt_snapshot", blob)
}
