package crdt

import (
	"errors"
	"sync"
	"time"
)

// MemStore holds live CRDT objects + an op heap.
// No automatic persistence. You can add (optional) snapshot hooks later.
type MemStore struct {
	mu       sync.RWMutex
	objects  map[string]CRDT
	ops      *OpHeap
	maxBytes int64 // cap of heap, e.g., 50<<20
	metrics  StoreMetrics
}

type StoreMetrics struct {
	TotalOpsAppended uint64
	TotalOpsEvicted  uint64
	LastEvictAt      time.Time
}

func NewMemStore(maxBytes int64) *MemStore {
	return &MemStore{
		objects:  make(map[string]CRDT),
		ops:      NewOpHeap(maxBytes),
		maxBytes: maxBytes,
	}
}

var ErrWrongType = errors.New("wrong CRDT type for key")

func (s *MemStore) getOrCreateSet(key string) *LWWSet {
	sv, ok := s.objects[key]
	if !ok {
		set := NewLWWSet(key)
		s.objects[key] = set
		return set
	}
	if set, ok := sv.(*LWWSet); ok {
		return set
	}
	return nil
}

func (s *MemStore) getOrCreateCounter(key string) *Counter {
	cv, ok := s.objects[key]
	if !ok {
		c := NewCounter(key)
		s.objects[key] = c
		return c
	}
	if c, ok := cv.(*Counter); ok {
		return c
	}
	return nil
}

// AppendOp applies the op to the live CRDT and appends it to the bounded heap.
func (s *MemStore) AppendOp(op *Op) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch op.Kind {
	case OpAdd:
		set := s.getOrCreateSet(op.Key)
		if set == nil {
			return ErrWrongType
		}
		// apply to state with provided timestamp
		s.applySetOp(set, op.NodeID, op.Element, true, op.TS)
	case OpRemove:
		set := s.getOrCreateSet(op.Key)
		if set == nil {
			return ErrWrongType
		}
		s.applySetOp(set, op.NodeID, op.Element, false, op.TS)
	case OpCounterInc:
		cnt := s.getOrCreateCounter(op.Key)
		if cnt == nil {
			return ErrWrongType
		}
		s.applyCounterOp(cnt, op.NodeID, op.Value, op.TS)
	default:
		// ignore unknown ops
	}

	evicted := s.ops.Append(op)
	if len(evicted) > 0 {
		s.metrics.TotalOpsEvicted += uint64(len(evicted))
		s.metrics.LastEvictAt = time.Now()
	}
	s.metrics.TotalOpsAppended++
	return nil
}

// applySetOp applies a set operation with the provided timestamp
func (s *MemStore) applySetOp(set *LWWSet, nodeID, element string, isAdd bool, ts VectorClock) {
	if len(ts) == 0 {
		// If no timestamp provided, use the set's current timestamp and increment
		ts = make(VectorClock)
		for k, v := range set.Timestamp {
			ts[k] = v
		}
		ts.Increment(nodeID)
	}

	if isAdd {
		set.Adds[element] = ts
	} else {
		set.Removes[element] = ts
	}
	// Update set's timestamp to the maximum of current and operation timestamp
	set.Timestamp = set.Timestamp.Merge(ts)
}

// applyCounterOp applies a counter operation with the provided timestamp
func (s *MemStore) applyCounterOp(cnt *Counter, nodeID string, value uint64, ts VectorClock) {
	cnt.IncrementWithTimestamp(nodeID, value, ts)
}

// Read-only accessors

func (s *MemStore) GetSetElements(key string) ([]string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.objects[key]
	if !ok {
		return nil, false
	}
	set, ok := v.(*LWWSet)
	if !ok {
		return nil, false
	}
	return set.GetElements(), true
}

func (s *MemStore) GetCounterValue(key string) (uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.objects[key]
	if !ok {
		return 0, false
	}
	cnt, ok := v.(*Counter)
	if !ok {
		return 0, false
	}
	return cnt.Value(), true
}
