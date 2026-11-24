package committee

import "testing"

// Cross-repo interop vector (v2). Regenerated with
// `seedNodes: go run ./cmd/committee-verify -vector`, which signs the v2
// canonical bytes — version|epoch|seed|<peer_id:bls_pub:reward_address>,...
// sorted by peer_id, empty reward_address kept as a trailing colon — with a
// dela/bls key and emits the public key + signature + served snapshot. jmdn
// stores only the PUBLIC key + signature and independently reconstructs the
// canonical bytes from the snapshot, proving the format and dela/bls
// verification are byte-for-byte compatible across the two repos.
//
// The key below is the committee-verify THROWAWAY test key (never a real seed
// authority). To refresh after a canonical-format change: rerun the command and
// replace these two consts + seedVectorSnapshot() with the new emission.
const (
	seedAuthorityPubHex = "37f54f8ae63f337316ba33c7c2611f8f4a9c6884cdfe3a2a5a59b5bb3d16114043d59759aa6305a48cd7332a94a82abac400f8675e48094508aab27f3be8b3a55e181d9015647a0b2312ae3ba096c9ccd10e161b519b213f67c102a220f43adb14ee644c7a40e97183cbe62f8c3983a8b50c4f82a6e8d251a84e277dd9b6f4e3"
	seedSnapshotSigHex  = "3ccbfe892ef2987d466aec78c20608a89ec0dd72be83a5dea8503d2e9fdaecc43ca4f30e5e35d662ae7d4d0267019c863ae1a23ec541340f9ad79797394d3191"
)

func seedVectorSnapshot() *CommitteeSnapshot {
	return &CommitteeSnapshot{
		Epoch: 490000,
		Seed:  "",
		Entries: []CommitteeEntry{
			{PeerID: "12D3KooWAlice", BLSPub: "aaaaaaaa", RewardAddress: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{PeerID: "12D3KooWBob", BLSPub: "bbbbbbbb", RewardAddress: ""},
			{PeerID: "12D3KooWCharlie", BLSPub: "cccccccc", RewardAddress: "0xcccccccccccccccccccccccccccccccccccccccc"},
		},
		AuthorityPubHex: seedAuthorityPubHex,
		Signature:       seedSnapshotSigHex,
	}
}

func TestInterop_SeedSignedSnapshotVerifies(t *testing.T) {
	// The genuine seed-signed snapshot verifies against the pinned authority.
	if err := VerifyCommitteeSnapshot(seedVectorSnapshot(), seedAuthorityPubHex); err != nil {
		t.Fatalf("seed-signed snapshot must verify against the pinned authority: %v", err)
	}

	// Pinned to a different authority -> rejected.
	if err := VerifyCommitteeSnapshot(seedVectorSnapshot(), "deadbeef"); err == nil {
		t.Fatal("must reject a snapshot not signed by the pinned authority")
	}

	// Tampered entry -> signature no longer matches the canonical bytes.
	badEntry := seedVectorSnapshot()
	badEntry.Entries[1].BLSPub = "cc"
	if err := VerifyCommitteeSnapshot(badEntry, seedAuthorityPubHex); err == nil {
		t.Fatal("tampered committee entry must invalidate the seed signature")
	}

	// Changed epoch -> different canonical bytes -> rejected.
	badEpoch := seedVectorSnapshot()
	badEpoch.Epoch = 2
	if err := VerifyCommitteeSnapshot(badEpoch, seedAuthorityPubHex); err == nil {
		t.Fatal("changed epoch must invalidate the seed signature")
	}
}
