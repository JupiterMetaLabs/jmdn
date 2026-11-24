package committee

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	blssign "gossipnode/AVC/BLS/bls-sign"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// These tests pin the canonical byte formats and the sign/verify round-trips so
// the jmdn mirror stays byte-for-byte with seedNodes. Golden strings below are
// the exact contract text; if a seed change alters them, these fail loudly.

func TestPoPChallenge_Golden(t *testing.T) {
	got := string(PoPChallenge("QmPeer", "AABB"))
	want := "jmdt/bls-pop/v1|QmPeer|aabb" // bls_pub lowercased
	if got != want {
		t.Fatalf("PoPChallenge=%q want %q", got, want)
	}
}

func TestBLSProofOfPossession_RoundTrip(t *testing.T) {
	priv, pub, err := blssign.GenerateBLSKeyPair()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pubHex, popHex, err := ProveBLSPossession("QmPeerA", priv, pub)
	if err != nil {
		t.Fatalf("prove: %v", err)
	}
	if pubHex != strings.ToLower(pubHex) {
		t.Fatalf("bls_pub not lowercase")
	}
	if err := VerifyBLSProofOfPossession("QmPeerA", pubHex, popHex); err != nil {
		t.Fatalf("PoP should verify: %v", err)
	}
	// Wrong peer id -> different challenge -> reject.
	if err := VerifyBLSProofOfPossession("QmPeerB", pubHex, popHex); err == nil {
		t.Fatal("PoP must not verify for a different peer id")
	}
	// Empty inputs fail closed.
	if err := VerifyBLSProofOfPossession("QmPeerA", "", popHex); err == nil {
		t.Fatal("empty bls_pub must fail closed")
	}
	if err := VerifyBLSProofOfPossession("QmPeerA", pubHex, ""); err == nil {
		t.Fatal("empty bls_pop must fail closed")
	}
}

func TestCanonicalCommitteeBytes_GoldenAndSorting(t *testing.T) {
	// Provided out of order + mixed case; must sort by peer_id and lowercase.
	entries := []CommitteeEntry{
		{PeerID: "QmB", BLSPub: "CCDD"},
		{PeerID: "QmA", BLSPub: "AaBb"},
	}
	got := string(CanonicalCommitteeBytes(7, "beacon", entries))
	// v2: three colon-separated fields ALWAYS; an unset reward_address is a
	// trailing colon (never dropped). Must match seedNodes byte-for-byte.
	want := "jmdt/committee/v2|7|beacon|QmA:aabb:,QmB:ccdd:"
	if got != want {
		t.Fatalf("canonical=%q want %q", got, want)
	}
	// Empty seed still yields the two-pipe framing.
	got2 := string(CanonicalCommitteeBytes(0, "", []CommitteeEntry{{PeerID: "QmA", BLSPub: "aa"}}))
	if got2 != "jmdt/committee/v2|0||QmA:aa:" {
		t.Fatalf("canonical(empty seed)=%q", got2)
	}
}

func TestVerifyCommitteeSnapshot_PinAndTamper(t *testing.T) {
	authPriv, authPub, err := blssign.GenerateBLSKeyPair()
	if err != nil {
		t.Fatalf("authority keygen: %v", err)
	}
	authHex := strings.ToLower(hex.EncodeToString(authPub))
	entries := []CommitteeEntry{{PeerID: "QmA", BLSPub: "aa"}, {PeerID: "QmB", BLSPub: "bb"}}
	sig, err := blssign.BLSSign(authPriv, CanonicalCommitteeBytes(3, "", entries))
	if err != nil {
		t.Fatalf("authority sign: %v", err)
	}
	snap := &CommitteeSnapshot{
		Epoch: 3, Entries: entries,
		AuthorityPubHex: authHex, Signature: hex.EncodeToString(sig),
	}

	if err := VerifyCommitteeSnapshot(snap, authHex); err != nil {
		t.Fatalf("valid snapshot should verify against pinned authority: %v", err)
	}
	// Pin mismatch -> reject.
	if err := VerifyCommitteeSnapshot(snap, "deadbeef"); err == nil {
		t.Fatal("snapshot signed by non-pinned authority must be rejected")
	}
	// Tampered entry -> signature no longer matches canonical bytes.
	bad := *snap
	bad.Entries = []CommitteeEntry{{PeerID: "QmA", BLSPub: "aa"}, {PeerID: "QmB", BLSPub: "cc"}}
	if err := VerifyCommitteeSnapshot(&bad, authHex); err == nil {
		t.Fatal("tampered entry must invalidate the signature")
	}
	// Empty set fails closed.
	empty := *snap
	empty.Entries = nil
	if err := VerifyCommitteeSnapshot(&empty, authHex); err == nil {
		t.Fatal("empty snapshot must fail closed")
	}
}

func TestSequencerAuthChallenge_GoldenAndVerify(t *testing.T) {
	got := string(SequencerAuthChallenge("ListBuddy", "QmSeq", 1700000000))
	want := "jmdt/seed-auth/v1|ListBuddy|QmSeq|1700000000"
	if got != want {
		t.Fatalf("seq challenge=%q want %q", got, want)
	}

	priv, _, err := ic.GenerateKeyPair(ic.Ed25519, 0)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	now := time.Unix(1700000123, 0)
	ts, sigHex, err := SignSequencerRequest(priv, "ListBuddy", now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if ts != 1700000123 {
		t.Fatalf("ts=%d", ts)
	}
	pid, _ := peer.IDFromPublicKey(priv.GetPublic())
	sig, _ := hex.DecodeString(sigHex)
	ok, err := priv.GetPublic().Verify(SequencerAuthChallenge("ListBuddy", pid.String(), ts), sig)
	if err != nil || !ok {
		t.Fatalf("sequencer signature should verify: ok=%v err=%v", ok, err)
	}
	// Wrong method -> different challenge -> must not verify.
	ok, _ = priv.GetPublic().Verify(SequencerAuthChallenge("GetSnapshot", pid.String(), ts), sig)
	if ok {
		t.Fatal("signature must not verify for a different method")
	}
}

func TestEpochForTime(t *testing.T) {
	if EpochForTime(7200, 3600) != 2 {
		t.Fatal("7200/3600 = 2")
	}
	if EpochForTime(7199, 3600) != 1 {
		t.Fatal("7199/3600 = 1")
	}
	if EpochForTime(100, 0) != 0 { // 0 -> default 3600; 100/3600 = 0
		t.Fatal("default epoch seconds")
	}
}

// A validly-signed but STALE snapshot must be rejected on freshness.
func TestCheckSnapshotEpochFresh(t *testing.T) {
	const es = int64(3600)
	now := int64(6 * 3600) // current epoch = 6
	cur := EpochForTime(now, es)

	// Within ±EpochFreshnessWindow (=1) → fresh.
	for _, e := range []uint64{cur, cur - 1, cur + 1} {
		if err := CheckSnapshotEpochFresh(e, now, es); err != nil {
			t.Fatalf("epoch %d should be fresh (cur=%d): %v", e, cur, err)
		}
	}
	// Two or more epochs stale/ahead → rejected.
	if err := CheckSnapshotEpochFresh(cur-2, now, es); err == nil {
		t.Fatalf("stale epoch %d (cur=%d) must be rejected", cur-2, cur)
	}
	if err := CheckSnapshotEpochFresh(cur+2, now, es); err == nil {
		t.Fatalf("future epoch %d (cur=%d) must be rejected", cur+2, cur)
	}
}
