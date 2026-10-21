package seednode

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	blssign "gossipnode/AVC/BLS/bls-sign"
	"gossipnode/internal/sequencerid"
	"gossipnode/seednode/committee"
)

// A verified current-epoch v3 snapshot publishes its sequencer peer ID to
// internal/sequencerid.
func TestCommitteeSource_RecordsSeedSequencer(t *testing.T) {
	const seq = "12D3KooWEyoppNCUx8Yx66oV9fJnriXwCcXwDDUA2kj6vnc6iDEp"
	priv, pubHex := makeAuthority(t, "authority-seq")
	ep := curEpoch()
	entries := twoEntries()
	sig, err := blssign.BLSSign(priv, committee.CanonicalCommitteeBytesWithSequencer(ep, "", seq, entries))
	if err != nil {
		t.Fatal(err)
	}
	snap := &committee.CommitteeSnapshot{
		Epoch: ep, Entries: entries, SequencerPeerID: seq,
		AuthorityPubHex: pubHex, Signature: hex.EncodeToString(sig),
	}
	s := &committeeSource{
		fetch:        func(context.Context, uint64) (*committee.CommitteeSnapshot, error) { return snap, nil },
		epochSeconds: testEpochSeconds,
		pinFile:      filepath.Join(t.TempDir(), "seedAuth.json"),
		seedURL:      "test-seed",
		ttl:          time.Minute,
	}
	if _, err := s.eligible(0, false); err != nil {
		t.Fatalf("eligible: %v", err)
	}
	if got := sequencerid.FromSeed(); got != seq {
		t.Fatalf("seed sequencer not recorded: got %q", got)
	}
}
