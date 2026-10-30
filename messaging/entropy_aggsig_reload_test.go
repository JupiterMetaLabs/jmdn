package messaging

// The sequencer held the parent's certificate only in memory, so after a
// restart (or a sequencer handover) every fold-window block it proposed
// carried none. Five missing certificates in a window leave the epoch with no
// fallback seed, no VDF input and no boundary block: the chain halts. These
// tests pin the reload from the parent's persisted committee certificate.

import (
	"encoding/json"
	"errors"
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// Epoch 2's collection range is [103, 110), so a block at slot 104 carries
// its parent 103's certificate.
const (
	reloadSlot       = 104
	reloadPrevHeight = 103
)

func withAggCert(t *testing.T) {
	t.Helper()
	orig := AggCertEnabled
	AggCertEnabled = true
	t.Cleanup(func() { AggCertEnabled = orig })
}

func withCertInMemory(t *testing.T, height uint64, responses []BLS_Signer.BLSresponse) {
	t.Helper()
	certForNextBlockMu.Lock()
	savedCert, savedHeight := certForNextBlock, certForNextHeight
	certForNextBlockMu.Unlock()
	t.Cleanup(func() {
		certForNextBlockMu.Lock()
		certForNextBlock, certForNextHeight = savedCert, savedHeight
		certForNextBlockMu.Unlock()
	})
	certForNextBlockMu.Lock()
	certForNextBlock, certForNextHeight = nil, 0
	certForNextBlockMu.Unlock()
	if responses != nil {
		RecordCommitCertificate(height, responses)
	}
}

func withPersistedCert(t *testing.T, fn func(height uint64) (string, error)) {
	t.Helper()
	orig := persistedCommitCertificateFn
	persistedCommitCertificateFn = fn
	t.Cleanup(func() { persistedCommitCertificateFn = orig })
}

func reloadResponses() []BLS_Signer.BLSresponse {
	return []BLS_Signer.BLSresponse{
		{PeerID: "peer-c", PubKey: "cc", Signature: "c1", Agree: true},
		{PeerID: "peer-a", PubKey: "aa", Signature: "a1", Agree: true},
		{PeerID: "peer-x", PubKey: "xx", Signature: "x1", Agree: false},
		{PeerID: "peer-b", PubKey: "bb", Signature: "b1", Agree: true},
	}
}

func persistedJSON(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(reloadResponses())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCertificateForBlockAssembly_ReloadsPersistedCertAfterRestart(t *testing.T) {
	withAggCert(t)
	withCertInMemory(t, 0, nil) // a restarted sequencer holds nothing
	withPersistedCert(t, func(height uint64) (string, error) {
		if height != reloadPrevHeight {
			t.Fatalf("reloaded height %d, want the parent %d", height, reloadPrevHeight)
		}
		return persistedJSON(t), nil
	})

	got := CertificateForBlockAssembly(reloadSlot, reloadPrevHeight)
	if len(got) != 3 {
		t.Fatalf("got %d signers, want the parent's 3 YES votes from its persisted certificate: %+v", len(got), got)
	}
	for i, want := range []string{"peer-a", "peer-b", "peer-c"} {
		if got[i].PeerID != want {
			t.Fatalf("signer %d = %s, want %s (YES votes only, sorted by peer ID): %+v", i, got[i].PeerID, want, got)
		}
	}
}

// The reload must build exactly what the live path would have attached.
func TestCertificateForBlockAssembly_ReloadMatchesLiveCertificate(t *testing.T) {
	withAggCert(t)
	withCertInMemory(t, reloadPrevHeight, reloadResponses())
	live := CertificateForBlockAssembly(reloadSlot, reloadPrevHeight)

	withCertInMemory(t, 0, nil)
	withPersistedCert(t, func(uint64) (string, error) { return persistedJSON(t), nil })
	reloaded := CertificateForBlockAssembly(reloadSlot, reloadPrevHeight)

	if len(live) == 0 || len(live) != len(reloaded) {
		t.Fatalf("live %d signers, reloaded %d", len(live), len(reloaded))
	}
	for i := range live {
		if live[i] != reloaded[i] {
			t.Fatalf("signer %d differs: live %+v, reloaded %+v", i, live[i], reloaded[i])
		}
	}
}

func TestCertificateForBlockAssembly_PrefersInMemoryCert(t *testing.T) {
	withAggCert(t)
	withCertInMemory(t, reloadPrevHeight, reloadResponses())
	withPersistedCert(t, func(uint64) (string, error) {
		t.Fatal("the persisted certificate must not be read while memory holds the parent's")
		return "", nil
	})
	if got := CertificateForBlockAssembly(reloadSlot, reloadPrevHeight); len(got) != 3 {
		t.Fatalf("got %d signers, want 3", len(got))
	}
}

// Memory holding an older height (a sequencer handover) is as good as empty.
func TestCertificateForBlockAssembly_ReloadsWhenMemoryIsForAnotherHeight(t *testing.T) {
	withAggCert(t)
	withCertInMemory(t, reloadPrevHeight-5, reloadResponses())
	withPersistedCert(t, func(uint64) (string, error) { return persistedJSON(t), nil })
	if got := CertificateForBlockAssembly(reloadSlot, reloadPrevHeight); len(got) != 3 {
		t.Fatalf("got %d signers, want the parent's 3 from its persisted certificate", len(got))
	}
}

func TestCertificateForBlockAssembly_NoReloadOutsideWindow(t *testing.T) {
	withAggCert(t)
	withCertInMemory(t, 0, nil)
	withPersistedCert(t, func(uint64) (string, error) {
		t.Fatal("a block outside the fold window must not read the DB")
		return "", nil
	})
	if got := CertificateForBlockAssembly(reloadSlot+20, reloadPrevHeight+20); got != nil {
		t.Fatalf("got %+v outside the window, want nil", got)
	}
}

func TestCertificateForBlockAssembly_ReloadFailuresAttachNothing(t *testing.T) {
	withAggCert(t)
	withCertInMemory(t, 0, nil)
	cases := map[string]func(uint64) (string, error){
		"read error":    func(uint64) (string, error) { return "", errors.New("db down") },
		"not persisted": func(uint64) (string, error) { return "", nil },
		"malformed":     func(uint64) (string, error) { return "{not json", nil },
		"only NO votes": func(uint64) (string, error) {
			return `[{"PeerID":"p","PubKey":"pp","Signature":"s","Agree":false}]`, nil
		},
		"empty certificate": func(uint64) (string, error) { return "[]", nil },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			withPersistedCert(t, fn)
			if got := CertificateForBlockAssembly(reloadSlot, reloadPrevHeight); got != nil {
				t.Fatalf("got %+v, want nil", got)
			}
		})
	}
}
