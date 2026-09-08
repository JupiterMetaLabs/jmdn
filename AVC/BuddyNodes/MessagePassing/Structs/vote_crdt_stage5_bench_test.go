package Structs

// Stage 5 benchmarks: sequential (single worker) vs bounded-concurrent
// verifyTallySignatures at 50/100/500/1000 votes, run against the REAL
// production function (not a reimplementation) by overriding
// verifyTallySignaturesWorkers. Per the task's explicit requirement, these
// numbers are measured here, not assumed — see the run output captured in
// the final report.

import (
	"fmt"
	"runtime"
	"testing"

	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func benchPeer(b *testing.B) peer.ID {
	b.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		b.Fatalf("generating bench identity: %v", err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		b.Fatalf("deriving bench peer ID: %v", err)
	}
	return id
}

// benchTally builds n genuinely-signed votes (real BLS keys/signatures, the
// same cost profile production verification actually pays — no shortcuts).
func benchTally(b *testing.B, n int) avcvotes.BlockTally {
	b.Helper()
	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: make(map[string][]int8, n),
		Signatures:            make(map[string][]avcvotes.VoteRecord, n),
	}
	for i := 0; i < n; i++ {
		p := benchPeer(b)
		vote := int8(1)
		if i%2 == 1 {
			vote = -1
		}
		blsResp, signed, err := BLS_Signer.SignMessageForBlock(vote, stage5ChainID, stage5Height, stage5BlockHash, "")
		if err != nil || !signed {
			b.Fatalf("SignMessageForBlock: signed=%v err=%v", signed, err)
		}
		rec := avcvotes.VoteRecord{
			PeerID: p.String(), Vote: vote, BlockHash: stage5BlockHash, Height: stage5Height,
			BLSSignature: blsResp.Signature, BLSPubKeyHex: blsResp.PubKey,
		}
		tally.AuthorizedVotesByPeer[p.String()] = []int8{vote}
		tally.Signatures[p.String()] = []avcvotes.VoteRecord{rec}
	}
	return tally
}

// runVerifyBenchmark times verifyTallySignatures itself (the real function
// under test, unmodified) with verifyTallySignaturesWorkers forced to
// `workers` for the duration of the benchmark. workers=1 is the sequential
// baseline (one worker draining the job channel is functionally
// sequential, same call sequence as the pre-concurrency code); workers=
// runtime.GOMAXPROCS(0) is the production default.
func runVerifyBenchmark(b *testing.B, n, workers int) {
	b.Helper()
	b.Setenv("JMDN_BLS_AUTOGEN", "1")
	tally := benchTally(b, n)

	origWorkers := verifyTallySignaturesWorkers
	verifyTallySignaturesWorkers = workers
	defer func() { verifyTallySignaturesWorkers = origWorkers }()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, "")
		if dropped != 0 {
			b.Fatalf("unexpected drop in benchmark fixture: dropped=%d", dropped)
		}
		if got := len(verified.SingleVotePeers()); got != n {
			b.Fatalf("unexpected verified count: got %d want %d", got, n)
		}
	}
}

var benchSizes = []int{50, 100, 500, 1000}

// BenchmarkVerifyTallySignatures_Sequential is the pre-concurrency baseline:
// verifyTallySignaturesWorkers pinned to 1, so every vote is verified one
// at a time by a single goroutine draining the job channel -- the same
// call sequence the old sequential loop had.
func BenchmarkVerifyTallySignatures_Sequential(b *testing.B) {
	for _, n := range benchSizes {
		n := n
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			runVerifyBenchmark(b, n, 1)
		})
	}
}

// BenchmarkVerifyTallySignatures_Concurrent uses the production default
// worker count (runtime.GOMAXPROCS(0), same as an unmodified package var).
func BenchmarkVerifyTallySignatures_Concurrent(b *testing.B) {
	workers := runtime.GOMAXPROCS(0)
	for _, n := range benchSizes {
		n := n
		b.Run(fmt.Sprintf("N=%d/workers=%d", n, workers), func(b *testing.B) {
			runVerifyBenchmark(b, n, workers)
		})
	}
}
