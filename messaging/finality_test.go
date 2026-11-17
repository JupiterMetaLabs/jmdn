package messaging

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
)

// finalityChain is a linked chain 0..tip where every block is certified
// unless listed in uncertified, and block n's PrevHash is block n-1's hash
// unless n is listed in unlinked.
type finalityChain struct {
	tip         uint64
	uncertified map[uint64]bool
	unlinked    map[uint64]bool
}

func hashAt(n uint64) common.Hash { return common.BigToHash(new(big.Int).SetUint64(n + 1000)) }

func (c finalityChain) install(t *testing.T) {
	t.Helper()
	prevBlock, prevVerify, prevTip := finalityBlockFn, finalityVerifyFn, finalityTipFn
	t.Cleanup(func() {
		finalityBlockFn, finalityVerifyFn, finalityTipFn = prevBlock, prevVerify, prevTip
		resetFinalityCaches()
	})
	resetFinalityCaches()
	finalityTipFn = func() (uint64, error) { return c.tip, nil }
	finalityBlockFn = func(n uint64) (*config.ZKBlock, error) {
		if n > c.tip {
			return nil, fmt.Errorf("block %d not found", n)
		}
		b := &config.ZKBlock{BlockNumber: n, BlockHash: hashAt(n)}
		if n > 0 {
			b.PrevHash = hashAt(n - 1)
			if c.unlinked[n] {
				b.PrevHash = common.HexToHash("0xbad")
			}
		}
		return b, nil
	}
	finalityVerifyFn = func(b *config.ZKBlock) (CertificateResult, error) {
		if c.uncertified[b.BlockNumber] {
			return CertificateResult{CommitteeSize: 4, Threshold: 3, YesVotes: 2}, nil
		}
		return CertificateResult{CommitteeSize: 4, Threshold: 3, YesVotes: 3, Reached: true}, nil
	}
}

func TestFinalizedHeight(t *testing.T) {
	cases := []struct {
		name  string
		chain finalityChain
		want  uint64
		none  bool
	}{
		{"all certified: tip-1", finalityChain{tip: 10}, 9, false},
		{"tip uncertified: tip-2", finalityChain{tip: 10, uncertified: map[uint64]bool{10: true}}, 8, false},
		{"tip-1 uncertified: its parent has no certified child", finalityChain{tip: 10, uncertified: map[uint64]bool{9: true}}, 7, false},
		{"tip does not link to tip-1", finalityChain{tip: 10, unlinked: map[uint64]bool{10: true}}, 8, false},
		{"only the tip exists", finalityChain{tip: 0}, 0, true},
		{"nothing certified", finalityChain{tip: 5, uncertified: map[uint64]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.chain.install(t)
			got, err := FinalizedHeight(context.Background())
			if c.none {
				if !errors.Is(err, ErrNotFinalized) {
					t.Fatalf("want ErrNotFinalized, got %d, %v", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %d, %v; want %d", got, err, c.want)
			}
		})
	}
}

// A block without a verified certificate is never finalized, even with a
// certified child; a certified block is not finalized until its child is.
func TestIsFinalized(t *testing.T) {
	finalityChain{tip: 10, uncertified: map[uint64]bool{5: true}}.install(t)
	for h, want := range map[uint64]bool{4: false, 5: false, 6: true, 9: true, 10: false} {
		got, err := IsFinalized(h)
		if err != nil || got != want {
			t.Errorf("IsFinalized(%d) = %v, %v; want %v", h, got, err, want)
		}
	}
}

// The strict verifier refuses what it cannot check against an anchored
// committee instead of relaxing.
func TestVerifyFinalityCertificateFailsClosed(t *testing.T) {
	if _, err := VerifyFinalityCertificate(&config.ZKBlock{BlockNumber: 7}); !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("no certificate: got %v", err)
	}
	prev := CommitteeV2Enabled
	t.Cleanup(func() { CommitteeV2Enabled = prev })
	CommitteeV2Enabled = false
	if _, err := VerifyFinalityCertificate(&config.ZKBlock{BlockNumber: 7, CommitteeCertificate: "[]"}); !errors.Is(err, ErrFinalityNotAnchored) {
		t.Fatalf("committee-v2 off: got %v", err)
	}
}

// Polling "finalized" at an unchanged tip verifies nothing again, and a
// verified block is not re-verified when the tip moves.
func TestFinalizedHeight_Cached(t *testing.T) {
	c := finalityChain{tip: 10}
	c.install(t)
	var calls int
	inner := finalityVerifyFn
	finalityVerifyFn = func(b *config.ZKBlock) (CertificateResult, error) {
		calls++
		return inner(b)
	}
	if _, err := FinalizedHeight(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := calls
	if first != 2 {
		t.Fatalf("first call verified %d blocks, want 2", first)
	}
	if _, err := FinalizedHeight(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != first {
		t.Fatalf("same tip re-verified: %d calls after %d", calls, first)
	}
	c.tip = 11
	finalityTipFn = func() (uint64, error) { return 11, nil }
	prevBlock := finalityBlockFn
	finalityBlockFn = func(n uint64) (*config.ZKBlock, error) {
		if n == 11 {
			return &config.ZKBlock{BlockNumber: 11, BlockHash: hashAt(11), PrevHash: hashAt(10)}, nil
		}
		return prevBlock(n)
	}
	got, err := FinalizedHeight(context.Background())
	if err != nil || got != 10 {
		t.Fatalf("got %d, %v; want 10", got, err)
	}
	if calls != first+1 {
		t.Fatalf("new tip verified %d blocks, want 1 (the new block only)", calls-first)
	}
}

// A cancelled request stops the search.
func TestFinalizedHeight_HonoursContext(t *testing.T) {
	finalityChain{tip: 10, uncertified: map[uint64]bool{10: true, 9: true, 8: true}}.install(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FinalizedHeight(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// resetFinalityCaches drops both caches (tests swap the seams underneath them).
func resetFinalityCaches() {
	certifiedCache.Lock()
	certifiedCache.m = make(map[certKey]CertificateResult)
	certifiedCache.Unlock()
	finalizedAtTip.Lock()
	finalizedAtTip.valid = false
	finalizedAtTip.Unlock()
}
