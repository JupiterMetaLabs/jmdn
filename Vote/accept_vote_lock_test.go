package Vote

import (
	"testing"

	"gossipnode/internal/roundlock"
)

func TestReserveAcceptVote(t *testing.T) {
	prevLedger, prevOn := roundlock.Votes, roundlock.HashBindingEnabled
	roundlock.Votes = roundlock.NewLedger()
	t.Cleanup(func() { roundlock.Votes, roundlock.HashBindingEnabled = prevLedger, prevOn })

	roundlock.HashBindingEnabled = false
	if err := reserveAcceptVote(7, 0, "0xA"); err != nil {
		t.Fatal(err)
	}
	if err := reserveAcceptVote(7, 0, "0xB"); err != nil {
		t.Fatal("with rounds unable to advance, the lock must not apply")
	}

	roundlock.HashBindingEnabled = true
	if err := reserveAcceptVote(8, 0, "0xA"); err != nil {
		t.Fatal(err)
	}
	if err := reserveAcceptVote(8, 0, "0xA"); err != nil {
		t.Fatal("re-voting the same block must be allowed")
	}
	if err := reserveAcceptVote(8, 0, "0xB"); err == nil {
		t.Fatal("second block at the same round accepted")
	}
	if err := reserveAcceptVote(8, 1, "0xB"); err != nil {
		t.Fatal("next period must be open")
	}
}
