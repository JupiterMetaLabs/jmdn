package votemodule

import (
	"context"
	"errors"
	"fmt"
	"math"

	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

func VoteAggregation(weights map[string]float64, votes map[string]int8) (bool, error) {
	var positiveVotes float64
	var negetiveVotes float64
	if len(weights) != len(votes) {
		return false, errors.New("length mismatch between maps")
	}
	for address, weight := range weights {
		voteValue := votes[address]
		if weight > 1 || weight < 0 {
			return false, errors.New("invalid weight value")
		}
		if voteValue != 1 && voteValue != -1 {
			return false, errors.New("invalid vote value")
		}
		switch voteValue {
		case 1:
			positiveVotes = positiveVotes + (1 * weight)
		case -1:
			negetiveVotes = negetiveVotes + (1 * weight)
		}
	}
	if positiveVotes > negetiveVotes {
		return true, nil
	}
	return false, nil
}

// MajorityDecision is the Stage 4 (JMDN-CRDT-VOTE-MIGRATION-LLD.md)
// vote-decision function. Unlike VoteAggregation, it takes NO weight map:
// reputation/stake weight must never multiply an already-cast validator
// vote (that would let a single high-reputation validator outvote several
// others). Weight belongs only to Buddy/Aggregator SELECTION
// (avc/docs/COMMITTEE-SELECTION-ALGORITHM.md's A-ExpJ), never to counting
// votes once cast. Each authorized validator's vote counts as exactly one,
// win by simple majority of yes vs no.
//
// votes is expected to be avcvotes.BlockTally.SingleVotePeers() — i.e.
// already restricted to the authorized committee for this block and
// already stripped of equivocating peers (peers with >1 distinct vote
// value), so no peer can be counted twice or in both directions here.
//
// Untested: no Go toolchain was available in the environment this was
// written in (avc repo has no build/test run backing this specific
// function yet). Validate with:
//   cd avc && go test ./crdt/votes/... ./AVC/VoteModule/... 2>&1 | tee /tmp/stage4.log
// (adjust the second path to wherever this package resolves under
// go.work) and add a table test asserting: ties (equal yes/no) return
// (false, nil) — reject on tie, matching "yes > no" below, not "yes >=
// no"; an empty votes map returns (false, nil) with no error (0 > 0 is
// false); and any vote value other than 1/-1 returns an error rather than
// being silently ignored.
func MajorityDecision(votes map[string]int8) (bool, error) {
	var yes, no int
	for peerID, v := range votes {
		switch v {
		case 1:
			yes++
		case -1:
			no++
		default:
			return false, fmt.Errorf("invalid vote value %d for peer %s", v, peerID)
		}
	}
	return yes > no, nil
}

func WeightAggregation(weight float64, correct bool, alpha float64, beta float64) float64 {
	if alpha == 0 {
		alpha = 0.3
	}
	if beta == 0 {
		beta = 2.0
	}
	var delta float64

	if correct {
		delta = alpha
	} else if !correct {
		delta = alpha * (-beta)
	}
	// logit transform (add delta in log-odds space)
	logValue := math.Log(weight/(1-weight)) + delta
	// sigmoid value
	logger(log.VoteModule).Debug(context.Background(), "Vote calculation", ion.Float64("original", weight), ion.Bool("correct", correct), ion.Float64("new_value", 1/(1+math.Exp(-logValue))))
	return 1 / (1 + math.Exp(-logValue))
}

// logger returns the ion logger instance for vote module
func logger(namedLogger string) *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(namedLogger, "")
	if err != nil {
		return nil
	}
	return logInstance.GetNamedLogger()
}
