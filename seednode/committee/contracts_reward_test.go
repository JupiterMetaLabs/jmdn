package committee

// UNTESTED-LOCALLY: no Go toolchain in the authoring environment. Run with:
//   go test ./seednode/committee/ -run Reward -v
//
// This pins jmdn's v2 canonical bytes to the seedNodes interop vector
// (committee-source-interop-vector.json) BYTE-FOR-BYTE. A mismatch here means a
// v2 seed snapshot will fail jmdn-side signature verification — the exact bug
// this test exists to catch before deploy.

import (
	"strings"
	"testing"
)

func TestRewardCanonicalBytes_MatchesSeedInteropVectorV2(t *testing.T) {
	addrA := "0x" + strings.Repeat("a", 40)
	addrC := "0x" + strings.Repeat("c", 40)

	// Deliberately UNSORTED and with UPPERCASE hex, exactly as the seed vector
	// documents its input — the signed/canonical form must sort by peer_id and
	// lowercase bls_pub + reward_address.
	entries := []CommitteeEntry{
		{PeerID: "12D3KooWCharlie", BLSPub: "CCCCCCCC", RewardAddress: "0x" + strings.Repeat("C", 40)},
		{PeerID: "12D3KooWAlice", BLSPub: "aaaaaaaa", RewardAddress: addrA},
		{PeerID: "12D3KooWBob", BLSPub: "bbbbbbbb", RewardAddress: ""}, // unbound → trailing colon
	}

	got := string(CanonicalCommitteeBytes(490000, "", entries))
	want := "jmdt/committee/v2|490000||" +
		"12D3KooWAlice:aaaaaaaa:" + addrA + "," +
		"12D3KooWBob:bbbbbbbb:," + // empty reward = trailing colon, field never dropped
		"12D3KooWCharlie:cccccccc:" + addrC

	if got != want {
		t.Fatalf("canonical bytes mismatch with seed interop vector:\n got=%q\nwant=%q", got, want)
	}
}

func TestRewardAddrByPeer_OmitsUnbound(t *testing.T) {
	snap := &CommitteeSnapshot{Entries: []CommitteeEntry{
		{PeerID: "A", BLSPub: "aa", RewardAddress: "0x" + strings.Repeat("A", 40)}, // uppercase → normalized
		{PeerID: "B", BLSPub: "bb", RewardAddress: ""},                             // unbound → omitted
	}}
	m := snap.RewardAddrByPeer()
	if len(m) != 1 {
		t.Fatalf("want 1 bound address, got %d: %v", len(m), m)
	}
	if m["A"] != "0x"+strings.Repeat("a", 40) {
		t.Fatalf("A not lowercased/normalized: %q", m["A"])
	}
	if _, ok := m["B"]; ok {
		t.Fatal("unbound peer B must be omitted from the reward map")
	}
}
