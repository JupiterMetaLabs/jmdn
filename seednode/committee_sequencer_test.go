package seednode

import (
	"context"
	"encoding/hex"
	"errors"
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

// refreshCurrent bypasses the TTL cache: within the TTL, a changed sequencer
// in the seed's snapshot is recorded at once. A failed fetch keeps the cache.
func TestCommitteeSource_RefreshBypassesTTL(t *testing.T) {
	const seqA = "12D3KooWEyoppNCUx8Yx66oV9fJnriXwCcXwDDUA2kj6vnc6iDEp"
	const seqB = "12D3KooWLRPJAA5o6F4pXHpHg9jgfwV9WCUm1Ftr1MBRjFvVWU1f"
	priv, pubHex := makeAuthority(t, "authority-refresh")
	ep := curEpoch()
	entries := twoEntries()
	signed := func(seq string) *committee.CommitteeSnapshot {
		sig, err := blssign.BLSSign(priv, committee.CanonicalCommitteeBytesWithSequencer(ep, "", seq, entries))
		if err != nil {
			t.Fatal(err)
		}
		return &committee.CommitteeSnapshot{
			Epoch: ep, Entries: entries, SequencerPeerID: seq,
			AuthorityPubHex: pubHex, Signature: hex.EncodeToString(sig),
		}
	}
	serve := signed(seqA)
	var fetchErr error
	fetches := 0
	s := &committeeSource{
		fetch: func(context.Context, uint64) (*committee.CommitteeSnapshot, error) {
			fetches++
			return serve, fetchErr
		},
		configPin:    pubHex,
		epochSeconds: testEpochSeconds,
		ttl:          time.Hour,
	}
	if _, err := s.eligible(0, false); err != nil {
		t.Fatalf("eligible: %v", err)
	}
	if got := sequencerid.FromSeed(); got != seqA {
		t.Fatalf("want %s recorded, got %q", seqA, got)
	}

	serve = signed(seqB)
	if _, err := s.eligible(0, false); err != nil || fetches != 1 {
		t.Fatalf("setup: eligible within the TTL must use the cache (fetches=%d, err=%v)", fetches, err)
	}
	s.refreshCurrent()
	if fetches != 2 {
		t.Fatalf("refresh must fetch despite the TTL, fetches=%d", fetches)
	}
	if got := sequencerid.FromSeed(); got != seqB {
		t.Fatalf("refresh must record the new sequencer %s, got %q", seqB, got)
	}

	cachedAt := s.cachedAt
	fetchErr = errors.New("seed unreachable")
	s.refreshCurrent()
	if s.cachedAt != cachedAt || s.cachedSnap == nil || s.cachedSnap.SequencerPeerID != seqB {
		t.Fatal("a failed refresh must keep the cached snapshot and its timestamp")
	}
}
