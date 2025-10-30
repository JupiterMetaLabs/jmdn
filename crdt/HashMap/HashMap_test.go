package HashMap

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func generateRandomDIDs(n int) []string {
	schemes := []string{"did:zkm:mainnet:", "did:zkm:superj:", "did:zkm:metamask:"}
	letters := []rune("abcdef0123456789")
	dids := make([]string, n)
	rand.Seed(time.Now().UTC().UnixNano())

	for i := 0; i < n; i++ {
		prefix := schemes[rand.Intn(len(schemes))]
		var b strings.Builder
		for j := 0; j < 32; j++ {
			b.WriteRune(letters[rand.Intn(len(letters))])
		}
		dids[i] = prefix + b.String()
	}
	return dids
}

func TestHashMapOperations(t *testing.T) {
	total := 50000
	common := 35000
	a := New()
	b := New()
	allDIDs := generateRandomDIDs(total)

	// Insert into A (all 50000)
	for _, did := range allDIDs {
		a.Insert(did)
	}
	if a.Size() != total {
		t.Fatalf("expected HashMap A to have %d keys, got %d", total, a.Size())
	}

	// Insert into B (first 35000)
	for _, did := range allDIDs[:common] {
		b.Insert(did)
	}
	if b.Size() != common {
		t.Fatalf("expected HashMap B to have %d keys, got %d", common, b.Size())
	}

	// Subtract B from A → expect 15000
	diff := a.Subtract(b)
	if len(diff) != total-common {
		t.Fatalf("expected %d diffs, got %d", total-common, len(diff))
	}

	// Test Exists and Delete
	testKey := allDIDs[0]
	if !a.Exists(testKey) {
		t.Errorf("expected key %s to exist", testKey)
	}
	a.Delete(testKey)
	if a.Exists(testKey) {
		t.Errorf("expected key %s to be deleted", testKey)
	}

	// Test Fingerprint is deterministic
	fp1 := b.Fingerprint()
	fp2 := b.Fingerprint()
	if fp1 != fp2 {
		t.Errorf("expected deterministic fingerprint, got %s and %s", fp1, fp2)
	}

	// Test Union
	union := a.Union(b)
	if union.Size() < b.Size() {
		t.Errorf("union should contain at least all B keys")
	}

	// Test JSON output
	jsonOut := b.ToJSON()
	fmt.Println("JSON output: ", string(jsonOut))
	if len(jsonOut) == 0 {
		t.Errorf("expected non-empty JSON output")
	}
}
