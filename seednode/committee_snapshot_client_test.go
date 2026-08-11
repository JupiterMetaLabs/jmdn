package seednode

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	blssign "gossipnode/AVC/BLS/bls-sign"
	"gossipnode/seednode/committee"
)

const testEpochSeconds int64 = 3600

func curEpoch() uint64 { return uint64(time.Now().Unix() / testEpochSeconds) }

// makeAuthority derives an ephemeral dela/bls authority keypair (no disk global)
// and returns priv bytes + lowercase pubkey hex. GenerateBLSKeyPairFromRawPrivKey
// hashes the input to a BN256 scalar and rejects out-of-range values, so retry
// deterministic seed variants until one lands in range.
func makeAuthority(t *testing.T, seed string) (priv []byte, pubHex string) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		p, pub, err := blssign.GenerateBLSKeyPairFromRawPrivKey([]byte(fmt.Sprintf("%s-%d", seed, i)))
		if err == nil {
			return p, hex.EncodeToString(pub)
		}
	}
	t.Fatalf("could not derive a valid authority key for seed %q", seed)
	return nil, ""
}

// signSnapshot builds a snapshot signed by priv over the canonical bytes, so
// committee.VerifyCommitteeSnapshot accepts it against pubHex.
func signSnapshot(t *testing.T, priv []byte, pubHex string, epoch uint64, entries []committee.CommitteeEntry) *committee.CommitteeSnapshot {
	t.Helper()
	sig, err := blssign.BLSSign(priv, committee.CanonicalCommitteeBytes(epoch, "", entries))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return &committee.CommitteeSnapshot{
		Epoch:           epoch,
		Seed:            "",
		Entries:         entries,
		AuthorityPubHex: pubHex,
		Signature:       hex.EncodeToString(sig),
	}
}

func twoEntries() []committee.CommitteeEntry {
	return []committee.CommitteeEntry{{PeerID: "QmA", BLSPub: "aa"}, {PeerID: "QmB", BLSPub: "bb"}}
}

// TOFU first use: adopts the seed's authority key, persists it, returns members.
func TestCommitteeSourceAuto_TOFUAdoptsAndPersists(t *testing.T) {
	priv, pubHex := makeAuthority(t, "authority-A")
	snap := signSnapshot(t, priv, pubHex, curEpoch(), twoEntries())

	pinFile := filepath.Join(t.TempDir(), "seedAuth.json")
	s := &committeeSource{
		fetch:        func(context.Context, uint64) (*committee.CommitteeSnapshot, error) { return snap, nil },
		epochSeconds: testEpochSeconds,
		pinFile:      pinFile,
		seedURL:      "test-seed",
		ttl:          time.Minute,
	}

	m, err := s.eligible(0, false)
	if err != nil {
		t.Fatalf("eligible: %v", err)
	}
	if m["QmA"] != "aa" || m["QmB"] != "bb" {
		t.Fatalf("unexpected members: %v", m)
	}
	rec, err := loadPersistedAuthority(pinFile)
	if err != nil {
		t.Fatalf("pin file not written: %v", err)
	}
	if normAuthorityHex(rec.AuthorityPubHex) != pubHex {
		t.Fatalf("persisted key %s != adopted %s", rec.AuthorityPubHex, pubHex)
	}
}

// A later key-swap is NOT honored: after adopting A, a snapshot signed by a
// different key B is rejected and the node keeps serving the last-good A
// committee (B's members never appear).
func TestCommitteeSourceAuto_KeySwapNotHonored(t *testing.T) {
	privA, pubA := makeAuthority(t, "authority-A")
	privB, pubB := makeAuthority(t, "authority-B")
	if pubA == pubB {
		t.Fatal("test keys collided")
	}
	snapA := signSnapshot(t, privA, pubA, curEpoch(), twoEntries())
	otherSet := []committee.CommitteeEntry{{PeerID: "QmOther", BLSPub: "ee"}}
	snapB := signSnapshot(t, privB, pubB, curEpoch(), otherSet)

	cur := snapA
	s := &committeeSource{
		fetch:        func(context.Context, uint64) (*committee.CommitteeSnapshot, error) { return cur, nil },
		epochSeconds: testEpochSeconds,
		pinFile:      filepath.Join(t.TempDir(), "seedAuth.json"),
		ttl:          time.Minute,
	}

	if _, err := s.eligible(0, false); err != nil { // adopt A
		t.Fatalf("adopt A: %v", err)
	}
	cur = snapB              // rogue seed swaps the authority key
	s.cachedAt = time.Time{} // force a refetch past the TTL

	m, err := s.eligible(0, false)
	if err != nil {
		t.Fatalf("expected last-good serve, got error: %v", err)
	}
	if _, bad := m["QmOther"]; bad {
		t.Fatal("key-swapped committee must NOT be honored")
	}
	if m["QmA"] != "aa" {
		t.Fatalf("expected last-good A committee, got: %v", m)
	}
}

// With no cache to bridge, a snapshot not signed by the adopted key fails closed.
func TestCommitteeSourceAuto_RejectFailClosedNoCache(t *testing.T) {
	privA, pubA := makeAuthority(t, "authority-A")
	privB, pubB := makeAuthority(t, "authority-B")
	snapB := signSnapshot(t, privB, pubB, curEpoch(), twoEntries())

	// Pre-populate the pin file with A so resolveAuthority loads A (no first-use).
	pinFile := filepath.Join(t.TempDir(), "seedAuth.json")
	if err := savePersistedAuthority(pinFile, persistedAuthority{AuthorityPubHex: pubA}); err != nil {
		t.Fatalf("seed pin: %v", err)
	}
	_ = privA

	s := &committeeSource{
		fetch:        func(context.Context, uint64) (*committee.CommitteeSnapshot, error) { return snapB, nil },
		epochSeconds: testEpochSeconds,
		pinFile:      pinFile,
		ttl:          time.Minute,
	}
	if _, err := s.eligible(0, false); err == nil {
		t.Fatal("expected fail-closed rejection for wrong-authority snapshot with no cache")
	}
}

// A configured pin overrides TOFU: a snapshot signed by a different key is
// rejected, and no TOFU file is written.
func TestCommitteeSourceAuto_ConfigPinOverridesTOFU(t *testing.T) {
	_, pubA := makeAuthority(t, "authority-A")
	privB, pubB := makeAuthority(t, "authority-B")
	snapB := signSnapshot(t, privB, pubB, curEpoch(), twoEntries())

	pinFile := filepath.Join(t.TempDir(), "seedAuth.json")
	s := &committeeSource{
		fetch:        func(context.Context, uint64) (*committee.CommitteeSnapshot, error) { return snapB, nil },
		configPin:    pubA, // operator pin
		epochSeconds: testEpochSeconds,
		pinFile:      pinFile,
		ttl:          time.Minute,
	}
	if _, err := s.eligible(0, false); err == nil {
		t.Fatal("pinned source must reject a snapshot not signed by the pin")
	}
	if _, err := os.Stat(pinFile); err == nil {
		t.Fatal("TOFU file must not be written when a config pin is set")
	}
}

// The verified snapshot is cached: within the TTL the seed is not re-queried.
func TestCommitteeSourceAuto_CachesWithinTTL(t *testing.T) {
	priv, pubHex := makeAuthority(t, "authority-A")
	snap := signSnapshot(t, priv, pubHex, curEpoch(), twoEntries())

	calls := 0
	s := &committeeSource{
		fetch: func(context.Context, uint64) (*committee.CommitteeSnapshot, error) {
			calls++
			return snap, nil
		},
		configPin:    pubHex, // pin set → resolveAuthority does not fetch
		epochSeconds: testEpochSeconds,
		ttl:          time.Minute,
	}
	for i := 0; i < 3; i++ {
		if _, err := s.eligible(0, false); err != nil {
			t.Fatalf("eligible #%d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 seed fetch across 3 cached calls, got %d", calls)
	}
}

// Fail-closed once the last-good snapshot itself goes stale.
func TestCommitteeSourceAuto_StaleCacheFailsClosed(t *testing.T) {
	priv, pubHex := makeAuthority(t, "authority-A")
	fresh := signSnapshot(t, priv, pubHex, curEpoch(), twoEntries())

	fail := false
	s := &committeeSource{
		fetch: func(context.Context, uint64) (*committee.CommitteeSnapshot, error) {
			if fail {
				return nil, context.DeadlineExceeded
			}
			return fresh, nil
		},
		configPin:    pubHex,
		epochSeconds: testEpochSeconds,
		ttl:          time.Minute,
	}
	if _, err := s.eligible(0, false); err != nil { // prime the cache
		t.Fatalf("prime: %v", err)
	}
	// Seed goes down AND the cached snapshot is now stale (old epoch).
	fail = true
	s.cachedAt = time.Time{}
	s.cachedSnap.Epoch = curEpoch() - uint64(committee.EpochFreshnessWindow) - 5

	if _, err := s.eligible(0, false); err == nil {
		t.Fatal("expected fail-closed when seed is down and cache is stale")
	}
}
