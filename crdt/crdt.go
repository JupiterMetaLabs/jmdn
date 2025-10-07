package crdt

import (
	"fmt"
)

// CRDT is an interface for conflict-free replicated data types
type CRDT interface {
	// GetKey returns the unique identifier for this CRDT
	GetKey() string

	// GetTimestamp returns the vector timestamp of this CRDT
	GetTimestamp() VectorClock

	// Merge combines this CRDT with another one and returns the result
	Merge(other CRDT) (CRDT, error)
}

// =============================
// === Vector Clock Section ===
// =============================

// VectorClock represents a vector clock for tracking causality
type VectorClock map[string]uint64

// Compare compares two vector clocks and returns:
//
//	-1 if vc < other (happens-before)
//	 0 if vc || other (concurrent or equal)
//	 1 if vc > other (happens-after)
func (vc VectorClock) Compare(other VectorClock) int {
	less := false
	greater := false
	equal := true

	// Check all nodes in both clocks
	allNodes := make(map[string]bool)
	for node := range vc {
		allNodes[node] = true
	}
	for node := range other {
		allNodes[node] = true
	}

	for node := range allNodes {
		ourTS, ourExists := vc[node]
		otherTS, otherExists := other[node]

		if !ourExists {
			// We don't have this node, other does
			less = true
			equal = false
		} else if !otherExists {
			// Other doesn't have this node, we do
			greater = true
			equal = false
		} else {
			// Both have this node
			if ourTS > otherTS {
				greater = true
				equal = false
			} else if ourTS < otherTS {
				less = true
				equal = false
			}
		}
	}

	if equal {
		return 0 // equal
	} else if less && greater {
		return 0 // concurrent
	} else if greater {
		return 1 // happens-after
	} else {
		return -1 // happens-before
	}
}

// Merge combines two vector clocks, taking the maximum value for each node
func (vc VectorClock) Merge(other VectorClock) VectorClock {
	result := make(VectorClock)
	for node, ts := range vc {
		result[node] = ts
	}
	for node, otherTS := range other {
		if ourTS, exists := result[node]; !exists || otherTS > ourTS {
			result[node] = otherTS
		}
	}
	return result
}

// Increment increases the counter for the specified node
func (vc VectorClock) Increment(nodeID string) {
	if current, exists := vc[nodeID]; exists {
		vc[nodeID] = current + 1
	} else {
		vc[nodeID] = 1
	}
}

// =============================
// === LWW Set (Last Writer Wins)
// =============================

type LWWSet struct {
	Key       string                 `json:"key"`
	Adds      map[string]VectorClock `json:"adds"`
	Removes   map[string]VectorClock `json:"removes"`
	Timestamp VectorClock            `json:"timestamp"`
}

// NewLWWSet creates a new LWW set
func NewLWWSet(key string) *LWWSet {
	return &LWWSet{
		Key:       key,
		Adds:      make(map[string]VectorClock),
		Removes:   make(map[string]VectorClock),
		Timestamp: make(VectorClock),
	}
}

func (s *LWWSet) GetKey() string                { return s.Key }
func (s *LWWSet) GetTimestamp() VectorClock     { return s.Timestamp }
func (s *LWWSet) Add(nodeID, element string)    { s.applyOp(nodeID, element, true) }
func (s *LWWSet) Remove(nodeID, element string) { s.applyOp(nodeID, element, false) }

func (s *LWWSet) applyOp(nodeID, element string, isAdd bool) {
	ts := make(VectorClock)
	for k, v := range s.Timestamp {
		ts[k] = v
	}
	ts.Increment(nodeID)
	if isAdd {
		s.Adds[element] = ts
	} else {
		s.Removes[element] = ts
	}
	s.Timestamp = ts
}

// Contains checks if an element is in the set
func (s *LWWSet) Contains(element string) bool {
	addTS, added := s.Adds[element]
	rmTS, removed := s.Removes[element]
	if !added {
		return false
	}
	if !removed {
		return true
	}
	// Removal wins on tie
	return addTS.Compare(rmTS) > 0
}

// GetElements returns current visible elements
func (s *LWWSet) GetElements() []string {
	var elems []string
	for el := range s.Adds {
		if s.Contains(el) {
			elems = append(elems, el)
		}
	}
	return elems
}

// Merge merges another LWWSet into this one
func (s *LWWSet) Merge(other CRDT) (CRDT, error) {
	o, ok := other.(*LWWSet)
	if !ok {
		return nil, fmt.Errorf("cannot merge different CRDT types")
	}
	if s.Key != o.Key {
		return nil, fmt.Errorf("cannot merge sets with different keys")
	}

	res := NewLWWSet(s.Key)

	// merge adds
	for e, ts := range s.Adds {
		res.Adds[e] = ts
	}
	for e, ts := range o.Adds {
		if our, exists := res.Adds[e]; !exists {
			res.Adds[e] = ts
		} else {
			// Keep the later timestamp, or either if concurrent
			compare := ts.Compare(our)
			if compare > 0 {
				// ts is later
				res.Adds[e] = ts
			} else if compare == 0 {
				// Concurrent - keep the first one for determinism
				// (in practice, you might want to use a tie-breaker like node ID)
				res.Adds[e] = our
			}
		}
	}

	// merge removes
	for e, ts := range s.Removes {
		res.Removes[e] = ts
	}
	for e, ts := range o.Removes {
		if our, exists := res.Removes[e]; !exists {
			res.Removes[e] = ts
		} else {
			// Keep the later timestamp, or either if concurrent
			compare := ts.Compare(our)
			if compare > 0 {
				// ts is later
				res.Removes[e] = ts
			} else if compare == 0 {
				// Concurrent - keep the first one for determinism
				// (in practice, you might want to use a tie-breaker like node ID)
				res.Removes[e] = our
			}
		}
	}

	res.Timestamp = s.Timestamp.Merge(o.Timestamp)
	return res, nil
}

// =============================
// === Grow-only Counter (G-Counter)
// =============================

type Counter struct {
	Key       string            `json:"key"`
	Counters  map[string]uint64 `json:"counters"`
	Timestamp VectorClock       `json:"timestamp"`
}

func NewCounter(key string) *Counter {
	return &Counter{
		Key:       key,
		Counters:  make(map[string]uint64),
		Timestamp: make(VectorClock),
	}
}

func (c *Counter) GetKey() string            { return c.Key }
func (c *Counter) GetTimestamp() VectorClock { return c.Timestamp }

// Increment increases the counter for a given node
// This method is used internally by MemStore; external users should use Engine.CounterInc
func (c *Counter) Increment(nodeID string, value uint64) {
	c.Counters[nodeID] += value
	c.Timestamp.Increment(nodeID)
}

// IncrementWithTimestamp increases the counter with a specific timestamp
// This is used when applying operations with provided timestamps
func (c *Counter) IncrementWithTimestamp(nodeID string, value uint64, ts VectorClock) {
	c.Counters[nodeID] += value
	if len(ts) > 0 {
		// Use provided timestamp and merge with current
		c.Timestamp = c.Timestamp.Merge(ts)
	} else {
		// Fallback to incrementing current timestamp
		c.Timestamp.Increment(nodeID)
	}
}

func (c *Counter) Value() uint64 {
	var total uint64
	for _, v := range c.Counters {
		total += v
	}
	return total
}

// Merge merges another counter
func (c *Counter) Merge(other CRDT) (CRDT, error) {
	o, ok := other.(*Counter)
	if !ok {
		return nil, fmt.Errorf("cannot merge different CRDT types")
	}
	if c.Key != o.Key {
		return nil, fmt.Errorf("cannot merge counters with different keys")
	}

	res := NewCounter(c.Key)
	for n, v := range c.Counters {
		res.Counters[n] = v
	}
	for n, v := range o.Counters {
		if our, exists := res.Counters[n]; !exists {
			res.Counters[n] = v
		} else {
			// For counters, we take the maximum value per node
			// This is correct for G-Counters (grow-only counters)
			if v > our {
				res.Counters[n] = v
			}
		}
	}
	res.Timestamp = c.Timestamp.Merge(o.Timestamp)
	return res, nil
}
