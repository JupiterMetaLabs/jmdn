package crdt

import (
	"container/list"
	"sync"
	"time"
)

type OpKind uint8

const (
	OpAdd OpKind = iota + 1
	OpRemove
	OpCounterInc
)

type Op struct {
	Key     string // crdt key
	Kind    OpKind
	NodeID  string
	Element string      // for sets
	Value   uint64      // for counters
	TS      VectorClock // causal metadata if you want to carry it with the op
	// Optional: wall clock to support heuristic compaction/metrics
	WallTime time.Time
}

func (o *Op) approxSize() int64 {
	// More accurate memory estimation accounting for Go's memory layout
	var n int64 = 0

	// Struct overhead: pointer (8 bytes) + field alignment
	n += 8 // pointer to Op struct

	// Field sizes with proper alignment
	n += 8  // Key string pointer
	n += 1  // Kind (OpKind)
	n += 7  // padding for alignment
	n += 8  // NodeID string pointer
	n += 8  // Element string pointer
	n += 8  // Value (uint64)
	n += 8  // TS (VectorClock) map pointer
	n += 24 // WallTime (time.Time)

	// String sizes (Go strings: 16-byte header + data)
	n += 16 + int64(len(o.Key))     // Key string
	n += 16 + int64(len(o.NodeID))  // NodeID string
	n += 16 + int64(len(o.Element)) // Element string

	// VectorClock map size with more accurate estimation
	// Map header: 8 bytes + bucket array overhead
	n += 8 + 8 // map header + bucket array pointer

	// Each map entry: key (string) + value (uint64) + map overhead
	// Go maps have additional overhead per entry
	for nodeID := range o.TS {
		n += 16 + int64(len(nodeID)) + 8 + 8 // string key + uint64 value + map entry overhead
	}

	// Additional overhead for map growth and hash table
	n += int64(len(o.TS)) * 2 // Additional overhead for map structure

	return n
}

// OpHeap is an append-only FIFO with byte-bound capacity.
type OpHeap struct {
	mu       sync.Mutex
	ops      *list.List // *Op
	bytes    int64
	capBytes int64
}

func NewOpHeap(capBytes int64) *OpHeap {
	return &OpHeap{
		ops:      list.New(),
		capBytes: capBytes,
	}
}

func (h *OpHeap) Append(op *Op) (evicted []*Op) {
	h.mu.Lock()
	defer h.mu.Unlock()

	size := op.approxSize()
	h.ops.PushBack(op)
	h.bytes += size

	for h.bytes > h.capBytes && h.ops.Len() > 0 {
		front := h.ops.Front()
		old := front.Value.(*Op)
		h.bytes -= old.approxSize()
		h.ops.Remove(front)
		evicted = append(evicted, old)
	}
	return evicted
}

// HeapStats represents atomic snapshot of heap statistics
type HeapStats struct {
	Len   int
	Bytes int64
}

// Stats returns an atomic snapshot of heap statistics
func (h *OpHeap) Stats() HeapStats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return HeapStats{
		Len:   h.ops.Len(),
		Bytes: h.bytes,
	}
}

// Len returns the number of operations in the heap
// Deprecated: Use Stats() for atomic access to avoid race conditions
func (h *OpHeap) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ops.Len()
}

// Bytes returns the current byte usage of the heap
// Deprecated: Use Stats() for atomic access to avoid race conditions
func (h *OpHeap) Bytes() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bytes
}
