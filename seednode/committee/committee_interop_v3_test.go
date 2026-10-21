package committee

import "testing"

// Cross-repo interop vector (v3): the seed signs the sequencer peer ID into the
// canonical bytes (jmdt/committee/v3|epoch|seed|sequencer|<entries>). Produced
// with seedNodes peer.BuildAndSignCommitteeSnapshotWithSequencer over the same
// entries as the v2 vector, with a throwaway key (never a real seed authority).
const (
	seedV3AuthorityPubHex = "4a6aa87fe6b4b59120923f25db5134f7476660260329c8d884fbddff411448af0dec6f8560e65d6cd05ebfa4bbb854e4ae95c97146dab2b68cdc7efa4b87612080e8e7e0fe6f369803109cd3af45adb4407ef63ae15398074a7f3d6dd0d6e6401b85f946d1e4c67872bbbe9086af0e4a644fb259c5e70aa24fa3496ab850caf8"
	seedV3SnapshotSigHex  = "4ff593725b9d7bfedfb2f6e272b0d3681e3ff7b00357dd703ffb6903e45b5c2f2d6a9ccc7d768af9b3017d40154eb107d40779507433d39bc86deff4ca80cc63"
	seedV3Sequencer       = "12D3KooWEyoppNCUx8Yx66oV9fJnriXwCcXwDDUA2kj6vnc6iDEp"
)

func seedV3VectorSnapshot() *CommitteeSnapshot {
	s := seedVectorSnapshot()
	s.SequencerPeerID = seedV3Sequencer
	s.AuthorityPubHex = seedV3AuthorityPubHex
	s.Signature = seedV3SnapshotSigHex
	return s
}

func TestInterop_SeedSignedV3SnapshotVerifies(t *testing.T) {
	if err := VerifyCommitteeSnapshot(seedV3VectorSnapshot(), seedV3AuthorityPubHex); err != nil {
		t.Fatalf("seed-signed v3 snapshot must verify: %v", err)
	}
	changed := seedV3VectorSnapshot()
	changed.SequencerPeerID = "12D3KooWHHzSeKaY8xuZVzkLbKFfvNgPPeKhFBGrMbNzbm5akpqu"
	if VerifyCommitteeSnapshot(changed, seedV3AuthorityPubHex) == nil {
		t.Fatal("a different sequencer peer ID must not verify")
	}
	stripped := seedV3VectorSnapshot()
	stripped.SequencerPeerID = ""
	if VerifyCommitteeSnapshot(stripped, seedV3AuthorityPubHex) == nil {
		t.Fatal("stripping the sequencer peer ID must not verify as v2")
	}
}
