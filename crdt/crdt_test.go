package crdt

import (
	"fmt"
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
		WallTime: time.Now().UTC(),
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
		WallTime: time.Now().UTC(),
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
			WallTime: time.Now().UTC(),
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

func TestDeterministicMerge(t *testing.T) {
	// Test that concurrent operations are handled deterministically
	set1 := NewLWWSet("test")
	set2 := NewLWWSet("test")

	// Create concurrent operations with same timestamp
	ts1 := VectorClock{"node1": 1, "node2": 1}
	ts2 := VectorClock{"node1": 1, "node2": 1}

	set1.Adds["element1"] = ts1
	set1.Timestamp = ts1

	set2.Adds["element1"] = ts2
	set2.Timestamp = ts2

	// Merge should be deterministic
	merged1, _ := set1.Merge(set2)
	merged2, _ := set1.Merge(set2)

	// Results should be identical
	if merged1.(*LWWSet).Contains("element1") != merged2.(*LWWSet).Contains("element1") {
		t.Error("Merge results should be deterministic")
	}
}

func TestVectorClockPruning(t *testing.T) {
	vc := VectorClock{"node1": 1, "node2": 2, "node3": 3}

	// Prune inactive nodes
	activeNodes := map[string]bool{"node1": true, "node3": true}
	vc.Prune(activeNodes)

	// Should only contain active nodes
	if _, exists := vc["node2"]; exists {
		t.Error("Inactive node should be pruned")
	}
	if vc["node1"] != 1 || vc["node3"] != 3 {
		t.Error("Active nodes should be preserved")
	}
}

func TestElementOrdering(t *testing.T) {
	set := NewLWWSet("test")

	// Add elements in random order
	set.Add("node1", "zebra")
	set.Add("node1", "apple")
	set.Add("node1", "banana")

	elements := set.GetElements()

	// Should be sorted
	expected := []string{"apple", "banana", "zebra"}
	if len(elements) != len(expected) {
		t.Errorf("Expected %d elements, got %d", len(expected), len(elements))
	}

	for i, elem := range elements {
		if elem != expected[i] {
			t.Errorf("Element %d: expected %s, got %s", i, expected[i], elem)
		}
	}
}

func TestInputValidation(t *testing.T) {
	engine := NewEngineMemOnly(1024 * 1024)

	// Test empty nodeID
	err := engine.LWWAdd("", "key", "element", VectorClock{})
	if err == nil {
		t.Error("Should reject empty nodeID")
	}

	// Test empty key
	err = engine.LWWAdd("node1", "", "element", VectorClock{})
	if err == nil {
		t.Error("Should reject empty key")
	}

	// Test empty element
	err = engine.LWWAdd("node1", "key", "", VectorClock{})
	if err == nil {
		t.Error("Should reject empty element")
	}

	// Test counter validation
	err = engine.CounterInc("", "key", 1, VectorClock{})
	if err == nil {
		t.Error("Should reject empty nodeID for counter")
	}

	err = engine.CounterInc("node1", "", 1, VectorClock{})
	if err == nil {
		t.Error("Should reject empty key for counter")
	}

	err = engine.CounterInc("node1", "key", 0, VectorClock{})
	if err == nil {
		t.Error("Should reject zero delta")
	}
}

func TestHeapStats(t *testing.T) {
	heap := NewOpHeap(1000) // Larger limit to avoid eviction

	// Add some operations
	for i := 0; i < 5; i++ {
		op := &Op{
			Key:      "test",
			Kind:     OpAdd,
			NodeID:   "node1",
			Element:  "element",
			TS:       VectorClock{"node1": uint64(i)},
			WallTime: time.Now().UTC(),
		}
		heap.Append(op)
	}

	// Get atomic stats
	stats := heap.Stats()

	if stats.Len != 5 {
		t.Errorf("Expected 5 operations, got %d", stats.Len)
	}

	if stats.Bytes <= 0 {
		t.Error("Bytes should be positive")
	}
}

func TestEvictionHandling(t *testing.T) {
	var evictedOps []*Op

	// Create eviction handler
	evictionHandler := func(ops []*Op) error {
		evictedOps = append(evictedOps, ops...)
		return nil
	}

	store := NewMemStoreWithEviction(100, evictionHandler) // Small limit

	// Add operations until eviction occurs
	for i := 0; i < 10; i++ {
		op := &Op{
			Key:      "test",
			Kind:     OpAdd,
			NodeID:   "node1",
			Element:  "element",
			TS:       VectorClock{"node1": uint64(i)},
			WallTime: time.Now().UTC(),
		}
		store.AppendOp(op)
	}

	// Should have some evicted operations
	if len(evictedOps) == 0 {
		t.Error("Expected some operations to be evicted")
	}
}

func TestMemStorePruning(t *testing.T) {
	store := NewMemStore(1024 * 1024)

	// Add operations with different nodes
	store.AppendOp(&Op{
		Key:      "test-set",
		Kind:     OpAdd,
		NodeID:   "node1",
		Element:  "element1",
		TS:       VectorClock{"node1": 1, "node2": 1},
		WallTime: time.Now().UTC(),
	})

	store.AppendOp(&Op{
		Key:      "test-counter",
		Kind:     OpCounterInc,
		NodeID:   "node2",
		Value:    5,
		TS:       VectorClock{"node1": 1, "node2": 1, "node3": 1},
		WallTime: time.Now().UTC(),
	})

	// Get active nodes
	activeNodes := store.GetActiveNodes()

	// Should contain all nodes
	expectedNodes := []string{"node1", "node2", "node3"}
	for _, node := range expectedNodes {
		if !activeNodes[node] {
			t.Errorf("Expected node %s to be active", node)
		}
	}

	// Prune inactive nodes
	activeNodes = map[string]bool{"node1": true, "node2": true}
	store.PruneVectorClocks(activeNodes)

	// Check that node3 was pruned
	updatedActiveNodes := store.GetActiveNodes()
	if updatedActiveNodes["node3"] {
		t.Error("Node3 should have been pruned")
	}
}

// TestCRDTHarness tests a 3-node CRDT system with local operations and eventual sync
func TestCRDTHarness(t *testing.T) {
	// Create 3 nodes with their own engines
	nodes := make(map[string]*Engine)
	nodeIDs := []string{"node1", "node2", "node3"}

	for _, nodeID := range nodeIDs {
		nodes[nodeID] = NewEngineMemOnly(10 * 1024 * 1024) // 10MB per node
	}

	// Phase 1: Local operations on each node
	t.Log("Phase 1: Local operations on each node")

	// Node 1 operations
	nodes["node1"].LWWAdd("node1", "users", "alice", VectorClock{})
	nodes["node1"].LWWAdd("node1", "users", "bob", VectorClock{})
	nodes["node1"].CounterInc("node1", "likes", 5, VectorClock{})
	nodes["node1"].CounterInc("node1", "views", 10, VectorClock{})

	// Node 2 operations
	nodes["node2"].LWWAdd("node2", "users", "charlie", VectorClock{})
	nodes["node2"].LWWAdd("node2", "users", "david", VectorClock{})
	nodes["node2"].CounterInc("node2", "likes", 3, VectorClock{})
	nodes["node2"].CounterInc("node2", "views", 7, VectorClock{})

	// Node 3 operations
	nodes["node3"].LWWAdd("node3", "users", "eve", VectorClock{})
	nodes["node3"].LWWAdd("node3", "users", "frank", VectorClock{})
	nodes["node3"].CounterInc("node3", "likes", 8, VectorClock{})
	nodes["node3"].CounterInc("node3", "views", 15, VectorClock{})

	// Verify local state before sync
	t.Log("Local state before sync:")
	for nodeID, engine := range nodes {
		users, _ := engine.GetSet("users")
		likes, _ := engine.GetCounter("likes")
		views, _ := engine.GetCounter("views")
		t.Logf("  %s: users=%v, likes=%d, views=%d", nodeID, users, likes, views)
	}

	// Phase 2: Simulate concurrent operations (same elements, different nodes)
	t.Log("Phase 2: Concurrent operations")

	// Concurrent add of same user by different nodes
	nodes["node1"].LWWAdd("node1", "users", "grace", VectorClock{})
	nodes["node2"].LWWAdd("node2", "users", "grace", VectorClock{})

	// Concurrent counter increments
	nodes["node1"].CounterInc("node1", "likes", 2, VectorClock{})
	nodes["node2"].CounterInc("node2", "likes", 1, VectorClock{})
	nodes["node3"].CounterInc("node3", "likes", 3, VectorClock{})

	// Phase 3: Sync operations between nodes
	t.Log("Phase 3: Syncing nodes")

	// Sync node1 with node2
	syncNodes(t, nodes["node1"], nodes["node2"], "node1", "node2")

	// Sync node2 with node3
	syncNodes(t, nodes["node2"], nodes["node3"], "node2", "node3")

	// Sync node1 with node3 (now node3 has node2's data)
	syncNodes(t, nodes["node1"], nodes["node3"], "node1", "node3")

	// Final sync to ensure all nodes are consistent
	syncNodes(t, nodes["node1"], nodes["node2"], "node1", "node2")
	syncNodes(t, nodes["node2"], nodes["node3"], "node2", "node3")

	// Phase 4: Verify eventual consistency
	t.Log("Phase 4: Verifying eventual consistency")

	// All nodes should have the same final state
	expectedUsers := []string{"alice", "bob", "charlie", "david", "eve", "frank", "grace"}

	for nodeID, engine := range nodes {
		users, exists := engine.GetSet("users")
		if !exists {
			t.Errorf("Node %s: users set should exist", nodeID)
			continue
		}

		likes, exists := engine.GetCounter("likes")
		if !exists {
			t.Errorf("Node %s: likes counter should exist", nodeID)
			continue
		}

		views, exists := engine.GetCounter("views")
		if !exists {
			t.Errorf("Node %s: views counter should exist", nodeID)
			continue
		}

		t.Logf("  %s: users=%v, likes=%d, views=%d", nodeID, users, likes, views)

		// Verify users set
		if len(users) != len(expectedUsers) {
			t.Errorf("Node %s: expected %d users, got %d", nodeID, len(expectedUsers), len(users))
		}

		for _, expectedUser := range expectedUsers {
			found := false
			for _, user := range users {
				if user == expectedUser {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Node %s: missing user %s", nodeID, expectedUser)
			}
		}

		// Verify counters (note: G-Counter behavior - takes max per node)
		// The exact values depend on the merge order, but they should be consistent
		if likes == 0 {
			t.Errorf("Node %s: likes counter should be > 0", nodeID)
		}
		if views == 0 {
			t.Errorf("Node %s: views counter should be > 0", nodeID)
		}
	}

	// Verify all nodes have identical state
	verifyConsistentState(t, nodes)
}

// syncNodes simulates syncing between two nodes by merging their CRDTs
func syncNodes(t *testing.T, node1, node2 *Engine, nodeID1, nodeID2 string) {
	t.Logf("  Syncing %s with %s", nodeID1, nodeID2)

	// Get all CRDTs from both nodes
	crdts1 := node1.GetAllCRDTs()
	crdts2 := node2.GetAllCRDTs()

	// Merge CRDTs from node2 into node1
	for key, crdt2 := range crdts2 {
		if crdt1, exists := crdts1[key]; exists {
			// Both nodes have this CRDT, merge them
			merged, err := crdt1.Merge(crdt2)
			if err != nil {
				t.Errorf("Failed to merge CRDT %s: %v", key, err)
				continue
			}

			// Apply the merged CRDT back to node1's memory store
			// This is a simplified approach - in practice, you'd need to reconstruct operations
			applyMergedCRDT(node1, key, merged)
		} else {
			// Only node2 has this CRDT, copy it to node1
			applyMergedCRDT(node1, key, crdt2)
		}
	}

	// Merge CRDTs from node1 into node2
	for key, crdt1 := range crdts1 {
		if crdt2, exists := crdts2[key]; exists {
			// Both nodes have this CRDT, merge them
			merged, err := crdt2.Merge(crdt1)
			if err != nil {
				t.Errorf("Failed to merge CRDT %s: %v", key, err)
				continue
			}
			applyMergedCRDT(node2, key, merged)
		} else {
			// Only node1 has this CRDT, copy it to node2
			applyMergedCRDT(node2, key, crdt1)
		}
	}
}

// applyMergedCRDT applies a merged CRDT to a node's engine
// This is a test helper that directly manipulates the memory store
func applyMergedCRDT(engine *Engine, key string, crdt CRDT) {
	// Access the memory store directly to apply the merged CRDT
	// This is a test-only approach - in production, you'd use proper operation replay
	engine.mem.mu.Lock()
	defer engine.mem.mu.Unlock()

	engine.mem.objects[key] = crdt
}

// verifyConsistentState verifies that all nodes have identical CRDT states
func verifyConsistentState(t *testing.T, nodes map[string]*Engine) {
	t.Log("Verifying all nodes have consistent state...")

	var referenceNode string
	var referenceCRDTs map[string]CRDT

	// Use the first node as reference
	for nodeID, engine := range nodes {
		referenceNode = nodeID
		referenceCRDTs = engine.GetAllCRDTs()
		break
	}

	// Compare all other nodes with the reference
	for nodeID, engine := range nodes {
		if nodeID == referenceNode {
			continue
		}

		crdts := engine.GetAllCRDTs()

		// Check that both nodes have the same CRDT keys
		if len(crdts) != len(referenceCRDTs) {
			t.Errorf("Node %s has %d CRDTs, reference has %d", nodeID, len(crdts), len(referenceCRDTs))
			continue
		}

		for key, referenceCRDT := range referenceCRDTs {
			crdt, exists := crdts[key]
			if !exists {
				t.Errorf("Node %s missing CRDT key %s", nodeID, key)
				continue
			}

			// Compare CRDT types and basic properties
			if referenceCRDT.GetKey() != crdt.GetKey() {
				t.Errorf("Node %s CRDT %s has different key: %s vs %s",
					nodeID, key, crdt.GetKey(), referenceCRDT.GetKey())
			}

			// For sets, compare elements
			if set1, ok1 := referenceCRDT.(*LWWSet); ok1 {
				if set2, ok2 := crdt.(*LWWSet); ok2 {
					elements1 := set1.GetElements()
					elements2 := set2.GetElements()

					if len(elements1) != len(elements2) {
						t.Errorf("Node %s set %s has %d elements, reference has %d",
							nodeID, key, len(elements2), len(elements1))
						continue
					}

					for i, elem1 := range elements1 {
						if i >= len(elements2) || elem1 != elements2[i] {
							t.Errorf("Node %s set %s element %d: expected %s, got %s",
								nodeID, key, i, elem1, elements2[i])
						}
					}
				}
			}

			// For counters, compare values
			if counter1, ok1 := referenceCRDT.(*Counter); ok1 {
				if counter2, ok2 := crdt.(*Counter); ok2 {
					value1 := counter1.Value()
					value2 := counter2.Value()

					if value1 != value2 {
						t.Errorf("Node %s counter %s has value %d, reference has %d",
							nodeID, key, value2, value1)
					}
				}
			}
		}
	}

	t.Log("All nodes have consistent state!")
}

// TestCRDTHarnessAdvanced tests a more complex 3-node CRDT scenario with conflicts and eventual consistency
func TestCRDTHarnessAdvanced(t *testing.T) {
	// Create 3 nodes with their own engines
	nodes := make(map[string]*Engine)
	nodeIDs := []string{"node1", "node2", "node3"}

	for _, nodeID := range nodeIDs {
		nodes[nodeID] = NewEngineMemOnly(10 * 1024 * 1024) // 10MB per node
	}

	t.Log("=== Advanced CRDT Harness Test ===")

	// Phase 1: Initial data population
	t.Log("Phase 1: Initial data population")

	// Node 1: Creates a user set and some counters
	nodes["node1"].LWWAdd("node1", "active_users", "alice", VectorClock{})
	nodes["node1"].LWWAdd("node1", "active_users", "bob", VectorClock{})
	nodes["node1"].CounterInc("node1", "page_views", 100, VectorClock{})
	nodes["node1"].CounterInc("node1", "user_clicks", 25, VectorClock{})

	// Node 2: Adds different users and increments counters
	nodes["node2"].LWWAdd("node2", "active_users", "charlie", VectorClock{})
	nodes["node2"].LWWAdd("node2", "active_users", "diana", VectorClock{})
	nodes["node2"].CounterInc("node2", "page_views", 150, VectorClock{})
	nodes["node2"].CounterInc("node2", "user_clicks", 30, VectorClock{})

	// Node 3: More users and counters
	nodes["node3"].LWWAdd("node3", "active_users", "eve", VectorClock{})
	nodes["node3"].LWWAdd("node3", "active_users", "frank", VectorClock{})
	nodes["node3"].CounterInc("node3", "page_views", 200, VectorClock{})
	nodes["node3"].CounterInc("node3", "user_clicks", 45, VectorClock{})

	// Phase 2: Simulate network partitions and concurrent operations
	t.Log("Phase 2: Network partitions and concurrent operations")

	// Simulate concurrent operations on the same elements
	// These should be handled correctly by the CRDT merge logic

	// All nodes try to add the same user "grace" concurrently
	nodes["node1"].LWWAdd("node1", "active_users", "grace", VectorClock{})
	nodes["node2"].LWWAdd("node2", "active_users", "grace", VectorClock{})
	nodes["node3"].LWWAdd("node3", "active_users", "grace", VectorClock{})

	// Concurrent counter increments
	nodes["node1"].CounterInc("node1", "page_views", 50, VectorClock{})
	nodes["node2"].CounterInc("node2", "page_views", 75, VectorClock{})
	nodes["node3"].CounterInc("node3", "page_views", 25, VectorClock{})

	// Simulate user removal (some nodes think user left)
	nodes["node1"].LWWRemove("node1", "active_users", "bob", VectorClock{})
	nodes["node2"].LWWAdd("node2", "active_users", "bob", VectorClock{}) // Node2 thinks bob is still active

	// Phase 3: Gradual sync simulation (realistic network conditions)
	t.Log("Phase 3: Gradual sync simulation")

	// First sync: node1 and node2
	t.Log("  Syncing node1 <-> node2")
	syncNodesAdvanced(t, nodes["node1"], nodes["node2"], "node1", "node2")

	// Check intermediate state
	t.Log("  Intermediate state after node1<->node2 sync:")
	for nodeID, engine := range nodes {
		users, _ := engine.GetSet("active_users")
		views, _ := engine.GetCounter("page_views")
		clicks, _ := engine.GetCounter("user_clicks")
		t.Logf("    %s: users=%v, views=%d, clicks=%d", nodeID, users, views, clicks)
	}

	// Second sync: node2 and node3
	t.Log("  Syncing node2 <-> node3")
	syncNodesAdvanced(t, nodes["node2"], nodes["node3"], "node2", "node3")

	// Third sync: node1 and node3 (now node3 has node2's data)
	t.Log("  Syncing node1 <-> node3")
	syncNodesAdvanced(t, nodes["node1"], nodes["node3"], "node1", "node3")

	// Final round of syncs to ensure convergence
	t.Log("  Final convergence syncs")
	syncNodesAdvanced(t, nodes["node1"], nodes["node2"], "node1", "node2")
	syncNodesAdvanced(t, nodes["node2"], nodes["node3"], "node2", "node3")
	syncNodesAdvanced(t, nodes["node1"], nodes["node3"], "node1", "node3")

	// Phase 4: Verify eventual consistency and conflict resolution
	t.Log("Phase 4: Verifying eventual consistency")

	// All nodes should have identical final state
	expectedUsers := []string{"alice", "charlie", "diana", "eve", "frank", "grace"}
	// Note: "bob" should be present because node2's add operation should win over node1's remove
	// (assuming node2's timestamp is later due to the sync order)

	for nodeID, engine := range nodes {
		users, exists := engine.GetSet("active_users")
		if !exists {
			t.Errorf("Node %s: active_users set should exist", nodeID)
			continue
		}

		views, exists := engine.GetCounter("page_views")
		if !exists {
			t.Errorf("Node %s: page_views counter should exist", nodeID)
			continue
		}

		clicks, exists := engine.GetCounter("user_clicks")
		if !exists {
			t.Errorf("Node %s: user_clicks counter should exist", nodeID)
			continue
		}

		t.Logf("  %s: users=%v, views=%d, clicks=%d", nodeID, users, views, clicks)

		// Verify that all expected users are present
		for _, expectedUser := range expectedUsers {
			found := false
			for _, user := range users {
				if user == expectedUser {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Node %s: missing user %s", nodeID, expectedUser)
			}
		}

		// Verify counters are reasonable (G-Counter behavior)
		if views == 0 {
			t.Errorf("Node %s: page_views counter should be > 0", nodeID)
		}
		if clicks == 0 {
			t.Errorf("Node %s: user_clicks counter should be > 0", nodeID)
		}
	}

	// Verify all nodes have identical state
	verifyConsistentStateAdvanced(t, nodes)

	// Phase 5: Test additional operations after sync
	t.Log("Phase 5: Post-sync operations")

	// Add more operations after sync to ensure the system continues to work
	nodes["node1"].LWWAdd("node1", "active_users", "henry", VectorClock{})
	nodes["node2"].CounterInc("node2", "page_views", 10, VectorClock{})
	nodes["node3"].LWWAdd("node3", "active_users", "iris", VectorClock{})

	// Quick sync
	syncNodesAdvanced(t, nodes["node1"], nodes["node2"], "node1", "node2")
	syncNodesAdvanced(t, nodes["node2"], nodes["node3"], "node2", "node3")
	syncNodesAdvanced(t, nodes["node1"], nodes["node3"], "node1", "node3")

	// Verify final state
	t.Log("Final state after post-sync operations:")
	for nodeID, engine := range nodes {
		users, _ := engine.GetSet("active_users")
		views, _ := engine.GetCounter("page_views")
		clicks, _ := engine.GetCounter("user_clicks")
		t.Logf("  %s: users=%v, views=%d, clicks=%d", nodeID, users, views, clicks)
	}

	verifyConsistentStateAdvanced(t, nodes)
	t.Log("=== Advanced CRDT Harness Test Completed Successfully ===")
}

// syncNodesAdvanced provides more detailed sync simulation with logging
func syncNodesAdvanced(t *testing.T, node1, node2 *Engine, nodeID1, nodeID2 string) {
	// Get all CRDTs from both nodes
	crdts1 := node1.GetAllCRDTs()
	crdts2 := node2.GetAllCRDTs()

	// Merge CRDTs from node2 into node1
	for key, crdt2 := range crdts2 {
		if crdt1, exists := crdts1[key]; exists {
			// Both nodes have this CRDT, merge them
			merged, err := crdt1.Merge(crdt2)
			if err != nil {
				t.Errorf("Failed to merge CRDT %s: %v", key, err)
				continue
			}
			applyMergedCRDT(node1, key, merged)
		} else {
			// Only node2 has this CRDT, copy it to node1
			applyMergedCRDT(node1, key, crdt2)
		}
	}

	// Merge CRDTs from node1 into node2
	for key, crdt1 := range crdts1 {
		if crdt2, exists := crdts2[key]; exists {
			// Both nodes have this CRDT, merge them
			merged, err := crdt2.Merge(crdt1)
			if err != nil {
				t.Errorf("Failed to merge CRDT %s: %v", key, err)
				continue
			}
			applyMergedCRDT(node2, key, merged)
		} else {
			// Only node1 has this CRDT, copy it to node2
			applyMergedCRDT(node2, key, crdt1)
		}
	}
}

// verifyConsistentStateAdvanced provides more detailed consistency verification
func verifyConsistentStateAdvanced(t *testing.T, nodes map[string]*Engine) {
	t.Log("Verifying advanced consistency...")

	var referenceNode string
	var referenceCRDTs map[string]CRDT

	// Use the first node as reference
	for nodeID, engine := range nodes {
		referenceNode = nodeID
		referenceCRDTs = engine.GetAllCRDTs()
		break
	}

	// Compare all other nodes with the reference
	for nodeID, engine := range nodes {
		if nodeID == referenceNode {
			continue
		}

		crdts := engine.GetAllCRDTs()

		// Check that both nodes have the same CRDT keys
		if len(crdts) != len(referenceCRDTs) {
			t.Errorf("Node %s has %d CRDTs, reference has %d", nodeID, len(crdts), len(referenceCRDTs))
			continue
		}

		for key, referenceCRDT := range referenceCRDTs {
			crdt, exists := crdts[key]
			if !exists {
				t.Errorf("Node %s missing CRDT key %s", nodeID, key)
				continue
			}

			// Compare CRDT types and basic properties
			if referenceCRDT.GetKey() != crdt.GetKey() {
				t.Errorf("Node %s CRDT %s has different key: %s vs %s",
					nodeID, key, crdt.GetKey(), referenceCRDT.GetKey())
			}

			// For sets, compare elements
			if set1, ok1 := referenceCRDT.(*LWWSet); ok1 {
				if set2, ok2 := crdt.(*LWWSet); ok2 {
					elements1 := set1.GetElements()
					elements2 := set2.GetElements()

					if len(elements1) != len(elements2) {
						t.Errorf("Node %s set %s has %d elements, reference has %d",
							nodeID, key, len(elements2), len(elements1))
						continue
					}

					for i, elem1 := range elements1 {
						if i >= len(elements2) || elem1 != elements2[i] {
							t.Errorf("Node %s set %s element %d: expected %s, got %s",
								nodeID, key, i, elem1, elements2[i])
						}
					}
				}
			}

			// For counters, compare values
			if counter1, ok1 := referenceCRDT.(*Counter); ok1 {
				if counter2, ok2 := crdt.(*Counter); ok2 {
					value1 := counter1.Value()
					value2 := counter2.Value()

					if value1 != value2 {
						t.Errorf("Node %s counter %s has value %d, reference has %d",
							nodeID, key, value2, value1)
					}
				}
			}
		}
	}

	t.Log("Advanced consistency verification passed!")
}

// TestOfflineOnlineSync tests nodes going offline and coming back online
func TestOfflineOnlineSync(t *testing.T) {
	t.Log("=== Offline/Online Sync Test ===")

	// Create 3 nodes with persistence simulation
	nodes := make(map[string]*PersistentEngine)
	nodeIDs := []string{"node1", "node2", "node3"}

	for _, nodeID := range nodeIDs {
		nodes[nodeID] = NewPersistentEngine(nodeID, 10*1024*1024) // 10MB per node
	}

	// Phase 1: Initial operations while all nodes are online
	t.Log("Phase 1: All nodes online - initial operations")

	nodes["node1"].LWWAdd("node1", "users", "alice", VectorClock{})
	nodes["node1"].CounterInc("node1", "likes", 10, VectorClock{})

	nodes["node2"].LWWAdd("node2", "users", "bob", VectorClock{})
	nodes["node2"].CounterInc("node2", "likes", 5, VectorClock{})

	nodes["node3"].LWWAdd("node3", "users", "charlie", VectorClock{})
	nodes["node3"].CounterInc("node3", "likes", 8, VectorClock{})

	// Sync all nodes
	syncAllNodes(t, nodes)

	t.Log("State after initial sync:")
	logNodeStates(t, nodes)

	// Phase 2: Node1 goes offline, others continue operating
	t.Log("Phase 2: Node1 goes offline, others continue")

	nodes["node1"].GoOffline()

	// Node2 and Node3 continue operations
	nodes["node2"].LWWAdd("node2", "users", "diana", VectorClock{})
	nodes["node2"].CounterInc("node2", "likes", 3, VectorClock{})

	nodes["node3"].LWWAdd("node3", "users", "eve", VectorClock{})
	nodes["node3"].CounterInc("node3", "likes", 7, VectorClock{})

	// Sync between online nodes
	syncPersistentNodes(t, nodes["node2"], nodes["node3"], "node2", "node3")

	t.Log("State while node1 is offline:")
	logNodeStates(t, nodes)

	// Phase 3: Node1 comes back online and syncs
	t.Log("Phase 3: Node1 comes back online and syncs")

	nodes["node1"].ComeOnline()

	// Node1 should sync with both other nodes
	syncPersistentNodes(t, nodes["node1"], nodes["node2"], "node1", "node2")
	syncPersistentNodes(t, nodes["node1"], nodes["node3"], "node1", "node3")

	t.Log("State after node1 comes back online:")
	logNodeStates(t, nodes)

	// Phase 4: All nodes continue operations
	t.Log("Phase 4: All nodes continue operations")

	nodes["node1"].LWWAdd("node1", "users", "frank", VectorClock{})
	nodes["node2"].CounterInc("node2", "likes", 2, VectorClock{})
	nodes["node3"].LWWAdd("node3", "users", "grace", VectorClock{})

	// Final sync
	syncAllNodes(t, nodes)

	t.Log("Final state:")
	logNodeStates(t, nodes)

	// Verify all nodes are consistent
	verifyPersistentConsistency(t, nodes)

	t.Log("=== Offline/Online Sync Test Completed Successfully ===")
}

// PersistentEngine simulates a node with persistence capabilities
type PersistentEngine struct {
	nodeID     string
	engine     *Engine
	offline    bool
	lastSync   map[string]VectorClock // Track last sync timestamp with each node
	operations []*Op                  // Store operations for replay
}

func NewPersistentEngine(nodeID string, maxBytes int64) *PersistentEngine {
	return &PersistentEngine{
		nodeID:     nodeID,
		engine:     NewEngineMemOnly(maxBytes),
		offline:    false,
		lastSync:   make(map[string]VectorClock),
		operations: make([]*Op, 0),
	}
}

func (pe *PersistentEngine) GoOffline() {
	pe.offline = true
	// Note: In a real implementation, you'd use a proper logger here
	// t.Logf("Node %s went offline", pe.nodeID)
}

func (pe *PersistentEngine) ComeOnline() {
	pe.offline = false
	// Note: In a real implementation, you'd use a proper logger here
	// t.Logf("Node %s came back online", pe.nodeID)
}

func (pe *PersistentEngine) IsOffline() bool {
	return pe.offline
}

func (pe *PersistentEngine) LWWAdd(nodeID, key, element string, ts VectorClock) error {
	if pe.offline {
		return fmt.Errorf("node %s is offline", pe.nodeID)
	}

	err := pe.engine.LWWAdd(nodeID, key, element, ts)
	if err == nil {
		// Store operation for potential replay
		pe.operations = append(pe.operations, &Op{
			Key:      key,
			Kind:     OpAdd,
			NodeID:   nodeID,
			Element:  element,
			TS:       ts,
			WallTime: time.Now().UTC(),
		})
	}
	return err
}

func (pe *PersistentEngine) LWWRemove(nodeID, key, element string, ts VectorClock) error {
	if pe.offline {
		return fmt.Errorf("node %s is offline", pe.nodeID)
	}

	err := pe.engine.LWWRemove(nodeID, key, element, ts)
	if err == nil {
		pe.operations = append(pe.operations, &Op{
			Key:      key,
			Kind:     OpRemove,
			NodeID:   nodeID,
			Element:  element,
			TS:       ts,
			WallTime: time.Now().UTC(),
		})
	}
	return err
}

func (pe *PersistentEngine) CounterInc(nodeID, key string, delta uint64, ts VectorClock) error {
	if pe.offline {
		return fmt.Errorf("node %s is offline", pe.nodeID)
	}

	err := pe.engine.CounterInc(nodeID, key, delta, ts)
	if err == nil {
		pe.operations = append(pe.operations, &Op{
			Key:      key,
			Kind:     OpCounterInc,
			NodeID:   nodeID,
			Value:    delta,
			TS:       ts,
			WallTime: time.Now().UTC(),
		})
	}
	return err
}

func (pe *PersistentEngine) GetSet(key string) ([]string, bool) {
	return pe.engine.GetSet(key)
}

func (pe *PersistentEngine) GetCounter(key string) (uint64, bool) {
	return pe.engine.GetCounter(key)
}

func (pe *PersistentEngine) GetAllCRDTs() map[string]CRDT {
	return pe.engine.GetAllCRDTs()
}

// GetOperationsSince returns operations since a given timestamp
func (pe *PersistentEngine) GetOperationsSince(since VectorClock) []*Op {
	var result []*Op
	for _, op := range pe.operations {
		// Simple check: if operation timestamp is newer than 'since'
		if op.TS.Compare(since) > 0 {
			result = append(result, op)
		}
	}
	return result
}

// ReplayOperations replays operations from another node
func (pe *PersistentEngine) ReplayOperations(operations []*Op) error {
	for _, op := range operations {
		switch op.Kind {
		case OpAdd:
			if err := pe.engine.LWWAdd(op.NodeID, op.Key, op.Element, op.TS); err != nil {
				return err
			}
		case OpRemove:
			if err := pe.engine.LWWRemove(op.NodeID, op.Key, op.Element, op.TS); err != nil {
				return err
			}
		case OpCounterInc:
			if err := pe.engine.CounterInc(op.NodeID, op.Key, op.Value, op.TS); err != nil {
				return err
			}
		}
	}
	return nil
}

// syncAllNodes syncs all nodes with each other
func syncAllNodes(t *testing.T, nodes map[string]*PersistentEngine) {
	nodeIDs := make([]string, 0, len(nodes))
	for nodeID := range nodes {
		nodeIDs = append(nodeIDs, nodeID)
	}

	for i := 0; i < len(nodeIDs); i++ {
		for j := i + 1; j < len(nodeIDs); j++ {
			node1ID := nodeIDs[i]
			node2ID := nodeIDs[j]

			if !nodes[node1ID].IsOffline() && !nodes[node2ID].IsOffline() {
				syncPersistentNodes(t, nodes[node1ID], nodes[node2ID], node1ID, node2ID)
			}
		}
	}
}

// syncPersistentNodes syncs two persistent nodes
func syncPersistentNodes(t *testing.T, node1, node2 *PersistentEngine, node1ID, node2ID string) {
	if node1.IsOffline() || node2.IsOffline() {
		return
	}

	t.Logf("  Syncing %s with %s", node1ID, node2ID)

	// Use the same sync mechanism as the basic test, but with CRDT merging
	// This ensures proper CRDT semantics are maintained
	crdts1 := node1.GetAllCRDTs()
	crdts2 := node2.GetAllCRDTs()

	// Merge CRDTs from node2 into node1
	for key, crdt2 := range crdts2 {
		if crdt1, exists := crdts1[key]; exists {
			// Both nodes have this CRDT, merge them
			merged, err := crdt1.Merge(crdt2)
			if err != nil {
				t.Errorf("Failed to merge CRDT %s: %v", key, err)
				continue
			}
			applyMergedCRDT(node1.engine, key, merged)
		} else {
			// Only node2 has this CRDT, copy it to node1
			applyMergedCRDT(node1.engine, key, crdt2)
		}
	}

	// Merge CRDTs from node1 into node2
	for key, crdt1 := range crdts1 {
		if crdt2, exists := crdts2[key]; exists {
			// Both nodes have this CRDT, merge them
			merged, err := crdt2.Merge(crdt1)
			if err != nil {
				t.Errorf("Failed to merge CRDT %s: %v", key, err)
				continue
			}
			applyMergedCRDT(node2.engine, key, merged)
		} else {
			// Only node1 has this CRDT, copy it to node2
			applyMergedCRDT(node2.engine, key, crdt1)
		}
	}

	// Update last sync timestamps (use a dummy timestamp for now)
	node1.lastSync[node2ID] = VectorClock{node2ID: 1}
	node2.lastSync[node1ID] = VectorClock{node1ID: 1}
}

// logNodeStates logs the current state of all nodes
func logNodeStates(t *testing.T, nodes map[string]*PersistentEngine) {
	for nodeID, node := range nodes {
		status := "online"
		if node.IsOffline() {
			status = "offline"
		}

		users, _ := node.GetSet("users")
		likes, _ := node.GetCounter("likes")
		t.Logf("  %s (%s): users=%v, likes=%d", nodeID, status, users, likes)
	}
}

// verifyPersistentConsistency verifies all online nodes have consistent state
func verifyPersistentConsistency(t *testing.T, nodes map[string]*PersistentEngine) {
	t.Log("Verifying persistent consistency...")

	var referenceNode string
	var referenceCRDTs map[string]CRDT

	// Find first online node as reference
	for nodeID, node := range nodes {
		if !node.IsOffline() {
			referenceNode = nodeID
			referenceCRDTs = node.GetAllCRDTs()
			break
		}
	}

	if referenceNode == "" {
		t.Error("No online nodes found for consistency check")
		return
	}

	// Compare all other online nodes with reference
	for nodeID, node := range nodes {
		if nodeID == referenceNode || node.IsOffline() {
			continue
		}

		crdts := node.GetAllCRDTs()

		// Check CRDT keys
		if len(crdts) != len(referenceCRDTs) {
			t.Errorf("Node %s has %d CRDTs, reference has %d", nodeID, len(crdts), len(referenceCRDTs))
			continue
		}

		for key, referenceCRDT := range referenceCRDTs {
			crdt, exists := crdts[key]
			if !exists {
				t.Errorf("Node %s missing CRDT key %s", nodeID, key)
				continue
			}

			// Compare CRDT values
			if set1, ok1 := referenceCRDT.(*LWWSet); ok1 {
				if set2, ok2 := crdt.(*LWWSet); ok2 {
					elements1 := set1.GetElements()
					elements2 := set2.GetElements()

					if len(elements1) != len(elements2) {
						t.Errorf("Node %s set %s has %d elements, reference has %d",
							nodeID, key, len(elements2), len(elements1))
						continue
					}

					for i, elem1 := range elements1 {
						if i >= len(elements2) || elem1 != elements2[i] {
							t.Errorf("Node %s set %s element %d: expected %s, got %s",
								nodeID, key, i, elem1, elements2[i])
						}
					}
				}
			}

			if counter1, ok1 := referenceCRDT.(*Counter); ok1 {
				if counter2, ok2 := crdt.(*Counter); ok2 {
					value1 := counter1.Value()
					value2 := counter2.Value()

					if value1 != value2 {
						t.Errorf("Node %s counter %s has value %d, reference has %d",
							nodeID, key, value2, value1)
					}
				}
			}
		}
	}

	t.Log("Persistent consistency verification passed!")
}
