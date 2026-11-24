package main

import (
	"errors"
	"testing"
)

// A CatchUp that returns no error but no new blocks must not count as progress:
// the reconcile loop stops at the first peer it counts, so a peer serving
// nothing would end the round and leave every other target untried.
func TestCatchUpAdvanced(t *testing.T) {
	readErr := errors.New("local tip unreadable")
	cases := []struct {
		name      string
		reached   uint64
		tipBefore uint64
		tipErr    error
		want      bool
	}{
		{"advanced by one block", 101, 100, nil, true},
		{"advanced by many blocks", 5000, 100, nil, true},
		{"served nothing: reached == tip", 100, 100, nil, false},
		{"peer behind us: reached < tip", 99, 100, nil, false},
		{"zero tip, first blocks applied", 1, 0, nil, true},
		{"zero tip, nothing applied", 0, 0, nil, false},
		{"tip unreadable: do not block the catch-up", 100, 100, readErr, true},
		{"tip unreadable and behind: still do not block", 0, 100, readErr, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := catchUpAdvanced(c.reached, c.tipBefore, c.tipErr); got != c.want {
				t.Fatalf("catchUpAdvanced(reached=%d, tip=%d, err=%v) = %v, want %v",
					c.reached, c.tipBefore, c.tipErr, got, c.want)
			}
		})
	}
}
