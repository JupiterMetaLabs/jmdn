//go:build applygate

// D-78 acceptance test: a block proposed through the gRPC ingress
// (BlockServer.ProcessBlock) must carry a block-carried ART identity for an
// account that a contract tx creates during EVM execution — the same guarantee
// the HTTP ingress (processZKBlock) has. Before the fix the gRPC path called the
// pre-prediction EnrichBlockAccountNonces and this test failed.
//
//	APPLYGATE_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable" \
//	  CGO_ENABLED=1 go test -tags applygate ./Block/ -run TestProcessBlockGRPC -v

package Block

import (
	"context"
	"encoding/hex"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/libp2p/go-libp2p/core/host"

	pb "gossipnode/Block/proto"
	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/Sequencer"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/internal/applygate"
	"gossipnode/messaging/BlockProcessing"
)

func TestMain(m *testing.M) {
	if _, err := settings.Load(); err != nil {
		panic("Block applygate: load settings: " + err.Error())
	}
	code := m.Run()
	_ = os.Remove("config/bls.json")
	_ = os.Remove("config/peer.json")
	_ = os.Remove("config")
	os.Exit(code)
}

func gateTx(from common.Address, to *common.Address, nonce uint64, data []byte, value int64, gas uint64) config.Transaction {
	seed := append(append([]byte{byte(nonce)}, from.Bytes()...), data...)
	return config.Transaction{
		Hash:     crypto.Keccak256Hash(seed),
		From:     &from,
		To:       to,
		Value:    big.NewInt(value),
		ChainID:  big.NewInt(applygate.ChainID),
		Nonce:    nonce,
		GasLimit: gas,
		GasPrice: big.NewInt(1),
		Data:     data,
	}
}

func txToProto(tx config.Transaction) *pb.Transaction {
	p := &pb.Transaction{
		Hash:     tx.Hash.Bytes(),
		Nonce:    tx.Nonce,
		GasLimit: tx.GasLimit,
		Data:     tx.Data,
		Value:    tx.Value.Bytes(),
		ChainId:  tx.ChainID.Bytes(),
		GasPrice: tx.GasPrice.Bytes(),
		Type:     uint32(tx.Type),
	}
	if tx.From != nil {
		p.From = tx.From.Bytes()
	}
	if tx.To != nil {
		p.To = tx.To.Bytes()
	}
	return p
}

func TestProcessBlockGRPC_StampsExecutionCreatedAccounts(t *testing.T) {
	resetSlotAndPeriodStores(t) // attachAVCConsensusFields needs a recovered slot clock
	cleanup := applygate.BuildHandle(t, t.TempDir())
	defer cleanup()
	applygate.SeedGenesis(t)

	a, b := applygate.AcctA, applygate.AcctB
	code, err := hex.DecodeString(applygate.BinForward)
	if err != nil {
		t.Fatal(err)
	}
	fwd := crypto.CreateAddress(a, 0)
	fresh := common.BytesToAddress(crypto.Keccak256([]byte("grpc-fresh"))[12:])

	// Block 1 (setup): deploy Forward, applied directly.
	deploy := gateTx(a, nil, 0, code, 0, 3_000_000)
	b1 := &config.ZKBlock{Transactions: []config.Transaction{deploy}, Timestamp: 1_700_000_001,
		CoinbaseAddr: &a, ZKVMAddr: &b, BlockNumber: 1, GasLimit: 30_000_000}
	b1.BlockHash = Security.RecomputeBlockHashFromContents(b1.Transactions)
	if err := DB_OPs.EnrichBlockAccountNoncesWithPredicted(b1, []common.Address{fwd}); err != nil {
		t.Fatal(err)
	}
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b1, nil); err != nil {
		t.Fatalf("setup deploy: %v", err)
	}

	// Block 2, proposed through gRPC: Forward.pay(fresh){1 wei} — the internal
	// transfer creates `fresh` during execution.
	payData := append(crypto.Keccak256([]byte("pay(address)"))[:4], common.LeftPadBytes(fresh.Bytes(), 32)...)
	pay := gateTx(a, &fwd, 1, payData, 1, 1_000_000)
	pbBlock := &pb.ZKBlock{
		Status:       "verified",
		Transactions: []*pb.Transaction{txToProto(pay)},
		Timestamp:    1_700_000_002,
		BlockHash:    Security.RecomputeBlockHashFromContents([]config.Transaction{pay}).Bytes(),
		PrevHash:     b1.BlockHash.Bytes(),
		GasLimit:     30_000_000,
		BlockNumber:  2,
		CoinbaseAddr: a.Bytes(),
		ZkvmAddr:     b.Bytes(),
	}

	var proposed *config.ZKBlock
	saved := grpcStartConsensus
	grpcStartConsensus = func(_ Sequencer.PeerList, _ host.Host, blk *config.ZKBlock) error {
		proposed = blk
		return nil
	}
	defer func() { grpcStartConsensus = saved }()

	if _, err := NewBlockServer(nil, applygate.ChainID).ProcessBlock(context.Background(), &pb.ProcessBlockRequest{Block: pbBlock}); err != nil {
		t.Fatalf("gRPC ProcessBlock: %v", err)
	}
	if proposed == nil {
		t.Fatal("ProcessBlock did not hand a block to consensus")
	}

	var ordinal uint64
	for _, an := range proposed.AccountNonces {
		if an.Address == fresh {
			ordinal = an.Nonce
		}
	}
	if ordinal == 0 || ordinal >= DB_OPs.ARTOrdinalMax {
		t.Fatalf("gRPC-proposed block carries no ART ordinal for the execution-created account %s (AccountNonces=%v)", fresh.Hex(), proposed.AccountNonces)
	}

	// And the proposed block applies: the account is created from that ordinal.
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), proposed, nil); err != nil {
		t.Fatalf("apply gRPC-proposed block: %v", err)
	}
	acc, err := DB_OPs.GetAccount(nil, fresh)
	if err != nil {
		t.Fatalf("fresh account not created: %v", err)
	}
	if acc.Balance != "1" || acc.Nonce != ordinal {
		t.Fatalf("fresh account: balance=%s art=%d, want balance=1 art=%d", acc.Balance, acc.Nonce, ordinal)
	}
	t.Logf("PASS: gRPC-proposed block stamped %s with ordinal %d; applied with balance 1", fresh.Hex(), ordinal)
}
