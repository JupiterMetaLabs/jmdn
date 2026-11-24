package messaging

import (
	"errors"
	"reflect"
	"testing"

	"gossipnode/config/settings"
)

// The rollback contract, stated as a test: with JMDN_COMMITTEE_V2 off the new
// seam returns EXACTLY what the old wiring returned. main.go used to pass
// messaging.AuthorizedCommittee directly; this asserts the substitution is
// observationally identical while the flag is down, which is what makes
// "turn the flag off" a complete rollback.
func TestAuthorizedCommitteeForTally_FlagOff_IsLegacyPath(t *testing.T) {
	wireEligibility(t, 7)

	prev := CommitteeV2Enabled
	CommitteeV2Enabled = false
	t.Cleanup(func() { CommitteeV2Enabled = prev })

	want, wantErr := AuthorizedCommittee()
	got, gotErr := AuthorizedCommitteeForTally()

	if (wantErr == nil) != (gotErr == nil) {
		t.Fatalf("error mismatch: legacy=%v new=%v", wantErr, gotErr)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("flag-off path diverged from the legacy source\nlegacy: %v\nnew:    %v", want, got)
	}
	t.Log("flag off: AuthorizedCommitteeForTally is AuthorizedCommittee, member for member")
}

// With the flag on the seam resolves the UNCAPPED pool - the same set
// SelectCommittee draws its seats from - so the buddy's tally and the
// sequencer's certification stop reading two different sources.
func TestAuthorizedCommitteeForTally_FlagOn_IsUncappedPool(t *testing.T) {
	wireEligibility(t, 7)

	prev := CommitteeV2Enabled
	CommitteeV2Enabled = true
	t.Cleanup(func() { CommitteeV2Enabled = prev })

	want, wantErr := eligibleMembersUncapped()
	got, gotErr := AuthorizedCommitteeForTally()

	if (wantErr == nil) != (gotErr == nil) {
		t.Fatalf("error mismatch: uncapped=%v new=%v", wantErr, gotErr)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("flag-on path is not the uncapped pool\nuncapped: %v\nnew:      %v", want, got)
	}
	t.Log("flag on: the tally seam resolves the pool SelectCommittee seats from")
}

// The two paths must actually DIFFER once the pool exceeds max_validators -
// otherwise this whole fix is untestable and the split-brain it closes would be
// invisible. Capped (legacy) trims to max_validators by sorted peer_id;
// uncapped (v2) keeps the whole pool.
//
// Skipped when the settings singleton is not loaded: committeeSizeLimit()
// returns 0 (no cap) then, so capped == uncapped by construction and there is
// nothing to distinguish. Same guard convention as committeeOfSize.
func TestAuthorizedCommitteeForTally_PathsDivergeAboveCap(t *testing.T) {
	if !settings.IsLoaded() {
		t.Skip("settings not loaded: committeeSizeLimit() is 0, so capped == uncapped and there is no divergence to assert")
	}

	wireEligibility(t, 7)

	c := settings.Get()
	prevCap := c.Consensus.MaxValidators
	c.Consensus.MaxValidators = 3
	t.Cleanup(func() { c.Consensus.MaxValidators = prevCap })

	prev := CommitteeV2Enabled
	t.Cleanup(func() { CommitteeV2Enabled = prev })

	CommitteeV2Enabled = false
	legacy, err := AuthorizedCommitteeForTally()
	if err != nil {
		t.Fatalf("flag-off resolve failed: %v", err)
	}

	CommitteeV2Enabled = true
	v2, err := AuthorizedCommitteeForTally()
	if err != nil {
		t.Fatalf("flag-on resolve failed: %v", err)
	}

	if len(legacy) != 3 {
		t.Fatalf("flag off: capped set has %d members, want 3 (max_validators)", len(legacy))
	}
	if len(v2) != 7 {
		t.Fatalf("flag on: pool has %d members, want 7 (uncapped)", len(v2))
	}

	// And the capped set must be a strict subset of the pool - the v2 path may
	// only ever ADD members a buddy could weigh, never substitute different ones.
	for pid := range legacy {
		if _, ok := v2[pid]; !ok {
			t.Fatalf("peer %s is in the capped set but not in the pool: the paths disagree on membership, not just size", pid)
		}
	}
	t.Log("capped=3, pool=7, capped is a subset: the fix is observable and one-directional")
}

// Rollback is not just "the flag exists" - flipping it back must restore the
// legacy result bit for bit, after the v2 path has already been exercised in
// the same process. This is the assertion an operator's rollback depends on.
func TestAuthorizedCommitteeForTally_FlagOffRestoresLegacy(t *testing.T) {
	wireEligibility(t, 7)

	prev := CommitteeV2Enabled
	t.Cleanup(func() { CommitteeV2Enabled = prev })

	CommitteeV2Enabled = false
	before, err := AuthorizedCommitteeForTally()
	if err != nil {
		t.Fatalf("baseline resolve failed: %v", err)
	}

	CommitteeV2Enabled = true
	if _, err := AuthorizedCommitteeForTally(); err != nil {
		t.Fatalf("v2 resolve failed: %v", err)
	}

	CommitteeV2Enabled = false
	after, err := AuthorizedCommitteeForTally()
	if err != nil {
		t.Fatalf("post-rollback resolve failed: %v", err)
	}

	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rollback did not restore legacy behaviour\nbefore: %v\nafter:  %v", before, after)
	}
	t.Log("off -> on -> off returns the identical legacy set: the flag is a true rollback, not a one-way door")
}

// Fail closed on BOTH paths. A flag must never turn a broken eligibility source
// into an authorized set: the error propagates and the map stays empty either
// way, so TallyBlock authorizes nobody rather than counting unauthenticated
// votes.
func TestAuthorizedCommitteeForTally_FailsClosedOnBothPaths(t *testing.T) {
	sentinel := errors.New("eligibility source down")
	SetCommitteeEligibilitySource(func(_ uint64, _ bool) (map[string]string, error) { return nil, sentinel })
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })

	prev := CommitteeV2Enabled
	t.Cleanup(func() { CommitteeV2Enabled = prev })

	for _, flag := range []bool{false, true} {
		CommitteeV2Enabled = flag
		got, err := AuthorizedCommitteeForTally()
		if err == nil {
			t.Fatalf("CommitteeV2Enabled=%v: resolved %d members with a broken source, want an error", flag, len(got))
		}
		if len(got) != 0 {
			t.Fatalf("CommitteeV2Enabled=%v: returned %d members alongside an error, want none", flag, len(got))
		}
	}
	t.Log("both paths fail closed: a broken source authorizes nobody, on either side of the flag")
}
