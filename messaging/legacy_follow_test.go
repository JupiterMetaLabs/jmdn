package messaging

import (
	"context"
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/config"
	"gossipnode/config/settings"
)

func testPeerID(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// withLegacyFollow installs legacy follow settings pinned to main.
func withLegacyFollow(t *testing.T, enabled bool, main peer.ID, until uint64) {
	t.Helper()
	prev := legacyFollowSettingsFn
	legacyFollowSettingsFn = func() settings.LegacyFollowSettings {
		return settings.LegacyFollowSettings{
			Enabled:       enabled,
			MainSequencer: "/ip4/127.0.0.1/tcp/15000/p2p/" + main.String(),
			UntilHeight:   until,
		}
	}
	t.Cleanup(func() { legacyFollowSettingsFn = prev })
}

// mainBlock builds a block in main's format: signed transactions, BlockHash
// and TxnsRoot over them, no ConsensusHash, no certificate.
func mainBlock(t *testing.T, num uint64, txs ...config.Transaction) *config.ZKBlock {
	t.Helper()
	cb := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	zk := common.HexToAddress("0x00000000000000000000000000000000000000c2")
	return &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromTxs(txs),
		TxnsRoot:     RecomputeTxnsRoot(txs),
		BlockNumber:  num,
		Transactions: txs,
		CoinbaseAddr: &cb,
		ZKVMAddr:     &zk,
		FeeRecipients: []config.FeeRecipient{
			{Addr: common.HexToAddress("0x00000000000000000000000000000000000000f1"), Weight: 1},
		},
	}
}

func TestIsLegacyBlock(t *testing.T) {
	main := testPeerID(t)
	b := &config.ZKBlock{BlockNumber: 100}

	withLegacyFollow(t, false, main, 0)
	if IsLegacyBlock(b) {
		t.Fatal("legacy while disabled")
	}
	withLegacyFollow(t, true, main, 0)
	if !IsLegacyBlock(b) || IsLegacyBlock(nil) {
		t.Fatal("open-ended window: every block is legacy, nil is not")
	}
	withLegacyFollow(t, true, main, 100)
	if !IsLegacyBlock(b) || IsLegacyBlock(&config.ZKBlock{BlockNumber: 101}) {
		t.Fatal("window ends at until_height")
	}
}

func TestAuthorizeLegacySource(t *testing.T) {
	main, other := testPeerID(t), testPeerID(t)
	withLegacyFollow(t, true, main, 0)
	if !AuthorizeLegacySource(main.String()) || !AuthorizeLegacySource("gossip:"+main.String()) {
		t.Fatal("main sequencer (direct and gossip publisher) not authorized")
	}
	if AuthorizeLegacySource(other.String()) || AuthorizeLegacySource("") {
		t.Fatal("another peer authorized")
	}
	prev := legacyFollowSettingsFn
	legacyFollowSettingsFn = func() settings.LegacyFollowSettings {
		return settings.LegacyFollowSettings{Enabled: true, MainSequencer: "/ip4/127.0.0.1/tcp/1"}
	}
	defer func() { legacyFollowSettingsFn = prev }()
	if AuthorizeLegacySource(main.String()) {
		t.Fatal("authorized with a pin that has no /p2p peer id (must fail closed)")
	}
	if _, err := ParseLegacyMainSequencer("/ip4/127.0.0.1/tcp/1"); err == nil {
		t.Fatal("pin without /p2p accepted")
	}
}

// A main-format block (no ConsensusHash, no certificate) passes the legacy
// gate; the v3 gate rejects the same block.
func TestValidateLegacyBlock_AcceptsMainFormat(t *testing.T) {
	resetEquivocation()
	withLegacyFollow(t, true, testPeerID(t), 0)
	key, _ := crypto.GenerateKey()
	b := mainBlock(t, 50, signedTx(t, key, 0), signedTx(t, key, 1))

	if rej := validateLegacyBlock(context.Background(), b); rej != nil {
		t.Fatalf("legacy gate rejected a main-format block: %s %v", rej.reason, rej.err)
	}
	resetEquivocation()
	if rej := validateRemoteBlock(context.Background(), config.BlockMessage{Block: b}); rej == nil {
		t.Fatal("v3 gate accepted a block without ConsensusHash or certificate")
	}
}

func TestValidateLegacyBlock_Rejects(t *testing.T) {
	withLegacyFollow(t, true, testPeerID(t), 0)
	key, _ := crypto.GenerateKey()
	ctx := context.Background()

	t.Run("bad signature", func(t *testing.T) {
		resetEquivocation()
		tx := signedTx(t, key, 0)
		tx.R = new(big.Int).Add(tx.R, big.NewInt(1))
		if rej := validateLegacyBlock(ctx, mainBlock(t, 60, tx)); rej == nil || rej.reason != "bad_signature" {
			t.Fatalf("got %v", rej)
		}
	})
	t.Run("body binding even with EnforceBodyBinding off", func(t *testing.T) {
		resetEquivocation()
		prev := EnforceBodyBinding
		EnforceBodyBinding = false
		defer func() { EnforceBodyBinding = prev }()
		b := mainBlock(t, 61, signedTx(t, key, 0))
		b.BlockHash = common.HexToHash("0xbad")
		if rej := validateLegacyBlock(ctx, b); rej == nil {
			t.Fatal("block hash not bound to its transactions")
		}
	})
	t.Run("missing fee addresses", func(t *testing.T) {
		resetEquivocation()
		b := mainBlock(t, 62, signedTx(t, key, 0))
		b.ZKVMAddr = nil
		if rej := validateLegacyBlock(ctx, b); rej == nil || rej.reason != "missing_fee_addresses" {
			t.Fatalf("got %v", rej)
		}
	})
	t.Run("contract creation", func(t *testing.T) {
		resetEquivocation()
		tx := signedTx(t, key, 0)
		tx.To = nil
		if rej := validateLegacyBlock(ctx, mainBlock(t, 64, tx)); rej == nil || rej.reason != "legacy_contract_creation" {
			t.Fatalf("got %v", rej)
		}
	})
	t.Run("equivocation by block hash", func(t *testing.T) {
		resetEquivocation()
		if rej := validateLegacyBlock(ctx, mainBlock(t, 63, signedTx(t, key, 0))); rej != nil {
			t.Fatalf("first block: %v", rej.err)
		}
		if rej := validateLegacyBlock(ctx, mainBlock(t, 63, signedTx(t, key, 1))); rej == nil || rej.reason != "equivocation" {
			t.Fatalf("second block at the same height: got %v", rej)
		}
	})
}

// A height gap on the live path wakes the legacy catch-up loop.
func TestValidateLegacyBlock_GapNudgesCatchUp(t *testing.T) {
	resetEquivocation()
	withLegacyFollow(t, true, testPeerID(t), 0)
	prevLink, prevEnforce := legacyLinkageFn, EnforceBlockLinkage
	EnforceBlockLinkage = true
	legacyLinkageFn = func(context.Context, *config.ZKBlock) *blockRejection {
		return reject("height_gap", "gap")
	}
	nudged := 0
	SetLegacyCatchUpNudge(func() { nudged++ })
	defer func() {
		legacyLinkageFn, EnforceBlockLinkage = prevLink, prevEnforce
		SetLegacyCatchUpNudge(nil)
	}()
	key, _ := crypto.GenerateKey()
	if rej := validateLegacyBlock(context.Background(), mainBlock(t, 70, signedTx(t, key, 0))); rej == nil || rej.reason != "height_gap" {
		t.Fatalf("got %v", rej)
	}
	if nudged != 1 {
		t.Fatalf("nudged %d times, want 1", nudged)
	}
}

func TestValidateLegacySyncedBlock(t *testing.T) {
	resetEquivocation()
	withLegacyFollow(t, true, testPeerID(t), 0)
	key, _ := crypto.GenerateKey()
	if err := ValidateLegacySyncedBlock(context.Background(), mainBlock(t, 80, signedTx(t, key, 0))); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLegacySyncedBlock(context.Background(), mainBlock(t, 80, signedTx(t, key, 1))); err == nil {
		t.Fatal("second block at the same height accepted")
	}
}
