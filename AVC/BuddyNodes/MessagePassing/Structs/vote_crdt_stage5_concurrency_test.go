package Structs

// Stage 5 concurrency tests: verifyTallySignatures / verifyTallySigTasksConcurrently
// were changed from a sequential loop to a bounded worker pool. These tests
// cover the scenarios the sequential-only tests in vote_crdt_stage5_test.go
// do not: multiple simultaneous forgeries, mixed YES/NO at scale, a
// 1000-validator load, and — the property unique to the concurrent version —
// that the resulting tally's CONTENT is identical no matter how many workers
// did the verifying or how the scheduler interleaved them.

import (
	"reflect"
	"testing"

	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
)

// buildMixedTally creates n peers: peers with index%3==0 get a forged
// signature, the rest get a genuinely signed vote alternating YES/NO. It
// returns the tally plus how many of the n peers were genuinely valid.
func buildMixedTally(t *testing.T, n int) (avcvotes.BlockTally, int) {
	t.Helper()
	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: make(map[string][]int8, n),
		Signatures:            make(map[string][]avcvotes.VoteRecord, n),
	}
	validCount := 0
	for i := 0; i < n; i++ {
		p := stage5TestPeer(t)
		vote := int8(1)
		if i%2 == 1 {
			vote = -1
		}
		if i%3 == 0 {
			// Forged: syntactically valid signature, but for a different
			// message than the one being tallied.
			other, _, err := BLS_Signer.SignMessageForBlock(vote, stage5ChainID, stage5Height, "0xnot-this-block")
			if err != nil {
				t.Fatalf("building forged fixture %d: %v", i, err)
			}
			rec := avcvotes.VoteRecord{
				PeerID: p.String(), Vote: vote, BlockHash: stage5BlockHash, Height: stage5Height,
				BLSSignature: other.Signature, BLSPubKeyHex: other.PubKey,
			}
			tally.AuthorizedVotesByPeer[p.String()] = []int8{vote}
			tally.Signatures[p.String()] = []avcvotes.VoteRecord{rec}
			continue
		}
		rec := stage5SignedRecord(t, p, vote, stage5Height, stage5BlockHash)
		tally.AuthorizedVotesByPeer[p.String()] = []int8{vote}
		tally.Signatures[p.String()] = []avcvotes.VoteRecord{rec}
		validCount++
	}
	return tally, validCount
}

// Multiple invalid signatures, mixed in among valid ones, must ALL be
// dropped — not just the first one encountered, and dropping one must not
// affect any other peer's independent outcome.
func TestVerifyTallySignatures_MultipleInvalidSignaturesAllDropped(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	const n = 30 // i%3==0 -> 10 forged, 20 genuine
	tally, wantValid := buildMixedTally(t, n)

	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash)
	wantDropped := n - wantValid
	if dropped != wantDropped {
		t.Fatalf("expected %d dropped forgeries, got %d", wantDropped, dropped)
	}
	if got := len(verified.SingleVotePeers()); got != wantValid {
		t.Fatalf("expected %d surviving genuine votes, got %d", wantValid, got)
	}
}

// Mixed YES/NO votes at moderate scale, all genuinely signed: every vote
// must survive and the YES/NO split must be exact.
func TestVerifyTallySignatures_MixedYesNoVotesSurviveIntact(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	const n = 40
	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: make(map[string][]int8, n),
		Signatures:            make(map[string][]avcvotes.VoteRecord, n),
	}
	wantYes, wantNo := 0, 0
	for i := 0; i < n; i++ {
		p := stage5TestPeer(t)
		vote := int8(1)
		if i%2 == 1 {
			vote = -1
			wantNo++
		} else {
			wantYes++
		}
		rec := stage5SignedRecord(t, p, vote, stage5Height, stage5BlockHash)
		tally.AuthorizedVotesByPeer[p.String()] = []int8{vote}
		tally.Signatures[p.String()] = []avcvotes.VoteRecord{rec}
	}

	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash)
	if dropped != 0 {
		t.Fatalf("all votes were genuinely signed, expected 0 dropped, got %d", dropped)
	}
	gotYes, gotNo := 0, 0
	for _, v := range verified.SingleVotePeers() {
		switch v {
		case 1:
			gotYes++
		case -1:
			gotNo++
		default:
			t.Fatalf("unexpected vote value %d", v)
		}
	}
	if gotYes != wantYes || gotNo != wantNo {
		t.Fatalf("YES/NO split changed under verification: want yes=%d no=%d, got yes=%d no=%d", wantYes, wantNo, gotYes, gotNo)
	}
}

// 1000-validator load: the scale the worker pool is meant for. Confirms
// correctness (not just speed) holds at the requirement's stated ceiling.
func TestVerifyTallySignatures_LargeLoad1000Votes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 1000-vote load test in -short mode")
	}
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	const n = 1000
	tally, wantValid := buildMixedTally(t, n)

	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash)
	wantDropped := n - wantValid
	if dropped != wantDropped {
		t.Fatalf("expected %d dropped forgeries out of %d, got %d", wantDropped, n, dropped)
	}
	if got := len(verified.SingleVotePeers()); got != wantValid {
		t.Fatalf("expected %d surviving genuine votes out of %d, got %d", wantValid, n, got)
	}
}

// The concurrent verification path must produce byte-identical tally
// CONTENT (same peers, same votes, same records) no matter how many
// workers ran or how they were scheduled. This is the determinism
// requirement: run the same input repeatedly under 1 worker (effectively
// sequential), a small pool, and the production default, and require every
// run's resulting map content to be equal.
func TestVerifyTallySignatures_DeterministicAcrossWorkerCounts(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	const n = 60
	tally, _ := buildMixedTally(t, n)

	origWorkers := verifyTallySignaturesWorkers
	defer func() { verifyTallySignaturesWorkers = origWorkers }()

	workerCounts := []int{1, 2, 3, 8, 32, origWorkers}

	var baseline avcvotes.BlockTally
	var baselineDropped int
	for i, w := range workerCounts {
		verifyTallySignaturesWorkers = w
		verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash)
		if i == 0 {
			baseline = verified
			baselineDropped = dropped
			continue
		}
		if dropped != baselineDropped {
			t.Fatalf("worker count %d: dropped=%d, want %d (from worker count %d)", w, dropped, baselineDropped, workerCounts[0])
		}
		if !reflect.DeepEqual(verified.AuthorizedVotesByPeer, baseline.AuthorizedVotesByPeer) {
			t.Fatalf("worker count %d: AuthorizedVotesByPeer differs from worker count %d's result", w, workerCounts[0])
		}
		if !reflect.DeepEqual(verified.Signatures, baseline.Signatures) {
			t.Fatalf("worker count %d: Signatures differs from worker count %d's result", w, workerCounts[0])
		}
	}
}

// Sanity check that verifyTallySigTasksConcurrently itself never spawns
// more goroutines-worth of work than there are tasks, including the
// zero-task case (must not hang or panic on an empty jobs channel).
func TestVerifyTallySigTasksConcurrently_EmptyInput(t *testing.T) {
	results := verifyTallySigTasksConcurrently(nil, stage5ChainID, stage5Height, stage5BlockHash)
	if len(results) != 0 {
		t.Fatalf("expected 0 results for 0 tasks, got %d", len(results))
	}
}
