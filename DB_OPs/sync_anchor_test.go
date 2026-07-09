package DB_OPs

// Unit tests for the accounts-applied anchor's pure decision rules.
// Each test names the invariant it pins:
//   - effects must never be silently skipped: the anchor never exceeds a
//     block whose effects are not fully applied
//   - effects must never be applied twice across live processing and
//     reconciliation
//   - the anchor is co-located with the data it describes (storage property,
//     enforced by placement in accountsdb; not testable here)

import (
	"errors"
	"testing"
)

func TestNextLiveAnchor_ContiguityRule(t *testing.T) {
	// A gap means blocks below are missing — the live path must NOT jump
	// the anchor over them (their effects would be skipped forever).
	cases := []struct {
		name         string
		current      uint64
		block        uint64
		wantAnchor   uint64
		wantAdvanced bool
	}{
		{"contiguous next advances", 10, 11, 11, true},
		{"gap does NOT advance", 10, 15, 10, false},
		{"duplicate/older does NOT advance", 10, 10, 10, false},
		{"far older does NOT advance", 10, 3, 10, false},
		{"genesis start: block 1 from anchor 0", 0, 1, 1, true},
		{"bootstrap node mid-chain: no jump from 0", 0, 5000, 0, false},
	}
	for _, c := range cases {
		got, advanced := NextLiveAnchor(c.current, c.block)
		if got != c.wantAnchor || advanced != c.wantAdvanced {
			t.Errorf("%s: NextLiveAnchor(%d, %d) = (%d, %v), want (%d, %v)",
				c.name, c.current, c.block, got, advanced, c.wantAnchor, c.wantAdvanced)
		}
	}
}

func TestNextReconAnchor_MonotonicMax(t *testing.T) {
	// The anchor never moves backwards: a regression would re-open ranges whose
	// recon-applied txs carry no markers → double-apply on the next run.
	cases := []struct {
		name         string
		current      uint64
		target       uint64
		wantAnchor   uint64
		wantAdvanced bool
	}{
		{"forward advances", 10, 500, 500, true},
		{"equal is a no-op", 500, 500, 500, false},
		{"backwards is refused", 500, 10, 500, false},
		{"zero target refused", 500, 0, 500, false},
		{"first advance from zero", 0, 42, 42, true},
	}
	for _, c := range cases {
		got, advanced := NextReconAnchor(c.current, c.target)
		if got != c.wantAnchor || advanced != c.wantAdvanced {
			t.Errorf("%s: NextReconAnchor(%d, %d) = (%d, %v), want (%d, %v)",
				c.name, c.current, c.target, got, advanced, c.wantAnchor, c.wantAdvanced)
		}
	}
}

func TestCapAnchorTarget_PoisonGuard(t *testing.T) {
	// The anchor may never claim blocks the node does not verifiably hold.
	// Two real poison sources: HandleSync's MaxUint64 substitution for legacy
	// peers, and legacy SQLite watermarks carrying that value into the seed.
	const maxU64 = ^uint64(0)
	cases := []struct {
		name        string
		target, tip uint64
		want        uint64
	}{
		{"MaxUint64 poison capped to tip", maxU64, 12077, 12077},
		{"target above tip capped", 20000, 12077, 12077},
		{"target at tip passes", 12077, 12077, 12077},
		{"target below tip passes", 5000, 12077, 5000},
		{"zero tip caps everything to zero", 42, 0, 0},
	}
	for _, c := range cases {
		if got := CapAnchorTarget(c.target, c.tip); got != c.want {
			t.Errorf("%s: CapAnchorTarget(%d, %d) = %d, want %d", c.name, c.target, c.tip, got, c.want)
		}
	}
}

func TestShouldAdvanceReconAnchor_AllProofsRequired(t *testing.T) {
	// Advancing on anything less than (no error AND zero failed accounts
	// AND verified data-complete) stamps unapplied ranges as done —
	// historically, failedAccounts>0 and skeleton-block ranges were marked
	// complete, silently skipping their effects.
	someErr := errors.New("recon failed")
	cases := []struct {
		name     string
		reconErr error
		failed   int
		verified bool
		want     bool
	}{
		{"all proofs present → advance", nil, 0, true, true},
		{"recon error blocks", someErr, 0, true, false},
		{"failed accounts block", nil, 3, true, false},
		{"unverified range blocks", nil, 0, false, false},
		{"everything wrong blocks", someErr, 3, false, false},
	}
	for _, c := range cases {
		if got := ShouldAdvanceReconAnchor(c.reconErr, c.failed, c.verified); got != c.want {
			t.Errorf("%s: ShouldAdvanceReconAnchor(%v, %d, %v) = %v, want %v",
				c.name, c.reconErr, c.failed, c.verified, got, c.want)
		}
	}
}
