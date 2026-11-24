//go:build applygate

package thebesync

// End-to-end legacy follow: blocks in main's format (signed transactions, no
// ConsensusHash, no certificate) are served over /fastsync/v4 by a mocknet
// peer pinned as the main sequencer and applied into a real ThebeDB store.
//
//	APPLYGATE_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable" \
//	  go test -tags applygate ./thebesync/ -run TestLegacyCatchUp_Applygate

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	fssync "github.com/JupiterMetaLabs/JMDN-FastSync/thebesync"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"

	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/internal/applygate"
	"gossipnode/messaging"
)

type staticProvider struct{ blocks []*config.ZKBlock }

func (p staticProvider) LatestHeight() (uint64, bool, error) {
	return uint64(len(p.blocks) - 1), len(p.blocks) > 0, nil
}

func (p staticProvider) TipHash(h uint64) (string, error) { return p.blocks[h].BlockHash.Hex(), nil }

func (p staticProvider) RawBlock(n uint64) ([]byte, bool, error) {
	if n >= uint64(len(p.blocks)) {
		return nil, false, nil
	}
	raw, err := json.Marshal(p.blocks[n])
	return raw, err == nil, err
}

func legacySignedTransfer(t *testing.T, key *ecdsa.PrivateKey, to common.Address, nonce uint64, wei int64) config.Transaction {
	t.Helper()
	chainID := big.NewInt(applygate.ChainID)
	from := crypto.PubkeyToAddress(key.PublicKey)
	inner := &types.DynamicFeeTx{
		ChainID: chainID, Nonce: nonce, To: &to, Value: big.NewInt(wei),
		GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(1), Gas: 21000,
	}
	signed, err := types.SignNewTx(key, types.LatestSignerForChainID(chainID), inner)
	if err != nil {
		t.Fatal(err)
	}
	v, r, s := signed.RawSignatureValues()
	return config.Transaction{
		Hash: signed.Hash(), From: &from, To: &to, Value: big.NewInt(wei),
		Type: types.DynamicFeeTxType, ChainID: chainID, Nonce: nonce, GasLimit: 21000,
		MaxFee: big.NewInt(1), MaxPriorityFee: big.NewInt(1), V: v, R: r, S: s,
	}
}

// legacyMainBlock builds a block the way main does: BlockHash and TxnsRoot
// over the transactions, its own fee recipients, nothing v3-specific.
func legacyMainBlock(num uint64, prev common.Hash, txs []config.Transaction, fee common.Address) *config.ZKBlock {
	cb, zk := applygate.AcctA, applygate.AcctB
	return &config.ZKBlock{
		BlockNumber:   num,
		PrevHash:      prev,
		Timestamp:     int64(1_700_000_000 + num),
		Transactions:  txs,
		BlockHash:     messaging.RecomputeBlockHashFromTxs(txs),
		TxnsRoot:      messaging.RecomputeTxnsRoot(txs),
		StateRoot:     crypto.Keccak256Hash([]byte{byte(num), 's'}),
		CoinbaseAddr:  &cb,
		ZKVMAddr:      &zk,
		GasLimit:      30_000_000,
		FeeRecipients: []config.FeeRecipient{{Addr: fee, Weight: 1}},
	}
}

func TestLegacyCatchUp_Applygate(t *testing.T) {
	Security.SetExpectedChainID(applygate.ChainID)
	defer applygate.BuildHandle(t, t.TempDir())()
	applygate.SeedGenesis(t)

	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	recv := common.HexToAddress("0x00000000000000000000000000000000000000D7")
	// Main cannot create a never-funded fee recipient, so its fee recipients
	// always exist already.
	fee := applygate.AcctA
	if err := DB_OPs.CreateAccount(nil, "did:jmdn:"+strings.ToLower(sender.Hex()), sender, nil); err != nil {
		t.Fatal(err)
	}
	if err := DB_OPs.UpdateAccountBalance(nil, sender, applygate.OneK.String(), 0); err != nil {
		t.Fatal(err)
	}

	genesis := &config.ZKBlock{
		BlockNumber: 0, Timestamp: 1_700_000_000,
		BlockHash: crypto.Keccak256Hash([]byte("legacy-genesis")),
		TxnsRoot:  "0x" + strings.Repeat("0", 63) + "1",
		StateRoot: crypto.Keccak256Hash([]byte("legacy-genesis-state")),
	}
	if err := DB_OPs.StoreZKBlock(nil, genesis); err != nil {
		t.Fatalf("store genesis: %v", err)
	}
	if _, _, err := DB_OPs.UpdateLatestBlockMonotonic(0); err != nil {
		t.Fatal(err)
	}

	chain := []*config.ZKBlock{genesis}
	nonce := uint64(0)
	for n := uint64(1); n <= 3; n++ {
		// Block 1 creates recv from its carried identity; later blocks pay an
		// existing account.
		to := recv
		if n > 1 {
			to = applygate.AcctB
		}
		txs := []config.Transaction{
			legacySignedTransfer(t, key, to, nonce, 100),
			legacySignedTransfer(t, key, to, nonce+1, 50),
		}
		nonce += 2
		b := legacyMainBlock(n, chain[n-1].BlockHash, txs, fee)
		// Main stamps the identity nonce of every sender and receiver
		// (EnrichBlockAccountNonces); with an existing fee recipient the v3base
		// stamp adds nothing beyond that.
		if err := DB_OPs.EnrichBlockAccountNoncesWithPredicted(b, nil); err != nil {
			t.Fatal(err)
		}
		chain = append(chain, b)
	}

	mn := mocknet.New()
	defer mn.Close()
	mainH, err := mn.GenPeer()
	if err != nil {
		t.Fatal(err)
	}
	otherH, err := mn.GenPeer()
	if err != nil {
		t.Fatal(err)
	}
	nodeH, err := mn.GenPeer()
	if err != nil {
		t.Fatal(err)
	}
	if err := mn.LinkAll(); err != nil {
		t.Fatal(err)
	}
	for _, h := range []host.Host{mainH, otherH} {
		srv := &fssync.Server{Provider: staticProvider{blocks: chain}}
		h.SetStreamHandler(fssync.HeadProtocol, srv.HeadHandler)
		h.SetStreamHandler(fssync.GetBlocksProtocol, srv.GetBlocksHandler)
	}
	defer messaging.OverrideLegacyFollowSettings(legacySettings(mainH.ID(), 0))()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	otherInfo := &peer.AddrInfo{ID: otherH.ID(), Addrs: otherH.Addrs()}
	if got, err := LegacyCatchUp(ctx, nodeH, otherInfo); err == nil || got != 0 {
		t.Fatalf("catch-up from a peer that is not the pinned main sequencer: tip %d err %v", got, err)
	}

	mainInfo := &peer.AddrInfo{ID: mainH.ID(), Addrs: mainH.Addrs()}
	got, err := LegacyCatchUp(ctx, nodeH, mainInfo)
	if err != nil {
		t.Fatalf("legacy catch-up: %v", err)
	}
	if got != 3 {
		t.Fatalf("tip = %d, want 3", got)
	}

	for n := uint64(1); n <= 3; n++ {
		stored, err := DB_OPs.GetZKBlockByNumber(nil, n)
		if err != nil || stored.BlockHash != chain[n].BlockHash {
			t.Fatalf("block %d not stored as served: %v", n, err)
		}
	}
	sa, err := DB_OPs.GetAccount(nil, sender)
	if err != nil {
		t.Fatal(err)
	}
	ra, err := DB_OPs.GetAccount(nil, recv)
	if err != nil {
		t.Fatalf("receiver not created: %v", err)
	}
	if ra.Balance != "150" {
		t.Fatalf("receiver balance = %s, want 150", ra.Balance)
	}
	if sa.TxNonce != 6 {
		t.Fatalf("sender tx nonce = %d, want 6", sa.TxNonce)
	}
	t.Logf("sender balance after 6 transfers: %s (start %s)", sa.Balance, applygate.OneK.String())

	if _, err := LegacyCatchUp(ctx, nodeH, mainInfo); err != nil {
		t.Fatalf("second catch-up at tip: %v", err)
	}
}
