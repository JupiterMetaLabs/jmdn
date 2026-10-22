package messaging

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	blssign "gossipnode/AVC/BLS/bls-sign"
	"gossipnode/internal/ckptfloor"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/checkpoint"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/core"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
)

func newCkptStore(t *testing.T) kv.Store {
	t.Helper()
	s, err := kv.NewStore(kv.Config{Path: t.TempDir(), NoSyncWrites: true})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// appendRecords appends n records and returns the last sequence.
func appendRecords(t *testing.T, s kv.Store, n int) uint64 {
	t.Helper()
	var last uint64
	for i := 0; i < n; i++ {
		seq, err := s.Append(&core.CanonicalRecord{Namespace: "t", Type: "t", Value: []byte{byte(i)}, Timestamp: uint64(i)})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		last = seq
	}
	return last
}

func checkpointAt(t *testing.T, s kv.Store, seq uint64) *checkpoint.Checkpoint {
	t.Helper()
	head, err := checkpoint.RecomputeHead(context.Background(), s, 0, seq, make([]byte, 32))
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	return &checkpoint.Checkpoint{ChainID: 7, Seq: seq, Head: head, Signature: []byte("sig")}
}

func acceptAll(_, _ []byte, _ uint64) error { return nil }

// A checkpoint the node cannot verify must not be stored: the next boot would
// find it, fail, and refuse to start.
func TestStoreVerifiedCheckpointRefusesUnverifiable(t *testing.T) {
	s := newCkptStore(t)
	seq := appendRecords(t, s, 3)
	floor := filepath.Join(t.TempDir(), "floor.json")
	reject := func(_, _ []byte, _ uint64) error { return errors.New("not in committee") }

	if err := storeVerifiedCheckpoint(s, checkpointAt(t, s, seq), reject, floor); err == nil {
		t.Fatal("expected an error")
	}
	if _, ok, _ := checkpoint.Latest(s); ok {
		t.Fatal("unverifiable checkpoint was stored")
	}
	if _, ok, _ := ckptfloor.Load(floor); ok {
		t.Fatal("floor raised for an unverifiable checkpoint")
	}
}

func TestStoreVerifiedCheckpointRaisesFloor(t *testing.T) {
	s := newCkptStore(t)
	seq := appendRecords(t, s, 3)
	floor := filepath.Join(t.TempDir(), "floor.json")
	if err := storeVerifiedCheckpoint(s, checkpointAt(t, s, seq), acceptAll, floor); err != nil {
		t.Fatal(err)
	}
	f, ok, err := ckptfloor.Load(floor)
	if err != nil || !ok || f.Seq != seq || f.ChainID != 7 {
		t.Fatalf("floor = %+v ok=%v err=%v", f, ok, err)
	}
}

func TestBootGateNoCheckpointNoFloorPasses(t *testing.T) {
	s := newCkptStore(t)
	appendRecords(t, s, 2)
	ok, err := CheckpointBootGate(context.Background(), s, acceptAll, filepath.Join(t.TempDir(), "floor.json"), 0, 0, 0)
	if err != nil || ok {
		t.Fatalf("got ok=%v err=%v, want false,nil", ok, err)
	}
}

func TestBootGateVerifiesAndRaisesFloor(t *testing.T) {
	s := newCkptStore(t)
	seq := appendRecords(t, s, 4)
	if err := checkpoint.Store(s, checkpointAt(t, s, seq)); err != nil {
		t.Fatal(err)
	}
	floor := filepath.Join(t.TempDir(), "floor.json")
	ok, err := CheckpointBootGate(context.Background(), s, acceptAll, floor, 0, 0, 0)
	if err != nil || !ok {
		t.Fatalf("got ok=%v err=%v", ok, err)
	}
	if f, has, _ := ckptfloor.Load(floor); !has || f.Seq != seq {
		t.Fatalf("floor not raised to %d: %+v", seq, f)
	}
}

// The store's checkpoints are gone (keys deleted, or a store that never had
// them swapped in) while the floor file remembers one: boot must fail.
func TestBootGateDeletedCheckpointsFail(t *testing.T) {
	signed := newCkptStore(t)
	seq := appendRecords(t, signed, 4)
	floor := filepath.Join(t.TempDir(), "floor.json")
	if err := storeVerifiedCheckpoint(signed, checkpointAt(t, signed, seq), acceptAll, floor); err != nil {
		t.Fatal(err)
	}

	stripped := newCkptStore(t) // same records, no checkpoints
	appendRecords(t, stripped, 4)
	if ok, err := CheckpointBootGate(context.Background(), stripped, acceptAll, floor, 0, 0, 0); err == nil || ok {
		t.Fatalf("expected failure, got ok=%v err=%v", ok, err)
	}
}

// A store restored to an older state verifies on its own; only the floor file
// (kept outside it) shows that a newer checkpoint existed.
func TestBootGateRollbackFails(t *testing.T) {
	old := newCkptStore(t)
	oldSeq := appendRecords(t, old, 3)
	if err := checkpoint.Store(old, checkpointAt(t, old, oldSeq)); err != nil {
		t.Fatal(err)
	}
	floor := filepath.Join(t.TempDir(), "floor.json")
	// The node had later verified a checkpoint further along.
	if err := ckptfloor.Raise(floor, 7, oldSeq+5, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if ok, err := CheckpointBootGate(context.Background(), old, acceptAll, floor, 0, 0, 0); err == nil || ok {
		t.Fatalf("expected rollback to fail, got ok=%v err=%v", ok, err)
	}
}

func TestBootGateMinSeqWithoutCheckpointFails(t *testing.T) {
	s := newCkptStore(t)
	appendRecords(t, s, 2)
	_, err := CheckpointBootGate(context.Background(), s, acceptAll, "", 1, 0, 0)
	if !errors.Is(err, checkpoint.ErrNoCheckpoint) {
		t.Fatalf("expected ErrNoCheckpoint, got %v", err)
	}
}

// An unavailable committee source is retried before the result is returned.
func TestBootGateRetriesUnavailableCommittee(t *testing.T) {
	s := newCkptStore(t)
	seq := appendRecords(t, s, 2)
	if err := checkpoint.Store(s, checkpointAt(t, s, seq)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	flaky := func(_, _ []byte, _ uint64) error {
		calls++
		if calls < 3 {
			return fmt.Errorf("%w: seed down", ErrCheckpointCommitteeUnavailable)
		}
		return nil
	}
	ok, err := CheckpointBootGate(context.Background(), s, flaky, "", 0, 5, time.Millisecond)
	if err != nil || !ok || calls != 3 {
		t.Fatalf("got ok=%v err=%v calls=%d", ok, err, calls)
	}
	calls = 0
	if _, err := CheckpointBootGate(context.Background(), s, flaky, "", 0, 1, time.Millisecond); !errors.Is(err, ErrCheckpointCommitteeUnavailable) {
		t.Fatalf("expected unavailable after retries ran out, got %v", err)
	}
}

// signedCheckpointAt is checkpointAt with a real BLS signature from priv.
func signedCheckpointAt(t *testing.T, s kv.Store, seq uint64, priv []byte) *checkpoint.Checkpoint {
	t.Helper()
	c := checkpointAt(t, s, seq)
	sig, err := blssign.BLSSign(priv, checkpoint.CanonicalCheckpointBytes(c))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	c.Signature = sig
	return c
}

func testBLSKey(t *testing.T, seed string) (priv []byte, pubHex string) {
	t.Helper()
	// The helper maps sha256(seed) straight to a scalar, which is out of range
	// for some seeds; step the seed until one is valid (deterministic).
	for i := 0; i < 16; i++ {
		priv, pub, err := blssign.GenerateBLSKeyPairFromRawPrivKey([]byte(fmt.Sprintf("%s/%d", seed, i)))
		if err == nil {
			return priv, hex.EncodeToString(pub)
		}
	}
	t.Fatalf("keygen: no valid key for seed %q", seed)
	return nil, ""
}

// The sequencer signs while its key K is in the committee; later K drops out
// of the eligible set (pool change, cap, blocklist). Checked against the
// committee, its own genuine checkpoint now fails at boot — the restart trap.
// Checked against the pinned key, committee changes do not matter.
func TestCheckpointCommitteeRotationVsPinnedKey(t *testing.T) {
	seqPriv, seqPub := testBLSKey(t, "sequencer-key")
	_, otherPub := testBLSKey(t, "validator-key")

	SetCommitteeEligibilitySource(func(_ uint64, _ bool) (map[string]string, error) {
		return map[string]string{"peerSeq": seqPub, "peerVal": otherPub}, nil
	})
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })

	s := newCkptStore(t)
	seq := appendRecords(t, s, 3)
	if err := storeVerifiedCheckpoint(s, signedCheckpointAt(t, s, seq, seqPriv), CheckpointSigVerifierFor(""), ""); err != nil {
		t.Fatalf("store while in committee: %v", err)
	}

	// K leaves the eligible set.
	SetCommitteeEligibilitySource(func(_ uint64, _ bool) (map[string]string, error) {
		return map[string]string{"peerVal": otherPub}, nil
	})

	_, err := CheckpointBootGate(context.Background(), s, CheckpointSigVerifierFor(""), "", 0, 0, 0)
	if !errors.Is(err, ErrCheckpointSignerNotInCommittee) {
		t.Fatalf("committee verifier after rotation: want ErrCheckpointSignerNotInCommittee, got %v", err)
	}

	ok, err := CheckpointBootGate(context.Background(), s, CheckpointSigVerifierFor(seqPub), "", 0, 0, 0)
	if err != nil || !ok {
		t.Fatalf("pinned verifier after rotation: want pass, got ok=%v err=%v", ok, err)
	}
}

// A checkpoint signed by any other key fails against the pin.
func TestPinnedVerifierRejectsOtherSigner(t *testing.T) {
	_, seqPub := testBLSKey(t, "sequencer-key")
	otherPriv, _ := testBLSKey(t, "attacker-key")
	s := newCkptStore(t)
	seq := appendRecords(t, s, 2)
	c := signedCheckpointAt(t, s, seq, otherPriv)
	err := CheckpointSigVerifierFor(seqPub)(checkpoint.CanonicalCheckpointBytes(c), c.Signature, c.Epoch)
	if !errors.Is(err, ErrCheckpointBadSignature) {
		t.Fatalf("want ErrCheckpointBadSignature, got %v", err)
	}
	// And storeVerifiedCheckpoint refuses it (a sequencer whose own key is not
	// the pinned one never stores a checkpoint).
	if err := storeVerifiedCheckpoint(s, c, CheckpointSigVerifierFor(seqPub), ""); err == nil {
		t.Fatal("stored a checkpoint not signed by the pinned key")
	}
}

// With no pin configured the node's own key file is the signer, so a fresh
// sequencer needs no operator step; a configured pin still wins.
func TestResolveCheckpointSignerUsesNodeKey(t *testing.T) {
	t.Setenv("JMDN_BLS_KEY_FILE", filepath.Join(t.TempDir(), "bls.json"))

	if pub, src := ResolveCheckpointSigner(""); pub != "" || src != "none" {
		t.Fatalf("no key file: got %q/%q, want empty/none", pub, src)
	}
	priv, pubBytes, err := blssign.GenerateBLSKeyPair() // writes JMDN_BLS_KEY_FILE
	if err != nil {
		t.Fatal(err)
	}
	pub, src := ResolveCheckpointSigner("")
	if src != "node_key" || pub != hex.EncodeToString(pubBytes) {
		t.Fatalf("node key: got %q/%q", pub, src)
	}
	if p, s := ResolveCheckpointSigner("0xABCD"); p != "abcd" || s != "config" {
		t.Fatalf("config pin: got %q/%q", p, s)
	}

	// The resolved key verifies the node's own checkpoint even when the key is
	// not in the committee at all.
	SetCommitteeEligibilitySource(func(_ uint64, _ bool) (map[string]string, error) {
		return map[string]string{"someoneElse": "00"}, nil
	})
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })
	s := newCkptStore(t)
	seq := appendRecords(t, s, 2)
	if err := storeVerifiedCheckpoint(s, signedCheckpointAt(t, s, seq, priv), CheckpointSigVerifierFor(pub), ""); err != nil {
		t.Fatalf("store: %v", err)
	}
	if ok, err := CheckpointBootGate(context.Background(), s, CheckpointSigVerifierFor(pub), "", 0, 0, 0); err != nil || !ok {
		t.Fatalf("boot gate: ok=%v err=%v", ok, err)
	}
}
