package crdt

import (
	"testing"
	"time"
)

func TestVectorClockCompare(t *testing.T) {
	tests := []struct {
		name     string
		vc1      VectorClock
		vc2      VectorClock
		expected int
	}{
		{
			name:     "equal clocks",
			vc1:      VectorClock{"A": 1, "B": 2},
			vc2:      VectorClock{"A": 1, "B": 2},
			expected: 0,
		},
		{
			name:     "vc1 happens-before vc2",
			vc1:      VectorClock{"A": 1, "B": 1},
			vc2:      VectorClock{"A": 1, "B": 2},
			expected: -1,
		},
		{
			name:     "vc1 happens-after vc2",
			vc1:      VectorClock{"A": 2, "B": 2},
			vc2:      VectorClock{"A": 1, "B": 1},
			expected: 1,
		},
		{
			name:     "concurrent clocks",
			vc1:      VectorClock{"A": 2, "B": 1},
			vc2:      VectorClock{"A": 1, "B": 2},
			expected: 0,
		},
		{
			name:     "vc1 has extra node",
			vc1:      VectorClock{"A": 1, "B": 1, "C": 1},
			vc2:      VectorClock{"A": 1, "B": 1},
			expected: 1,
		},
		{
			name:     "vc2 has extra node",
			vc1:      VectorClock{"A": 1, "B": 1},
			vc2:      VectorClock{"A": 1, "B": 1, "C": 1},
			expected: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.vc1.Compare(tt.vc2)
			if result != tt.expected {
				t.Errorf("Compare() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestVectorClockMerge(t *testing.T) {
	vc1 := VectorClock{"A": 1, "B": 2}
	vc2 := VectorClock{"B": 1, "C": 3}

	merged := vc1.Merge(vc2)

	expected := VectorClock{"A": 1, "B": 2, "C": 3}

	if len(merged) != len(expected) {
		t.Errorf("Merge() length = %v, want %v", len(merged), len(expected))
	}

	for k, v := range expected {
		if merged[k] != v {
			t.Errorf("Merge()[%s] = %v, want %v", k, merged[k], v)
		}
	}
}

func TestLWWSetOperations(t *testing.T) {
	set := NewLWWSet("test")

	// Add elements
	set.Add("node1", "element1")
	set.Add("node2", "element2")

	// Check elements exist
	if !set.Contains("element1") {
		t.Error("element1 should be in set")
	}
	if !set.Contains("element2") {
		t.Error("element2 should be in set")
	}

	// Remove element
	set.Remove("node1", "element1")
	if set.Contains("element1") {
		t.Error("element1 should not be in set after removal")
	}

	// Check remaining elements
	elements := set.GetElements()
	if len(elements) != 1 || elements[0] != "element2" {
		t.Errorf("GetElements() = %v, want [element2]", elements)
	}
}

func TestLWWSetMerge(t *testing.T) {
	set1 := NewLWWSet("test")
	set2 := NewLWWSet("test")

	// Add different elements to each set
	set1.Add("node1", "element1")
	set1.Add("node1", "element2")

	set2.Add("node2", "element2")
	set2.Add("node2", "element3")

	// Merge sets
	merged, err := set1.Merge(set2)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}

	mergedSet := merged.(*LWWSet)
	elements := mergedSet.GetElements()

	// Should contain all elements
	expected := []string{"element1", "element2", "element3"}
	if len(elements) != len(expected) {
		t.Errorf("Merged set length = %v, want %v", len(elements), len(expected))
	}

	for _, elem := range expected {
		if !mergedSet.Contains(elem) {
			t.Errorf("Merged set should contain %s", elem)
		}
	}
}

func TestCounterOperations(t *testing.T) {
	counter := NewCounter("test")

	// Increment from different nodes
	counter.Increment("node1", 5)
	counter.Increment("node2", 3)
	counter.Increment("node1", 2)

	// Check total value
	expected := uint64(10) // 5 + 3 + 2
	if counter.Value() != expected {
		t.Errorf("Counter.Value() = %v, want %v", counter.Value(), expected)
	}

	// Check individual node values
	if counter.Counters["node1"] != 7 {
		t.Errorf("node1 counter = %v, want 7", counter.Counters["node1"])
	}
	if counter.Counters["node2"] != 3 {
		t.Errorf("node2 counter = %v, want 3", counter.Counters["node2"])
	}
}

func TestCounterMerge(t *testing.T) {
	counter1 := NewCounter("test")
	counter2 := NewCounter("test")

	// Increment different nodes in each counter
	counter1.Increment("node1", 5)
	counter1.Increment("node2", 3)

	counter2.Increment("node2", 2)
	counter2.Increment("node3", 4)

	// Merge counters
	merged, err := counter1.Merge(counter2)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}

	mergedCounter := merged.(*Counter)

	// Check total value
	// For G-Counters, we take max per node: max(5,0) + max(3,2) + max(0,4) = 5 + 3 + 4 = 12
	expected := uint64(12)
	if mergedCounter.Value() != expected {
		t.Errorf("Merged counter value = %v, want %v", mergedCounter.Value(), expected)
	}

	// Check individual node values
	if mergedCounter.Counters["node1"] != 5 {
		t.Errorf("node1 counter = %v, want 5", mergedCounter.Counters["node1"])
	}
	if mergedCounter.Counters["node2"] != 3 { // max(3, 2) = 3 for G-Counter
		t.Errorf("node2 counter = %v, want 3", mergedCounter.Counters["node2"])
	}
	if mergedCounter.Counters["node3"] != 4 {
		t.Errorf("node3 counter = %v, want 4", mergedCounter.Counters["node3"])
	}
}

func TestMemStoreOperations(t *testing.T) {
	store := NewMemStore(1024 * 1024) // 1MB limit

	// Test set operations
	err := store.AppendOp(&Op{
		Key:      "test-set",
		Kind:     OpAdd,
		NodeID:   "node1",
		Element:  "element1",
		TS:       VectorClock{"node1": 1},
		WallTime: time.Now(),
	})
	if err != nil {
		t.Fatalf("AppendOp() error = %v", err)
	}

	elements, exists := store.GetSetElements("test-set")
	if !exists {
		t.Error("Set should exist")
	}
	if len(elements) != 1 || elements[0] != "element1" {
		t.Errorf("GetSetElements() = %v, want [element1]", elements)
	}

	// Test counter operations
	err = store.AppendOp(&Op{
		Key:      "test-counter",
		Kind:     OpCounterInc,
		NodeID:   "node1",
		Value:    5,
		TS:       VectorClock{"node1": 1},
		WallTime: time.Now(),
	})
	if err != nil {
		t.Fatalf("AppendOp() error = %v", err)
	}

	value, exists := store.GetCounterValue("test-counter")
	if !exists {
		t.Error("Counter should exist")
	}
	if value != 5 {
		t.Errorf("GetCounterValue() = %v, want 5", value)
	}
}

func TestOpHeapEviction(t *testing.T) {
	heap := NewOpHeap(100) // Small limit for testing

	// Add operations until eviction occurs
	var evicted []*Op
	for i := 0; i < 10; i++ {
		op := &Op{
			Key:      "test",
			Kind:     OpAdd,
			NodeID:   "node1",
			Element:  "element",
			TS:       VectorClock{"node1": uint64(i)},
			WallTime: time.Now(),
		}
		evicted = append(evicted, heap.Append(op)...)
	}

	// Should have some evicted operations
	if len(evicted) == 0 {
		t.Error("Expected some operations to be evicted")
	}

	// Heap should be within capacity
	if heap.Bytes() > 100 {
		t.Errorf("Heap bytes = %v, should be <= 100", heap.Bytes())
	}
}

func TestConcurrentOperations(t *testing.T) {
	// Test concurrent operations on the same CRDT
	set1 := NewLWWSet("test")
	set2 := NewLWWSet("test")

	// Simulate concurrent operations with different timestamps
	set1.Add("node1", "element1")
	set1.Timestamp = VectorClock{"node1": 1}

	set2.Add("node2", "element1") // Same element, different node
	set2.Timestamp = VectorClock{"node2": 1}

	// Merge should handle concurrent operations correctly
	merged, err := set1.Merge(set2)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}

	mergedSet := merged.(*LWWSet)

	// Element should still be in set (last writer wins)
	if !mergedSet.Contains("element1") {
		t.Error("Element should be in merged set")
	}
}
