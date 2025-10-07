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
	// More accurate memory estimation
	var n int64 = 64 // struct overhead (pointer + fields)

	// String sizes (Go strings have 16-byte header + data)
	n += 16 + int64(len(o.Key))     // Key string
	n += 16 + int64(len(o.NodeID))  // NodeID string
	n += 16 + int64(len(o.Element)) // Element string

	// VectorClock map size: map overhead + entries
	// Each map entry: key (string) + value (uint64) + map overhead
	n += 8 // map header
	for nodeID := range o.TS {
		n += 16 + int64(len(nodeID)) + 8 // string key + uint64 value
	}

	// Time.Time is 24 bytes
	n += 24

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

func (h *OpHeap) Len() int     { h.mu.Lock(); defer h.mu.Unlock(); return h.ops.Len() }
func (h *OpHeap) Bytes() int64 { h.mu.Lock(); defer h.mu.Unlock(); return h.bytes }
