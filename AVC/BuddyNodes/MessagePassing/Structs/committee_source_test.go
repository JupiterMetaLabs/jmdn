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
