package adapters

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/JupiterMetaLabs/avc/interfaces"
	"github.com/JupiterMetaLabs/avc/validation"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/config"
)

// These tests exercise the REAL jmdn validation logic through the checkers:
// real EIP-1559 signatures (via go-ethereum, jmdn's own test pattern) and a
// real SecurityCache populated in-memory with RegisterAccount (no DB). They
// prove Phase 1 (signature/chainID/hash/value) and Phase 2 (balance/nonce/
// address) actually verify — not just that the plumbing carries data.

var checkerChainID = big.NewInt(1337)

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

// signedTxTo builds a config.Transaction with a real EIP-1559 signature from
// key, to a given receiver, with a given nonce and value. Mirrors jmdn's own
// signedTx helper (messaging/blockPropagation_test.go).
func signedTxTo(t *testing.T, key *ecdsa.PrivateKey, to common.Address, nonce uint64, value int64) config.Transaction {
	t.Helper()
	from := crypto.PubkeyToAddress(key.PublicKey)
	inner := &gethtypes.DynamicFeeTx{
		ChainID: checkerChainID, Nonce: nonce, To: &to, Value: big.NewInt(value),
		GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(1), Gas: 21000,
	}
	signed, err := gethtypes.SignNewTx(key, gethtypes.LatestSignerForChainID(checkerChainID), inner)
	if err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	v, r, s := signed.RawSignatureValues()
	return config.Transaction{
		Hash: signed.Hash(), From: &from, To: &to, Value: big.NewInt(value),
		Type: gethtypes.DynamicFeeTxType, ChainID: checkerChainID, Nonce: nonce, GasLimit: 21000,
		MaxFee: big.NewInt(1), MaxPriorityFee: big.NewInt(1), V: v, R: r, S: s,
	}
}

func iface(tx config.Transaction) interfaces.Transaction { return txAdapter{tx: tx} }

func fundedCache(entries map[common.Address]struct {
	balance string
	nonce   uint64
}) *Security.SecurityCache {
	c := Security.NewSecurityCache()
	for addr, e := range entries {
		c.RegisterAccount(addr, &DB_OPs.Account{Address: addr, Balance: e.balance, TxNonce: e.nonce})
	}
	return c
}

// --- StatelessChecker --------------------------------------------------------

func TestStatelessChecker_ValidSignedTxPasses(t *testing.T) {
	sc, err := NewStatelessChecker(checkerChainID)
	if err != nil {
		t.Fatalf("new checker: %v", err)
	}
	tx := signedTxTo(t, newKey(t), common.HexToAddress("0x00000000000000000000000000000000000000ff"), 0, 1)
	if err := sc.CheckTx(context.Background(), iface(tx)); err != nil {
		t.Fatalf("a validly-signed tx must pass Phase 1, got: %v", err)
	}
}

func TestStatelessChecker_WrongChainIDFails(t *testing.T) {
	sc, _ := NewStatelessChecker(checkerChainID)
	tx := signedTxTo(t, newKey(t), common.HexToAddress("0x00000000000000000000000000000000000000ff"), 0, 1)
	tx.ChainID = big.NewInt(9999) // does not match the checker's configured chain id
	if err := sc.CheckTx(context.Background(), iface(tx)); err == nil {
		t.Fatal("a tx on the wrong chain id must be rejected")
	}
}

func TestStatelessChecker_TamperedHashFails(t *testing.T) {
	sc, _ := NewStatelessChecker(checkerChainID)
	tx := signedTxTo(t, newKey(t), common.HexToAddress("0x00000000000000000000000000000000000000ff"), 0, 1)
	tx.Hash = common.HexToHash("0xdeadbeef") // no longer matches the content hash
	if err := sc.CheckTx(context.Background(), iface(tx)); err == nil {
		t.Fatal("a tx whose Hash does not match its contents must be rejected")
	}
}

func TestStatelessChecker_BadSignatureFails(t *testing.T) {
	sc, _ := NewStatelessChecker(checkerChainID)
	tx := signedTxTo(t, newKey(t), common.HexToAddress("0x00000000000000000000000000000000000000ff"), 0, 1)
	// Corrupt the sender so the recovered signer no longer matches From.
	other := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	tx.From = &other
	if err := sc.CheckTx(context.Background(), iface(tx)); err == nil {
		t.Fatal("a tx whose signature does not recover to From must be rejected")
	}
}

func TestStatelessChecker_NegativeValueFails(t *testing.T) {
	sc, _ := NewStatelessChecker(checkerChainID)
	tx := signedTxTo(t, newKey(t), common.HexToAddress("0x00000000000000000000000000000000000000ff"), 0, 1)
	tx.Value = big.NewInt(-5) // value gate must reject
	if err := sc.CheckTx(context.Background(), iface(tx)); err == nil {
		t.Fatal("a tx with a negative value must be rejected")
	}
}

func TestStatelessChecker_NonJmdnBackedFailsClosed(t *testing.T) {
	sc, _ := NewStatelessChecker(checkerChainID)
	if err := sc.CheckTx(context.Background(), foreignTx{}); err == nil {
		t.Fatal("a non-jmdn-backed transaction must be rejected fail-closed")
	}
}

func TestNewStatelessChecker_RejectsZeroChainID(t *testing.T) {
	if _, err := NewStatelessChecker(big.NewInt(0)); err == nil {
		t.Fatal("chain id 0 must be refused")
	}
	if _, err := NewStatelessChecker(nil); err == nil {
		t.Fatal("nil chain id must be refused")
	}
}

// --- StatefulChecker ---------------------------------------------------------

func TestStatefulChecker_SufficientFundsPassesAndApplies(t *testing.T) {
	key := newKey(t)
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	cache := fundedCache(map[common.Address]struct {
		balance string
		nonce   uint64
	}{
		from: {"1000000000000000000", 0},
		to:   {"0", 0},
	})
	sfc, err := NewStatefulChecker(cache)
	if err != nil {
		t.Fatalf("new checker: %v", err)
	}
	tx := signedTxTo(t, key, to, 0, 100)
	if err := sfc.CheckAndApply(context.Background(), iface(tx)); err != nil {
		t.Fatalf("a funded, correct-nonce tx must pass Phase 2, got: %v", err)
	}
	// nonce advanced
	if got := cache.GetTxNonce(from); got != 1 {
		t.Fatalf("nonce must advance to 1, got %d", got)
	}
	// receiver credited by value (100)
	if bal := cache.GetAccount(to).Balance; bal != "100" {
		t.Fatalf("receiver must be credited 100, got %s", bal)
	}
}

func TestStatefulChecker_InsufficientFundsFails(t *testing.T) {
	key := newKey(t)
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	cache := fundedCache(map[common.Address]struct {
		balance string
		nonce   uint64
	}{
		from: {"5", 0}, // nowhere near value+gas
		to:   {"0", 0},
	})
	sfc, _ := NewStatefulChecker(cache)
	tx := signedTxTo(t, key, to, 0, 100)
	if err := sfc.CheckAndApply(context.Background(), iface(tx)); err == nil {
		t.Fatal("an underfunded tx must be rejected")
	}
}

func TestStatefulChecker_StaleNonceFails(t *testing.T) {
	key := newKey(t)
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	cache := fundedCache(map[common.Address]struct {
		balance string
		nonce   uint64
	}{
		from: {"1000000000000000000", 5}, // account already at nonce 5
		to:   {"0", 0},
	})
	sfc, _ := NewStatefulChecker(cache)
	tx := signedTxTo(t, key, to, 3, 1) // nonce 3 < expected 5
	if err := sfc.CheckAndApply(context.Background(), iface(tx)); err == nil {
		t.Fatal("a stale (too-low) nonce must be rejected")
	}
}

func TestStatefulChecker_MissingSenderFails(t *testing.T) {
	key := newKey(t)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	// Only the receiver is registered; sender absent.
	cache := fundedCache(map[common.Address]struct {
		balance string
		nonce   uint64
	}{
		to: {"0", 0},
	})
	sfc, _ := NewStatefulChecker(cache)
	tx := signedTxTo(t, key, to, 0, 1)
	if err := sfc.CheckAndApply(context.Background(), iface(tx)); err == nil {
		t.Fatal("a tx from an unknown sender must be rejected")
	}
}

// TestStatefulChecker_IntraBlockDoubleSpendCaught proves the mutating cache
// stops a sender spending the same balance twice within one block: fund for
// exactly one transfer, then two transfers must not both pass.
func TestStatefulChecker_IntraBlockDoubleSpendCaught(t *testing.T) {
	key := newKey(t)
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	// gas per tx = 21000*1 = 21000; value 1 → cost 21001. Fund ~1.5x one tx.
	cache := fundedCache(map[common.Address]struct {
		balance string
		nonce   uint64
	}{
		from: {"30000", 0},
		to:   {"0", 0},
	})
	sfc, _ := NewStatefulChecker(cache)

	first := signedTxTo(t, key, to, 0, 1)
	if err := sfc.CheckAndApply(context.Background(), iface(first)); err != nil {
		t.Fatalf("first transfer should pass, got: %v", err)
	}
	second := signedTxTo(t, key, to, 1, 1)
	if err := sfc.CheckAndApply(context.Background(), iface(second)); err == nil {
		t.Fatal("second transfer must fail — balance was already spent by the first (double-spend guard)")
	}
}

func TestStatefulChecker_NonJmdnBackedFailsClosed(t *testing.T) {
	cache := Security.NewSecurityCache()
	sfc, _ := NewStatefulChecker(cache)
	if err := sfc.CheckAndApply(context.Background(), foreignTx{}); err == nil {
		t.Fatal("a non-jmdn-backed transaction must be rejected fail-closed")
	}
}

func TestNewStatefulChecker_RejectsNilCache(t *testing.T) {
	if _, err := NewStatefulChecker(nil); err == nil {
		t.Fatal("a nil cache must be refused")
	}
}

// foreignTx is an interfaces.Transaction that is NOT jmdn-backed, to prove the
// checkers fail closed rather than mis-handling an unexpected implementation.
type foreignTx struct{}

func (foreignTx) TxHashBytes() []byte { return make([]byte, 32) }

// --- Full end-to-end through FullValidator with REAL checkers ----------------

// TestEndToEnd_RealBlockRealCheckersApproves runs a real block of real signed
// transactions through avc's FullValidator at DepthFull with BOTH real jmdn
// checkers and a funded cache — the complete buddy-node validation path.
func TestEndToEnd_RealBlockRealCheckersApproves(t *testing.T) {
	key := newKey(t)
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")

	txs := []config.Transaction{
		signedTxTo(t, key, to, 0, 1),
		signedTxTo(t, key, to, 1, 1),
	}
	blk := realBlock(txs)
	ad := NewZKBlockAdapter(blk)

	sc, _ := NewStatelessChecker(checkerChainID)
	cache := fundedCache(map[common.Address]struct {
		balance string
		nonce   uint64
	}{
		from: {"1000000000000000000", 0},
		to:   {"0", 0},
	})
	sfc, _ := NewStatefulChecker(cache)

	v := validation.NewFullValidator(sc, sfc, 0)
	verdict, err := v.ValidateBlock(ad, interfaces.DepthFull)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Accept {
		t.Fatalf("a real, fully-valid block must be approved end-to-end, got reject: %s / %s",
			verdict.Reason, verdict.Detail)
	}
}

// TestEndToEnd_UnderfundedBlockRejected: same path, but the sender cannot
// afford both transfers — the full validator must veto (Phase 2 stateful).
func TestEndToEnd_UnderfundedBlockRejected(t *testing.T) {
	key := newKey(t)
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")

	txs := []config.Transaction{
		signedTxTo(t, key, to, 0, 1),
		signedTxTo(t, key, to, 1, 1),
	}
	blk := realBlock(txs)
	ad := NewZKBlockAdapter(blk)

	sc, _ := NewStatelessChecker(checkerChainID)
	cache := fundedCache(map[common.Address]struct {
		balance string
		nonce   uint64
	}{
		from: {"25000", 0}, // enough for one transfer (~21001), not two
		to:   {"0", 0},
	})
	sfc, _ := NewStatefulChecker(cache)

	v := validation.NewFullValidator(sc, sfc, 0)
	verdict, err := v.ValidateBlock(ad, interfaces.DepthFull)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Accept {
		t.Fatal("a block whose sender cannot afford all its transfers must be rejected end-to-end")
	}
	if verdict.Reason != interfaces.ReasonStatefulCheckFailed {
		t.Fatalf("expected a stateful-check rejection, got %s", verdict.Reason)
	}
}
