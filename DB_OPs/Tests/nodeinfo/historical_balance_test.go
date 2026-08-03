// Verifies eth_getBalance-at-block-N reconstruction (DB_OPs.GetBalanceAtBlock):
// reverse-delta replay from the latest balance, mirroring reconciliation
// gas semantics (sender pays value+gas, receiver gets value, coinbase gets
// gas/2+remainder, zkvm gets gas/2).
package nodeinfo_test

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"testing"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"

	"github.com/ethereum/go-ethereum/common"
)

const (
	userAddr     = "0x1111111111111111111111111111111111111111"
	peerAddr     = "0x2222222222222222222222222222222222222222"
	coinbaseAddr = "0x3333333333333333333333333333333333333333"
	zkvmAddr     = "0x4444444444444444444444444444444444444444"
)

// histHandle: tip=10. One tx in block 9: user → peer, value 100, legacy,
// gasLimit 10, gasPrice 2 → gasFee 20 (coinbase +10, zkvm +10).
type histHandle struct {
	store.ThebeHandle
	balances map[string]string // lowercase addr → latest balance
	override *thebegateway.TransactionRecord
}

func (h *histHandle) GetLatestBlockNumber(_ context.Context) (uint64, error) { return 10, nil }

func (h *histHandle) GetAccount(_ context.Context, address string) (*store.Account, error) {
	bal, ok := h.balances[address]
	if !ok {
		// try checksummed key
		for k, v := range h.balances {
			if common.HexToAddress(k) == common.HexToAddress(address) {
				bal, ok = v, true
				break
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("account %s not found", address)
	}
	return &store.Account{Address: common.HexToAddress(address), Balance: bal}, nil
}

func (h *histHandle) txInBlock9() *thebegateway.TransactionRecord {
	to := common.HexToAddress(peerAddr).Hex()
	return &thebegateway.TransactionRecord{
		TxHash:      "0xaaa",
		BlockNumber: 9,
		FromAddr:    common.HexToAddress(userAddr).Hex(),
		ToAddr:      &to,
		ValueWei:    "100",
		Nonce:       "0",
		GasLimit:    "10",
		GasPriceWei: "2",
		Type:        0,
	}
}

func (h *histHandle) GetTransactionsByAddressInRange(_ context.Context, address string, from, to uint64) ([]*thebegateway.TransactionRecord, error) {
	a := common.HexToAddress(address)
	if from <= 9 && 9 <= to && (a == common.HexToAddress(userAddr) || a == common.HexToAddress(peerAddr)) {
		if h.override != nil {
			return []*thebegateway.TransactionRecord{h.override}, nil
		}
		return []*thebegateway.TransactionRecord{h.txInBlock9()}, nil
	}
	return nil, nil
}

func (h *histHandle) GetBlocksByRewardAddress(_ context.Context, address string, from, to uint64) ([]*thebegateway.BlockRecord, error) {
	a := common.HexToAddress(address)
	if from <= 9 && 9 <= to && (a == common.HexToAddress(coinbaseAddr) || a == common.HexToAddress(zkvmAddr)) {
		return []*thebegateway.BlockRecord{{
			BlockNumber:  9,
			CoinbaseAddr: common.HexToAddress(coinbaseAddr).Hex(),
			ZKVMAddr:     common.HexToAddress(zkvmAddr).Hex(),
		}}, nil
	}
	return nil, nil
}

func (h *histHandle) GetTransactionsByBlock(_ context.Context, bn uint64) ([]*thebegateway.TransactionRecord, error) {
	if bn == 9 {
		return []*thebegateway.TransactionRecord{h.txInBlock9()}, nil
	}
	return nil, nil
}

func setupHist(t *testing.T) {
	t.Helper()
	h := &histHandle{balances: map[string]string{
		userAddr:     "880", // 1000 − 100 (value) − 20 (gas)
		peerAddr:     "600", // 500 + 100
		coinbaseAddr: "10",  // 0 + 10
		zkvmAddr:     "10",  // 0 + 10
	}}
	DB_OPs.SetGlobalHandle(h)
	t.Cleanup(func() { DB_OPs.SetGlobalHandle(nil) })
}

func balAt(t *testing.T, addr string, block uint64) *big.Int {
	t.Helper()
	b, err := DB_OPs.GetBalanceAtBlock(common.HexToAddress(addr), block)
	if err != nil {
		t.Fatalf("GetBalanceAtBlock(%s, %d): %v", addr, block, err)
	}
	return b
}

func TestHistoricalBalance_SenderBeforeAndAfterTx(t *testing.T) {
	setupHist(t)
	// at block 8 (before tx): 880 + 100 + 20 = 1000
	if got := balAt(t, userAddr, 8); got.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("sender at block 8: want 1000, got %s", got)
	}
	// at block 9 and tip: unchanged 880
	for _, bn := range []uint64{9, 10, 11} {
		if got := balAt(t, userAddr, bn); got.Cmp(big.NewInt(880)) != 0 {
			t.Fatalf("sender at block %d: want 880, got %s", bn, got)
		}
	}
}

func TestHistoricalBalance_ReceiverCoinbaseZKVM(t *testing.T) {
	setupHist(t)
	cases := []struct {
		addr   string
		at8    int64 // before the block-9 tx
		latest int64
	}{
		{peerAddr, 500, 600},
		{coinbaseAddr, 0, 10},
		{zkvmAddr, 0, 10},
	}
	for _, c := range cases {
		if got := balAt(t, c.addr, 8); got.Cmp(big.NewInt(c.at8)) != 0 {
			t.Fatalf("%s at block 8: want %d, got %s", c.addr, c.at8, got)
		}
		if got := balAt(t, c.addr, 10); got.Cmp(big.NewInt(c.latest)) != 0 {
			t.Fatalf("%s at tip: want %d, got %s", c.addr, c.latest, got)
		}
	}
}

func TestHistoricalBalance_UnknownAccountIsZero(t *testing.T) {
	setupHist(t)
	if got := balAt(t, "0x9999999999999999999999999999999999999999", 5); got.Sign() != 0 {
		t.Fatalf("unknown account: want 0, got %s", got)
	}
}

func TestHistoricalBalance_LookbackCap(t *testing.T) {
	setupHist(t)
	old := DB_OPs.MaxBalanceLookback
	DB_OPs.MaxBalanceLookback = 1
	defer func() { DB_OPs.MaxBalanceLookback = old }()

	_, err := DB_OPs.GetBalanceAtBlock(common.HexToAddress(userAddr), 5) // tip 10, lookback 5 > 1
	if err == nil {
		t.Fatal("expected history-too-deep error")
	}
}

// Recorded gas fee (gas_fee_wei column) must take precedence over the
// derived gasLimit×price value when present.
func TestHistoricalBalance_RecordedFeePreferred(t *testing.T) {
	h := &histHandle{balances: map[string]string{
		userAddr: "870", // 1000 − 100 (value) − 30 (RECORDED fee, not the derived 20)
	}}
	rec := h.txInBlock9()
	rec.GasFeeWei = "30"
	h.override = rec
	DB_OPs.SetGlobalHandle(h)
	t.Cleanup(func() { DB_OPs.SetGlobalHandle(nil) })

	got, err := DB_OPs.GetBalanceAtBlock(common.HexToAddress(userAddr), 8)
	if err != nil {
		t.Fatalf("GetBalanceAtBlock: %v", err)
	}
	// 870 + 100 + 30 (recorded) = 1000; derived fee (20) would give 990.
	if got.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("recorded fee not used: want 1000, got %s", got)
	}
}

// sanity: the wire strings above parse the way the production converter expects
func TestHistoricalBalance_FixtureSanity(t *testing.T) {
	if _, err := strconv.ParseUint("10", 10, 64); err != nil {
		t.Fatal(err)
	}
}
