package committee

import "testing"

// Cross-repo interop vector. The signature below was produced by the seedNodes
// committee authority's dela/bls PRIVATE key (test keypair from the seed team)
// over CanonicalCommitteeBytes(1, "", [QmPeerA:aa, QmPeerB:bb]). Only the PUBLIC
// key + signature are stored here (the private key is the seed's secret and is
// never committed). This asserts jmdn's independent consumer accepts a snapshot
// the seed actually signed — proving the canonical-bytes format and dela/bls
// verification are byte-for-byte compatible across the two repos.
//
// Additionally verified out-of-band during generation: jmdn's dela/bls derives
// the identical public key from the seed's private key (key-derivation parity).
const (
	seedAuthorityPubHex = "28008cfde91dc8021bfdf6da30253fba9dbbe69237cc0cf15c5ee4615b08730a6915ab77202754084a26bc00a3fba5213d7fb7669dd055e1c88b000e9e53db0e556b52c07ba0a902e17b17b974329f15681113e446b1c2d2f6c3e317cd92d6d93ac1377bbddd46455aaf09a414d5145edbc990b095abcfd6b6b14be430e6a58a"
	seedSnapshotSigHex  = "1c5b60f619e8046fad07bc933cfd2bb5585017c0f6c68b9e2bb86951386f4c8d29df110449cf3b471781bfa670102b4e853b4ce3d2517054dd3b164197f01f29"
)

func seedVectorSnapshot() *CommitteeSnapshot {
	return &CommitteeSnapshot{
		Epoch:           1,
		Seed:            "",
		Entries:         []CommitteeEntry{{PeerID: "QmPeerA", BLSPub: "aa"}, {PeerID: "QmPeerB", BLSPub: "bb"}},
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
