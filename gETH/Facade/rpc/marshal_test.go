package rpc

// Golden-shape tests for the JSON-RPC transaction/block marshaling
// (marshalTx / marshalBlock). These lock down the output contract that
// explorers and client libraries (ethers.js, viem) parse strictly:
//
//   - legacy (type 0) txs: no accessList, no yParity, v is the raw 27/28
//     (or EIP-155) value
//   - type 2 txs: accessList present (empty), yParity == v, both in {0x0, 0x1},
//     maxFeePerGas / maxPriorityFeePerGas always present,
//     gasPrice == min(maxFeePerGas, baseFee + tip)
//   - pending txs: blockHash / blockNumber / transactionIndex are JSON null
//   - txs inside eth_getBlockByNumber(full=true): block context comes from the
//     parent block WITHOUT mutating the *Types.Tx (cache-safety regression guard)
//
// TestDynamicFeeTxVIsParityBit additionally proves — against go-ethereum's own
// signer — that a signed type-2 transaction carries V as the raw recovery bit
// (0 or 1), never 27/28. This is the invariant that makes `yParity = v` in
// marshalTx correct. It replaces a one-time live-node check (no type-2 txs
// exist on mainnet yet) with a permanent CI proof.

import (
	"math/big"
	"testing"

	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

var testChainID = big.NewInt(7000700)

func legacyTx() *Types.Tx {
	return &Types.Tx{
		Hash:     common.HexToHash("0x11").Bytes(),
		From:     common.HexToAddress("0xaa").Bytes(),
		To:       common.HexToAddress("0xbb").Bytes(),
		Input:    []byte{},
		Nonce:    7,
		Value:    big.NewInt(1000).Bytes(),
		Gas:      21000,
		GasPrice: big.NewInt(35_000_000_000).Bytes(),
		Type:     0,
		V:        27,
		R:        big.NewInt(1).Bytes(),
		S:        big.NewInt(2).Bytes(),
		ChainID:  testChainID.Bytes(),
	}
}

func dynamicFeeTx(v uint32) *Types.Tx {
	return &Types.Tx{
		Hash:                 common.HexToHash("0x22").Bytes(),
		From:                 common.HexToAddress("0xaa").Bytes(),
		To:                   common.HexToAddress("0xbb").Bytes(),
		Input:                []byte{},
		Nonce:                8,
		Value:                big.NewInt(2000).Bytes(),
		Gas:                  21000,
		Type:                 2,
		V:                    v,
		R:                    big.NewInt(3).Bytes(),
		S:                    big.NewInt(4).Bytes(),
		MaxFeePerGas:         big.NewInt(70_000_000_000).Bytes(),
		MaxPriorityFeePerGas: big.NewInt(1_000_000_000).Bytes(),
		ChainID:              testChainID.Bytes(),
	}
}

func TestMarshalTxLegacyShape(t *testing.T) {
	m := marshalTx(legacyTx(), testChainID)

	if _, ok := m["accessList"]; ok {
		t.Error("legacy tx must not carry accessList")
	}
	if _, ok := m["yParity"]; ok {
		t.Error("legacy tx must not carry yParity")
	}
	if m["v"] != "0x1b" {
		t.Errorf("legacy v: got %v, want 0x1b", m["v"])
	}
	if m["type"] != "0x0" {
		t.Errorf("type: got %v, want 0x0", m["type"])
	}
	if m["nonce"] != "0x7" {
		t.Errorf("nonce: got %v, want 0x7", m["nonce"])
	}
	if m["gasPrice"] != "0x826299e00" { // 35 gwei
		t.Errorf("gasPrice: got %v, want 0x826299e00", m["gasPrice"])
	}
	// Pending shape: block context keys present, values null.
	for _, k := range []string{"blockHash", "blockNumber", "transactionIndex"} {
		v, ok := m[k]
		if !ok {
			t.Errorf("%s key must be present (null) on pending tx", k)
		}
		if v != nil {
			t.Errorf("%s must be null on pending tx, got %v", k, v)
		}
	}
}

func TestMarshalTxType2Shape(t *testing.T) {
	for _, vBit := range []uint32{0, 1} {
		m := marshalTx(dynamicFeeTx(vBit), testChainID)

		wantV := "0x0"
		if vBit == 1 {
			wantV = "0x1"
		}
		if m["v"] != wantV {
			t.Errorf("v (parity %d): got %v, want %s", vBit, m["v"], wantV)
		}
		if m["yParity"] != wantV {
			t.Errorf("yParity (parity %d): got %v, want %s", vBit, m["yParity"], wantV)
		}
		al, ok := m["accessList"]
		if !ok {
			t.Error("type 2 tx must carry accessList")
		} else if list, isList := al.([]any); !isList || len(list) != 0 {
			t.Errorf("accessList must be an empty array, got %v", al)
		}
		if _, ok := m["maxFeePerGas"]; !ok {
			t.Error("type 2 tx must carry maxFeePerGas")
		}
		if _, ok := m["maxPriorityFeePerGas"]; !ok {
			t.Error("type 2 tx must carry maxPriorityFeePerGas")
		}

		// gasPrice must be min(maxFee, baseFee+tip) — computed, not hardcoded,
		// so the test tracks config.BaseFeeWei.
		maxFee := big.NewInt(70_000_000_000)
		tip := big.NewInt(1_000_000_000)
		expected := new(big.Int).Add(big.NewInt(config.BaseFeeWei), tip)
		if maxFee.Cmp(expected) < 0 {
			expected = maxFee
		}
		wantGasPrice := "0x" + expected.Text(16)
		if m["gasPrice"] != wantGasPrice {
			t.Errorf("gasPrice: got %v, want %s", m["gasPrice"], wantGasPrice)
		}
	}
}

func TestMarshalBlockInjectsContextWithoutMutation(t *testing.T) {
	tx := dynamicFeeTx(1)
	blockHash := common.HexToHash("0xabcdef").Bytes()
	b := &Types.Block{
		Header: &Types.BlockHeader{
			Hash:   blockHash,
			Number: 12345,
		},
		Transactions: []*Types.Tx{tx},
	}

	m := marshalBlock(b, true, testChainID)

	txs, ok := m["transactions"].([]any)
	if !ok || len(txs) != 1 {
		t.Fatalf("expected 1 full tx in marshaled block, got %v", m["transactions"])
	}
	tm, ok := txs[0].(map[string]any)
	if !ok {
		t.Fatalf("marshaled tx has unexpected type %T", txs[0])
	}

	if tm["blockHash"] != "0x"+common.Bytes2Hex(blockHash) {
		t.Errorf("blockHash: got %v", tm["blockHash"])
	}
	if tm["blockNumber"] != "0x3039" { // 12345
		t.Errorf("blockNumber: got %v, want 0x3039", tm["blockNumber"])
	}
	if tm["transactionIndex"] != "0x0" {
		t.Errorf("transactionIndex: got %v, want 0x0", tm["transactionIndex"])
	}

	// Cache-safety regression guard: the *Types.Tx itself must NOT be mutated.
	// If a block cache is ever added, mutation here would contaminate
	// concurrent requests. See PR #75.
	if tx.BlockHash != nil {
		t.Error("marshalBlock mutated tx.BlockHash — must stay nil")
	}
	if tx.BlockNumber != nil {
		t.Error("marshalBlock mutated tx.BlockNumber — must stay nil")
	}
	if tx.TransactionIndex != nil {
		t.Error("marshalBlock mutated tx.TransactionIndex — must stay nil")
	}
}

// TestDynamicFeeTxVIsParityBit proves, against go-ethereum's signer, that a
// signed EIP-1559 (type 2) transaction stores V as the raw recovery bit (0/1).
// jmdn ingests V via ethTx.RawSignatureValues() (Facade/Service/Service.go) and
// passes it through unchanged, so this invariant is exactly what makes
// marshalTx's `yParity = v` correct.
func TestDynamicFeeTxVIsParityBit(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	to := common.HexToAddress("0xbb")
	inner := &ethtypes.DynamicFeeTx{
		ChainID:   testChainID,
		Nonce:     0,
		GasTipCap: big.NewInt(1_000_000_000),
		GasFeeCap: big.NewInt(70_000_000_000),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(1),
	}

	signer := ethtypes.LatestSignerForChainID(testChainID)
	signed, err := ethtypes.SignNewTx(key, signer, inner)
	if err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	v, _, _ := signed.RawSignatureValues()
	if v.Cmp(big.NewInt(0)) != 0 && v.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("type 2 RawSignatureValues V = %s; must be 0 or 1 — yParity mapping in marshalTx would be wrong", v.String())
	}
}
