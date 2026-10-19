package DB_OPs_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/Security"
	"gossipnode/config"
)

// Every header field bound into the ConsensusHash must survive storage, or a
// stored (and so synced) block stops matching its own ConsensusHash. The
// record is built the way backend.toBlockRecord writes it, round-tripped
// through JSON (as the JSONB/columns are), and read back through
// DB_OPs.GetZKBlockByNumber.
func TestHeaderBinding_SurvivesStorageRoundTrip(t *testing.T) {
	Security.SetHeaderBindingFromHeight(1)
	t.Cleanup(func() { Security.SetHeaderBindingFromHeight(0) })

	cb := common.HexToAddress("0x00000000000000000000000000000000000000c0")
	zk := common.HexToAddress("0x00000000000000000000000000000000000000d0")
	b := &config.ZKBlock{
		BlockNumber: 42, BlockHash: common.HexToHash("0xab"), PrevHash: common.HexToHash("0x01"),
		Timestamp: 1_790_000_123, GasLimit: 30_000_000, GasUsed: 21_000,
		StateRoot: common.HexToHash("0x02"), CoinbaseAddr: &cb, ZKVMAddr: &zk,
		FeeRecipients: []config.FeeRecipient{{Addr: common.HexToAddress("0xe0"), Weight: 3}},
		AccountNonces: []config.AccountNonce{{Address: common.HexToAddress("0xf0"), Nonce: 9}},
		Slot:          77, Period: 1,
	}
	b.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(b)

	fr, _ := json.Marshal(b.FeeRecipients)
	an, _ := json.Marshal(b.AccountNonces)
	rec := &thebegateway.BlockRecord{
		BlockNumber: b.BlockNumber, BlockHash: b.BlockHash.Hex(), ParentHash: b.PrevHash.Hex(),
		Timestamp: time.Unix(b.Timestamp, 0).UTC(), StateRoot: b.StateRoot.Hex(),
		GasLimit: b.GasLimit, GasUsed: b.GasUsed, CoinbaseAddr: cb.Hex(), ZKVMAddr: zk.Hex(),
		ExtraData: map[string]any{
			"fee_recipients": string(fr), "account_nonces": string(an),
			"consensus_hash": b.ConsensusHash.Hex(), "slot": b.Slot, "period": b.Period,
		},
	}
	raw, _ := json.Marshal(rec)
	var back thebegateway.BlockRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	DB_OPs.SetGlobalHandle(&recordHandle{rec: &back})
	t.Cleanup(func() { DB_OPs.SetGlobalHandle(nil) })
	got, err := DB_OPs.GetZKBlockByNumber(nil, b.BlockNumber)
	if err != nil {
		t.Fatal(err)
	}
	if want := Security.RecomputeBlockHashWithConsensusFields(got); want != b.ConsensusHash {
		t.Fatalf("stored block no longer matches its ConsensusHash: %s != %s", want.Hex(), b.ConsensusHash.Hex())
	}
}

type recordHandle struct {
	store.ThebeHandle
	rec *thebegateway.BlockRecord
}

func (h *recordHandle) GetBlock(_ context.Context, _ uint64) (*thebegateway.BlockRecord, error) {
	return h.rec, nil
}
func (h *recordHandle) GetZKProof(_ context.Context, _ uint64) (*thebegateway.ZKProofRecord, error) {
	return &thebegateway.ZKProofRecord{}, nil
}
func (h *recordHandle) GetTransactionsByBlock(_ context.Context, _ uint64) ([]*thebegateway.TransactionRecord, error) {
	return nil, nil
}
func (h *recordHandle) GetL1FinalityForBlock(_ context.Context, _ uint64) (*thebegateway.L1FinalityRecord, error) {
	return nil, nil
}
