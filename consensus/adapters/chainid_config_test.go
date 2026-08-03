package adapters

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestChainIDConfig_ResolveMainnet(t *testing.T) {
	cfg := ChainIDConfig{Network: NetworkMainnet, MainnetChainID: big.NewInt(9001), TestnetChainID: big.NewInt(9002)}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cmp(big.NewInt(9001)) != 0 {
		t.Fatalf("mainnet resolve = %v, want 9001", got)
	}
}

func TestChainIDConfig_ResolveTestnet(t *testing.T) {
	cfg := ChainIDConfig{Network: NetworkTestnet, MainnetChainID: big.NewInt(9001), TestnetChainID: big.NewInt(9002)}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cmp(big.NewInt(9002)) != 0 {
		t.Fatalf("testnet resolve = %v, want 9002", got)
	}
}

// TestChainIDConfig_MainnetPicksMainnetNotTestnet is the case that matters
// most: prove selecting mainnet never accidentally returns the testnet value
// (or vice versa) — a network mix-up here would let a node validate mainnet
// blocks against a testnet chain id, silently accepting/rejecting the wrong
// signatures.
func TestChainIDConfig_MainnetPicksMainnetNotTestnet(t *testing.T) {
	cfg := ChainIDConfig{Network: NetworkMainnet, MainnetChainID: big.NewInt(111), TestnetChainID: big.NewInt(222)}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cmp(big.NewInt(222)) == 0 {
		t.Fatal("mainnet resolved to the testnet value — network selection is broken")
	}
	if got.Cmp(big.NewInt(111)) != 0 {
		t.Fatalf("mainnet resolve = %v, want 111", got)
	}
}

func TestChainIDConfig_UnknownNetworkFailsClosed(t *testing.T) {
	cfg := ChainIDConfig{Network: Network("devnet"), MainnetChainID: big.NewInt(1), TestnetChainID: big.NewInt(2)}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("an unrecognized network must be refused, not silently resolved")
	}
}

func TestChainIDConfig_EmptyNetworkFailsClosed(t *testing.T) {
	cfg := ChainIDConfig{MainnetChainID: big.NewInt(1), TestnetChainID: big.NewInt(2)}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("an empty network must be refused")
	}
}

func TestChainIDConfig_MainnetWithNilChainIDFailsClosed(t *testing.T) {
	cfg := ChainIDConfig{Network: NetworkMainnet, TestnetChainID: big.NewInt(2)}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("mainnet selected with no MainnetChainID set must be refused, not default to zero")
	}
}

func TestChainIDConfig_TestnetWithNilChainIDFailsClosed(t *testing.T) {
	cfg := ChainIDConfig{Network: NetworkTestnet, MainnetChainID: big.NewInt(1)}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("testnet selected with no TestnetChainID set must be refused")
	}
}

func TestChainIDConfig_ZeroChainIDFailsClosed(t *testing.T) {
	cfg := ChainIDConfig{Network: NetworkMainnet, MainnetChainID: big.NewInt(0), TestnetChainID: big.NewInt(2)}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("a zero chain id must be refused, same as nil")
	}
}

func TestChainIDConfig_NegativeChainIDFailsClosed(t *testing.T) {
	cfg := ChainIDConfig{Network: NetworkMainnet, MainnetChainID: big.NewInt(-5), TestnetChainID: big.NewInt(2)}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("a negative chain id must be refused")
	}
}

// TestNewStatelessCheckerForNetwork_EndToEnd proves the config actually
// drives real signature verification: a tx signed for the mainnet chain id
// passes under NetworkMainnet and is rejected under NetworkTestnet (using the
// SAME config, just switching Network) — the selector genuinely changes which
// chain id transactions are checked against.
func TestNewStatelessCheckerForNetwork_EndToEnd(t *testing.T) {
	mainnetID := big.NewInt(555001)
	testnetID := big.NewInt(555002)
	cfg := ChainIDConfig{MainnetChainID: mainnetID, TestnetChainID: testnetID}

	key := newKey(t)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")

	// Sign a tx for the mainnet chain id specifically (not the package-level
	// checkerChainID used elsewhere in this package's tests).
	savedChainID := checkerChainID
	checkerChainID = mainnetID
	tx := signedTxTo(t, key, to, 0, 1)
	checkerChainID = savedChainID

	cfg.Network = NetworkMainnet
	mainnetChecker, err := NewStatelessCheckerForNetwork(cfg)
	if err != nil {
		t.Fatalf("build mainnet checker: %v", err)
	}
	if err := mainnetChecker.CheckTx(context.Background(), iface(tx)); err != nil {
		t.Fatalf("a tx signed for the mainnet chain id must pass under NetworkMainnet, got: %v", err)
	}

	cfg.Network = NetworkTestnet
	testnetChecker, err := NewStatelessCheckerForNetwork(cfg)
	if err != nil {
		t.Fatalf("build testnet checker: %v", err)
	}
	if err := testnetChecker.CheckTx(context.Background(), iface(tx)); err == nil {
		t.Fatal("a tx signed for the mainnet chain id must be REJECTED under NetworkTestnet (different chain id)")
	}
}
