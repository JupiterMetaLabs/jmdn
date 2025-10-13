package DataLayer

import (
	"context"
	"fmt"
	"gossipnode/AVC/BuddyNodes/Types"

	"testing"

	"github.com/multiformats/go-multiaddr"
)

// TestExampleUsage demonstrates proper usage of the CRDT layer with multiaddr
func TestExampleUsage(t *testing.T) {
	// Create multiaddr for node identification
	node1Addr, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/15001/p2p/12D3KooWFMi4oXTbyZxnUX3vjc4PD4yMWmULXNsPdsokTGHAWfeT")
	if err != nil {
		fmt.Printf("Error creating multiaddr: %v\n", err)
		t.Fatal(err)
	}

	node2Addr, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/15002/p2p/12D3KooWFMi4oXTbyZxnUX3vjc4PD4yMWmULXNsPdsokTGHAWfe2")
	if err != nil {
		fmt.Printf("Error creating multiaddr: %v\n", err)
		t.Fatal(err)
	}

	// Create CRDT controllers for each node
	node1Controller := NewCRDTLayer(nil)
	node2Controller := NewCRDTLayer(nil)

	// Add data to node1
	fmt.Println("Adding data to node1...")
	err = Add(node1Controller, node1Addr, "users", "alice")
	if err != nil {
		fmt.Printf("Error adding to node1: %v\n", err)
		t.Fatal(err)
	}

	err = Add(node1Controller, node1Addr, "users", "bob")
	if err != nil {
		fmt.Printf("Error adding to node1: %v\n", err)
		t.Fatal(err)
	}

	err = CounterInc(node1Controller, node1Addr, "likes", 5)
	if err != nil {
		fmt.Printf("Error incrementing counter on node1: %v\n", err)
		t.Fatal(err)
	}

	// Add data to node2
	fmt.Println("Adding data to node2...")
	err = Add(node2Controller, node2Addr, "users", "charlie")
	if err != nil {
		fmt.Printf("Error adding to node2: %v\n", err)
		t.Fatal(err)
	}

	err = CounterInc(node2Controller, node2Addr, "likes", 3)
	if err != nil {
		fmt.Printf("Error incrementing counter on node2: %v\n", err)
		t.Fatal(err)
	}

	// Show state before sync
	fmt.Println("\nState before sync:")
	users1, _ := GetSet(node1Controller, "users")
	likes1, _ := GetCounter(node1Controller, "likes")
	fmt.Printf("Node1: users=%v, likes=%d\n", users1, likes1)

	users2, _ := GetSet(node2Controller, "users")
	likes2, _ := GetCounter(node2Controller, "likes")
	fmt.Printf("Node2: users=%v, likes=%d\n", users2, likes2)

	// Sync the nodes
	fmt.Println("\nSyncing nodes...")
	ctx := context.Background()
	err = SyncWithNode(ctx, node1Controller, node2Controller, "node1", "node2")
	if err != nil {
		fmt.Printf("Error syncing nodes: %v\n", err)
		t.Fatal(err)
	}

	// Show state after sync
	fmt.Println("\nState after sync:")
	users1, _ = GetSet(node1Controller, "users")
	likes1, _ = GetCounter(node1Controller, "likes")
	fmt.Printf("Node1: users=%v, likes=%d\n", users1, likes1)

	users2, _ = GetSet(node2Controller, "users")
	likes2, _ = GetCounter(node2Controller, "likes")
	fmt.Printf("Node2: users=%v, likes=%d\n", users2, likes2)

	// Example with multiple nodes
	fmt.Println("\n=== Multi-Node Example ===")
	nodes := map[string]*Types.Controller{
		"node1": node1Controller,
		"node2": node2Controller,
	}

	// Add a third node
	node3Controller := NewCRDTLayer(nil)
	node3Addr, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/15003/p2p/12D3KooWFMi4oXTbyZxnUX3vjc4PD4yMWmULXNsPdsokTGHAWfe3")
	if err != nil {
		fmt.Printf("Error creating node3 multiaddr: %v\n", err)
		t.Fatal(err)
	}

	Add(node3Controller, node3Addr, "users", "diana")
	CounterInc(node3Controller, node3Addr, "likes", 8)

	nodes["node3"] = node3Controller

	// Sync all nodes
	fmt.Println("Syncing all nodes...")
	err = SyncAllNodes(ctx, nodes)
	if err != nil {
		fmt.Printf("Error syncing all nodes: %v\n", err)
		t.Fatal(err)
	}

	// Show final state
	fmt.Println("\nFinal state after multi-node sync:")
	for nodeID, controller := range nodes {
		users, _ := GetSet(controller, "users")
		likes, _ := GetCounter(controller, "likes")
		fmt.Printf("%s: users=%v, likes=%d\n", nodeID, users, likes)
	}

	fmt.Println("\nExample completed successfully!")
}
