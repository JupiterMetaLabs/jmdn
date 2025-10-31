package votemodule

import (
	"errors"
	"fmt"
	"math"
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
	fmt.Println("original=", weight, "correct=", correct, "newValue=", 1/(1+math.Exp(-logValue)))
	return 1 / (1 + math.Exp(-logValue))
}
