package Security

// D-28: the consensus binding must identify a block's POSITION in the chain,
// not only its contents.
//
// The failure being closed here: ConsensusHash covered Slot, Period, the
// entropy fields and the transaction list — but neither the parent nor the
// height. Two blocks on different forks at the same height, with the same
// transactions, the same slot and the same period therefore produced the SAME
// ConsensusHash. BlockHash is transactions-only, so it collided too, which
// means the committee's v4 vote message
//
//	…:h=<height>:<BlockHash>:ch=<ConsensusHash>:<vote>
//
// was byte-identical for both blocks. A certificate collected for one fork
// verified against the other, and checkEquivocation — which keys on BlockHash
// — saw one block rather than two.

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
)

// forkPair returns two blocks that differ ONLY in their parent: same height,
// same slot, same period, same transactions, same entropy fields.
func forkPair() (*config.ZKBlock, *config.ZKBlock) {
	a := sampleBlock()
	a.BlockNumber = 500
	a.PrevHash = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	b := sampleBlock()
	b.BlockNumber = 500
	b.PrevHash = common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	return a, b
}

func TestForkAtOneHeightDoesNotShareAConsensusHash(t *testing.T) {
	restore := ConsensusHashV3Enabled
	ConsensusHashV3Enabled = true
	defer func() { ConsensusHashV3Enabled = restore }()

	a, b := forkPair()
	if RecomputeBlockHashWithConsensusFields(a) == RecomputeBlockHashWithConsensusFields(b) {
		t.Fatal("two blocks at height 500 with the same transactions but DIFFERENT PARENTS " +
			"share a ConsensusHash — a committee certificate for one is valid for the other")
	}
}

func TestSameParentDifferentHeightDoesNotShareAConsensusHash(t *testing.T) {
	restore := ConsensusHashV3Enabled
	ConsensusHashV3Enabled = true
	defer func() { ConsensusHashV3Enabled = restore }()

	a := sampleBlock()
	a.BlockNumber = 500
	a.PrevHash = common.HexToHash("0xaa")

	b := sampleBlock()
	b.BlockNumber = 501
	b.PrevHash = common.HexToHash("0xaa")

	if RecomputeBlockHashWithConsensusFields(a) == RecomputeBlockHashWithConsensusFields(b) {
		t.Fatal("blocks at heights 500 and 501 share a ConsensusHash — height is not bound")
	}
}

func TestV3StillBindsEveryFieldV2Bound(t *testing.T) {
	// Adding two fields must not drop any. Each mutation must change the hash.
	restore := ConsensusHashV3Enabled
	ConsensusHashV3Enabled = true
	defer func() { ConsensusHashV3Enabled = restore }()

	base := RecomputeBlockHashWithConsensusFields(sampleBlock())

	mutations := map[string]func(*config.ZKBlock){
		"Slot":                  func(b *config.ZKBlock) { b.Slot++ },
		"Period":                func(b *config.ZKBlock) { b.Period++ },
		"SeedEpoch":             func(b *config.ZKBlock) { b.SeedEpoch++ },
		"VotingSnapshotEpoch":   func(b *config.ZKBlock) { b.VotingSnapshotEpoch++ },
		"VdfProof":              func(b *config.ZKBlock) { b.VdfProof = []byte("tampered") },
		"RandaoReveals":         func(b *config.ZKBlock) { b.RandaoReveals[0].Secret = []byte("tampered") },
		"CommitteeSnapshotHash": func(b *config.ZKBlock) { b.CommitteeSnapshotHash = []byte("tampered") },
		"BlockNumber":           func(b *config.ZKBlock) { b.BlockNumber++ },
		"PrevHash":              func(b *config.ZKBlock) { b.PrevHash = common.HexToHash("0xdead") },
	}
	for name, mutate := range mutations {
		blk := sampleBlock()
		mutate(blk)
		if RecomputeBlockHashWithConsensusFields(blk) == base {
			t.Fatalf("mutating %s did not change the ConsensusHash — that field is not bound", name)
		}
	}
}

func TestV3IsDeterministic(t *testing.T) {
	restore := ConsensusHashV3Enabled
	ConsensusHashV3Enabled = true
	defer func() { ConsensusHashV3Enabled = restore }()

	a, _ := forkPair()
	first := RecomputeBlockHashWithConsensusFields(a)
	for i := 0; i < 50; i++ {
		if RecomputeBlockHashWithConsensusFields(a) != first {
			t.Fatal("ConsensusHash is not deterministic — every node must derive the same value")
		}
	}
}

func TestGenesisShapedBlockStillHashes(t *testing.T) {
	// Block 0 has a zero parent. That must hash normally, not be treated as a
	// missing field, and must not collide with a non-genesis block.
	restore := ConsensusHashV3Enabled
	ConsensusHashV3Enabled = true
	defer func() { ConsensusHashV3Enabled = restore }()

	genesis := sampleBlock()
	genesis.BlockNumber = 0
	genesis.PrevHash = common.Hash{}

	h := RecomputeBlockHashWithConsensusFields(genesis)
	if h == (common.Hash{}) {
		t.Fatal("a genesis-shaped block hashed to the zero hash; ConsensusHashHex would then " +
			"report it as absent and the vote would silently fall back to v3 (block-hash only)")
	}

	next := sampleBlock()
	next.BlockNumber = 1
	next.PrevHash = common.HexToHash("0xabc")
	if RecomputeBlockHashWithConsensusFields(next) == h {
		t.Fatal("genesis and block 1 share a ConsensusHash")
	}
}

func TestFlagOffPreservesTheExistingPreimageExactly(t *testing.T) {
	// The rollout guarantee. With the flag off, behaviour must be byte-identical
	// to before this change, so a binary carrying the fix can be deployed to the
	// whole fleet before anyone flips it.
	restore := ConsensusHashV3Enabled
	ConsensusHashV3Enabled = false
	defer func() { ConsensusHashV3Enabled = restore }()

	a, b := forkPair()
	if RecomputeBlockHashWithConsensusFields(a) != RecomputeBlockHashWithConsensusFields(b) {
		t.Fatal("with the flag OFF the parent must NOT be bound — otherwise deploying this " +
			"binary changes consensus before the fleet-wide flip, and upgraded nodes reject " +
			"un-upgraded ones with consensus_hash_mismatch")
	}
}

func TestV2AndV3NeverProduceTheSameDigest(t *testing.T) {
	// Domain separation. Without a domain bump the two preimages would claim the
	// same version, and a mixed fleet would fail with no way to tell which side
	// was wrong.
	a, _ := forkPair()

	ConsensusHashV3Enabled = false
	v2 := RecomputeBlockHashWithConsensusFields(a)
	ConsensusHashV3Enabled = true
	v3 := RecomputeBlockHashWithConsensusFields(a)
	ConsensusHashV3Enabled = false

	if v2 == v3 {
		t.Fatal("v2 and v3 produced the same digest for the same block")
	}
}
