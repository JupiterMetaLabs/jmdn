package DB_OPs

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// A type-2 transaction with a non-empty access list must read back from its
// stored record with the same access list and chain id, so its contents
// re-hash to the stored tx hash (the access list is signed content).
func TestTxRecordRoundTrip_PreservesAccessListAndChainID(t *testing.T) {
	key, _ := crypto.GenerateKey()
	chainID := big.NewInt(7000700)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	al := types.AccessList{{Address: common.HexToAddress("0x01"), StorageKeys: []common.Hash{common.HexToHash("0x02")}}}
	signed, err := types.SignNewTx(key, types.LatestSignerForChainID(chainID), &types.DynamicFeeTx{
		ChainID: chainID, Nonce: 3, To: &to, Value: big.NewInt(5),
		GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), Gas: 30000, AccessList: al,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfgAL := config.AccessList{{Address: al[0].Address, StorageKeys: al[0].StorageKeys}}

	v, r, s := signed.RawSignatureValues()
	rec := &thebegateway.TransactionRecord{
		TxHash: signed.Hash().Hex(), Type: 2, Nonce: "3", ValueWei: "5", GasLimit: "30000",
		MaxFeeWei: "2", MaxPriorityFeeWei: "1", ChainID: "7000700",
		SigV: v.Uint64(), SigR: r.Text(16), SigS: s.Text(16),
		AccessList: cfgAL.ToRecord(),
	}
	toHex := to.Hex()
	rec.ToAddr = &toHex

	// Simulate the JSONB column round trip.
	raw, _ := json.Marshal(rec.AccessList)
	rec.AccessList = nil
	_ = json.Unmarshal(raw, &rec.AccessList)

	tx := txRecordToTransaction(rec)
	if len(tx.AccessList) != 1 || tx.AccessList[0].Address != al[0].Address || len(tx.AccessList[0].StorageKeys) != 1 {
		t.Fatalf("access list not restored: %+v", tx.AccessList)
	}
	if tx.ChainID == nil || tx.ChainID.Cmp(chainID) != 0 {
		t.Fatalf("chain id not restored: %v", tx.ChainID)
	}
	rebuilt := types.NewTx(&types.DynamicFeeTx{
		ChainID: tx.ChainID, Nonce: tx.Nonce, To: tx.To, Value: tx.Value,
		GasTipCap: tx.MaxPriorityFee, GasFeeCap: tx.MaxFee, Gas: tx.GasLimit, Data: tx.Data,
		AccessList: types.AccessList{{Address: tx.AccessList[0].Address, StorageKeys: tx.AccessList[0].StorageKeys}},
		V:          tx.V, R: tx.R, S: tx.S,
	})
	if rebuilt.Hash() != signed.Hash() {
		t.Fatalf("stored tx no longer re-hashes: %s != %s", rebuilt.Hash().Hex(), signed.Hash().Hex())
	}
}

// A transaction carrying non-empty call Data (a contract call) must read back
// from its stored record with that same Data, so its contents re-hash to the
// stored tx hash. Data is signed content exactly like AccessList above; the
// existing round-trip test above never exercises this because its fixture
// transaction carries no Data at all, so a reader that silently drops Data
// still passes it.
func TestTxRecordRoundTrip_PreservesData(t *testing.T) {
	key, _ := crypto.GenerateKey()
	chainID := big.NewInt(7000700)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	callData := []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03}
	signed, err := types.SignNewTx(key, types.LatestSignerForChainID(chainID), &types.DynamicFeeTx{
		ChainID: chainID, Nonce: 4, To: &to, Value: big.NewInt(0),
		GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), Gas: 50000, Data: callData,
	})
	if err != nil {
		t.Fatal(err)
	}

	v, r, s := signed.RawSignatureValues()
	rec := &thebegateway.TransactionRecord{
		TxHash: signed.Hash().Hex(), Type: 2, Nonce: "4", ValueWei: "0", GasLimit: "50000",
		MaxFeeWei: "2", MaxPriorityFeeWei: "1", ChainID: "7000700",
		SigV: v.Uint64(), SigR: r.Text(16), SigS: s.Text(16),
		Data: callData,
	}
	toHex := to.Hex()
	rec.ToAddr = &toHex

	tx := txRecordToTransaction(rec)
	if string(tx.Data) != string(callData) {
		t.Fatalf("call data not restored: got %x, want %x", tx.Data, callData)
	}
	rebuilt := types.NewTx(&types.DynamicFeeTx{
		ChainID: tx.ChainID, Nonce: tx.Nonce, To: tx.To, Value: tx.Value,
		GasTipCap: tx.MaxPriorityFee, GasFeeCap: tx.MaxFee, Gas: tx.GasLimit, Data: tx.Data,
		V: tx.V, R: tx.R, S: tx.S,
	})
	if rebuilt.Hash() != signed.Hash() {
		t.Fatalf("stored tx with call data no longer re-hashes: %s != %s", rebuilt.Hash().Hex(), signed.Hash().Hex())
	}
}

// An empty access list stores nothing and reads back empty.
func TestAccessListRecord_Empty(t *testing.T) {
	if config.AccessList(nil).ToRecord() != nil {
		t.Fatal("empty access list must store nil")
	}
	if config.AccessListFromRecord(nil) != nil || config.AccessListFromRecord(map[string]any{}) != nil {
		t.Fatal("empty record must read back empty")
	}
}
