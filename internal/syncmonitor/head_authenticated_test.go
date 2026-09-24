package syncmonitor_test

// HeadAuthenticated must survive the trip from the reporter to GetStatus(),
// because main.go's consensus vote gate reads it off Status and abstains from
// voting when the head says this node is more than MaxConsensusLagBlocks (2)
// behind.
//
// The risk this pins: thebesync.PeerReporter derives SequencerHead from the
// MAXIMUM height claimed by up to 8 unauthenticated peers, so a single peer
// answering FetchHead with localHead+3 would push a validator into abstaining
// — removing it from quorum for the cost of one small, plausible-looking lie.
// The head is still reported (the propagation-lag filter needs it) and
// HeadAuthenticated=false is what keeps it out of the gate.
//
// If this flag is ever dropped in the Status copy, the gate silently starts
// trusting peer chatter again and nothing else fails. Hence a test.

import (
	"context"
	"testing"
	"time"

	"gossipnode/internal/syncmonitor"
)

// unvouchedReporter mimics thebesync.PeerReporter: a real head, no vouching.
type unvouchedReporter struct {
	head uint64
}

func (r unvouchedReporter) ReportBlockState(_ context.Context, _ uint64, _ []byte) (*syncmonitor.SyncStatus, error) {
	return &syncmonitor.SyncStatus{
		IsSynced:          false,
		SequencerHead:     r.head,
		HeadAuthenticated: false, // the property under test
		Message:           "peer-sampled, unauthenticated",
	}, nil
}

// vouchedReporter mimics the seednode-backed adapter.
type vouchedReporter struct {
	head uint64
}

func (r vouchedReporter) ReportBlockState(_ context.Context, _ uint64, _ []byte) (*syncmonitor.SyncStatus, error) {
	return &syncmonitor.SyncStatus{
		IsSynced:          false,
		SequencerHead:     r.head,
		HeadAuthenticated: true,
		Message:           "seednode",
	}, nil
}

func TestHeadAuthenticatedReachesStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reporter syncmonitor.SeedReporter
		want     bool
	}{
		{"peer-sampled head is NOT authenticated", unvouchedReporter{head: 500}, false},
		{"seednode head IS authenticated", vouchedReporter{head: 500}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain := &stubReporter{head: 100, root: []byte("r")}
			m := syncmonitor.New(chain, tc.reporter, time.Minute).WithOutOfSyncThreshold(1)

			st := m.TriggerCheck(context.Background())

			if st.HeadAuthenticated != tc.want {
				t.Fatalf("HeadAuthenticated = %v, want %v — the consensus vote gate keys on this; "+
					"losing it in the Status copy lets an unauthenticated peer-sampled head decide voting",
					st.HeadAuthenticated, tc.want)
			}
			// The head itself must still be reported either way: the
			// propagation-lag filter needs it, and suppressing it would turn
			// every one-block lag into a catch-up.
			if st.SequencerHead != 500 {
				t.Fatalf("SequencerHead = %d, want 500 — the head must still be published "+
					"for the lag filter regardless of whether it is vouched for", st.SequencerHead)
			}
		})
	}
}
