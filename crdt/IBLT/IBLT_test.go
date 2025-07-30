package IBLT

import (
	"fmt"
	"testing"
)

func TestIBLT_InsertAndExists(t *testing.T) {
	ib := New(100, 3)
	key1 := []byte("foo")
	key2 := []byte("bar")
	key3 := []byte("baz")

	ib.Insert(key1)
	ib.Insert(key2)

	if !ib.Exists(key1) {
		t.Errorf("Expected key1 to exist")
	}
	if !ib.Exists(key2) {
		t.Errorf("Expected key2 to exist")
	}
	if ib.Exists(key3) {
		t.Errorf("Did not expect key3 to exist")
	}
}

func TestIBLT_Empty(t *testing.T) {
	ib := New(50, 3)
	key := []byte("notfound")
	if ib.Exists(key) {
		t.Errorf("Empty IBLT should not contain any keys")
	}
}

func TestIBLT_DifferentKeys(t *testing.T) {
	ib := New(100, 3)
	keys := [][]byte{
		[]byte("a"),
		[]byte("b"),
		[]byte("c"),
		[]byte("d"),
		[]byte("e"),
	}
	for _, k := range keys {
		ib.Insert(k)
	}
	for _, k := range keys {
		if !ib.Exists(k) {
			t.Errorf("Expected key %q to exist", k)
		}
	}
	if ib.Exists([]byte("z")) {
		t.Errorf("Did not expect key 'z' to exist")
	}
}

func TestIBLT_PanicOnBadParams(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Expected panic for bad params")
		}
	}()
	_ = New(0, 0)
}

func TestIBLT(t *testing.T) {
    // Configuration must be identical
    m, k := 50, 4

    // --- Node A's data ---
    nodeA := New(m, k)
    nodeA.Insert([]byte("apple"))
    nodeA.Insert([]byte("banana"))
    nodeA.Insert([]byte("cherry"))

    // --- Node B's data (missing 'banana', has extra 'date') ---
    nodeB := New(m, k)
    nodeB.Insert([]byte("apple"))
    nodeB.Insert([]byte("cherry"))
    nodeB.Insert([]byte("date"))

    // Calculate the difference (A - B)
    diff, err := nodeB.Subtract(nodeA)
    if err != nil {
        t.Fatal(err)
    }

    fmt.Printf("Difference IBLT created: %s\n", diff)
	fmt.Println("Difference Exists 'banana'?", diff.Exists([]byte("banana"))) // false
	fmt.Println("Difference Exists 'date'?", diff.Exists([]byte("date"))) // true

    // Calculate the union (A + B)
    union, err := nodeA.Add(nodeB)
    if err != nil {
        t.Fatal(err)
    }

    fmt.Printf("Union IBLT created: %s\n", union)
    fmt.Println("Union Exists 'banana'?", union.Exists([]byte("banana"))) // true
    fmt.Println("Union Exists 'date'?", union.Exists([]byte("date")))     // true
}