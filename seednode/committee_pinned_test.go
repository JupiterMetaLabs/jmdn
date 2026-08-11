package seednode

import (
	"context"
	"testing"
	"time"

	"gossipnode/seednode/committee"
)

// W1 pool pinning — the committeeSource.eligible(epoch, pinned=true) path.
//
// These lock the two properties that make a pinned read safe to build committee
// selection on:
//  1. a pinned read resolves the EXACT epoch asked for (so a verifier can
//     re-derive a historical block's committee), bypassing wall-clock freshness;
//  2. if the seed answers with any other epoch, the read fails closed rather
//     than substituting it — that substitution is the whole W1 defect.

// pinnedFetch serves a snapshot at servedEpoch regardless of the epoch asked,
// and records the epoch it was actually asked for.
func pinnedFetch(t *testing.T, priv []byte, pubHex string, servedEpoch uint64) (func(context.Context, uint64) (*committee.CommitteeSnapshot, error), *uint64) {
	t.Helper()
	var got uint64
	f := func(_ context.Context, epoch uint64) (*committee.CommitteeSnapshot, error) {
		got = epoch
		return signSnapshot(t, priv, pubHex, servedEpoch, twoEntries()), nil
	}
	return f, &got
}

// A pinned read for a PAST epoch succeeds when the seed serves that exact epoch,
// even though wall-clock freshness (curEpoch) would reject it.
func TestPinnedRead_ExactPastEpoch_Succeeds(t *testing.T) {
	priv, pubHex := makeAuthority(t, "authority-pin-A")
	past := curEpoch() - 100 // far outside the ±1 freshness window

	fetch, asked := pinnedFetch(t, priv, pubHex, past)
	s := &committeeSource{
		fetch:        fetch,
		configPin:    pubHex, // pin so resolveAuthority skips TOFU/disk
		epochSeconds: testEpochSeconds,
		ttl:          time.Minute,
	}

	m, err := s.eligible(past, true)
	if err != nil {
		t.Fatalf("pinned read of exact past epoch should succeed, got: %v", err)
	}
	if *asked != past {
		t.Fatalf("pinned read must ask the seed for epoch %d, asked for %d", past, *asked)
	}
	if m["QmA"] != "aa" || m["QmB"] != "bb" {
		t.Fatalf("unexpected members: %v", m)
	}
}

// A pinned read fails closed when the seed serves a DIFFERENT epoch than asked —
// the substitution W1 exists to prevent.
func TestPinnedRead_EpochMismatch_FailsClosed(t *testing.T) {
	priv, pubHex := makeAuthority(t, "authority-pin-B")

	// Ask for a past epoch; the seed answers with the CURRENT one — exactly the
	// substitution the live path does today.
	past := curEpoch() - 100
	fetch, _ := pinnedFetch(t, priv, pubHex, curEpoch())
	s := &committeeSource{
		fetch:        fetch,
		configPin:    pubHex,
		epochSeconds: testEpochSeconds,
		ttl:          time.Minute,
	}

	if _, err := s.eligible(past, true); err == nil {
		t.Fatalf("pinned read must FAIL CLOSED when the seed serves a different epoch than asked")
	}
}

// The unpinned read (pinned=false) is unchanged: it serves the current epoch and
// is subject to wall-clock freshness, exactly as before W1.
func TestUnpinnedRead_UnchangedCurrentEpoch(t *testing.T) {
	priv, pubHex := makeAuthority(t, "authority-pin-C")
	fetch, asked := pinnedFetch(t, priv, pubHex, curEpoch())
	s := &committeeSource{
		fetch:        fetch,
		configPin:    pubHex,
		epochSeconds: testEpochSeconds,
		ttl:          time.Minute,
	}

	if _, err := s.eligible(0, false); err != nil {
		t.Fatalf("unpinned current read should succeed: %v", err)
	}
	if *asked != 0 {
		t.Fatalf("unpinned read must ask the seed for epoch 0 (current), asked %d", *asked)
	}
}
