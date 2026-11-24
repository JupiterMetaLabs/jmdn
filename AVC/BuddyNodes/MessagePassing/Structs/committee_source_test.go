package Structs

import (
	"fmt"
	"testing"
)

func TestAuthorizedCommittee_FailsClosedWhenUnset(t *testing.T) {
	old := authorizedCommitteeFn
	authorizedCommitteeFn = nil
	defer func() { authorizedCommitteeFn = old }()

	if _, err := authorizedCommittee(); err == nil {
		t.Fatal("authorizedCommittee must error when no source is installed")
	}
}

func TestAuthorizedCommittee_ReturnsInjectedSet(t *testing.T) {
	old := authorizedCommitteeFn
	defer func() { authorizedCommitteeFn = old }()

	want := map[string]string{"peer1": "abc123"}
	SetAuthorizedCommitteeFn(func() (map[string]string, error) { return want, nil })

	got, err := authorizedCommittee()
	if err != nil {
		t.Fatalf("authorizedCommittee: %v", err)
	}
	if len(got) != 1 || got["peer1"] != "abc123" {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAuthorizedCommittee_PropagatesSourceError(t *testing.T) {
	old := authorizedCommitteeFn
	defer func() { authorizedCommitteeFn = old }()

	SetAuthorizedCommitteeFn(func() (map[string]string, error) { return nil, fmt.Errorf("boom") })

	if _, err := authorizedCommittee(); err == nil {
		t.Fatal("authorizedCommittee must propagate the source's own error, not swallow it")
	}
}

// W1: when the per-height source is wired the tally uses it (the anchored pool
// of the tallied height's period), not the height-less current source.
func TestAuthorizedCommitteeFor_PrefersPerHeightSource(t *testing.T) {
	oldAny, oldH := authorizedCommitteeFn, authorizedCommitteeForHeightFn
	defer func() { authorizedCommitteeFn, authorizedCommitteeForHeightFn = oldAny, oldH }()

	SetAuthorizedCommitteeFn(func() (map[string]string, error) { return map[string]string{"current": "00"}, nil })
	var asked uint64
	SetAuthorizedCommitteeForHeightFn(func(h uint64) (map[string]string, error) {
		asked = h
		return map[string]string{"anchored": "11"}, nil
	})
	got, err := authorizedCommitteeFor(905)
	if err != nil || got["anchored"] != "11" || asked != 905 {
		t.Fatalf("got %v err=%v asked=%d; want the per-height source for 905", got, err, asked)
	}

	SetAuthorizedCommitteeForHeightFn(nil)
	got, err = authorizedCommitteeFor(905)
	if err != nil || got["current"] != "00" {
		t.Fatalf("unwired per-height source must fall back to the current source, got %v err=%v", got, err)
	}

	SetAuthorizedCommitteeForHeightFn(func(uint64) (map[string]string, error) { return nil, fmt.Errorf("anchor missing") })
	if _, err := authorizedCommitteeFor(905); err == nil {
		t.Fatalf("a per-height source error must propagate (fail closed), not fall back")
	}
}
