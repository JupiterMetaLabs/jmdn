package IBLT

import (
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
