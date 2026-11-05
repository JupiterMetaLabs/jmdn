package Security

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/config"
)

func withHeaderBinding(t *testing.T, c HeaderBindingConfig) {
	t.Helper()
	prev := currentHeaderBinding()
	SetHeaderBinding(c)
	t.Cleanup(func() { SetHeaderBinding(prev) })
}

func hbAddr(s string) *common.Address { a := common.HexToAddress(s); return &a }

var (
	vecTxsHash  = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	vecCoinbase = hbAddr("0x2222222222222222222222222222222222222222")
	vecZKVM     = hbAddr("0x3333333333333333333333333333333333333333")
)

const vecTimestamp int64 = 1_700_000_000

// HeaderBlockHashVector is the cross-repo test vector: the orchestrator's
// implementation must produce the same value for the same inputs.
const HeaderBlockHashVector = "0xc6ea1b5b95e79696316e8f8d68730257dd9805313459677e9c47c3df7354d42f"

// The preimage, rebuilt byte by byte, matches HeaderBlockHash.
func TestHeaderBlockHash_Preimage(t *testing.T) {
	pre, _ := hex.DecodeString(hex.EncodeToString([]byte("jmdn/block-header-hash/v1")) +
		strings.Repeat("11", 32) + strings.Repeat("22", 20) + strings.Repeat("33", 20) +
		"000000006553f100")
	want := crypto.Keccak256Hash(pre)
	got := HeaderBlockHash(vecTxsHash, vecCoinbase, vecZKVM, vecTimestamp)
	if got != want {
		t.Fatalf("HeaderBlockHash = %s, manual preimage = %s", got.Hex(), want.Hex())
	}
	t.Logf("vector: txs=0x11..11 coinbase=0x22..22 zkvm=0x33..33 ts=%d -> %s", vecTimestamp, got.Hex())
	if got.Hex() != HeaderBlockHashVector {
		t.Fatalf("vector changed: %s (pinned %s)", got.Hex(), HeaderBlockHashVector)
	}
}

func TestHeaderBlockHash_EachFieldBound(t *testing.T) {
	base := HeaderBlockHash(vecTxsHash, vecCoinbase, vecZKVM, vecTimestamp)
	for name, h := range map[string]common.Hash{
		"txs":       HeaderBlockHash(common.HexToHash("0x12"), vecCoinbase, vecZKVM, vecTimestamp),
		"coinbase":  HeaderBlockHash(vecTxsHash, hbAddr("0x99"), vecZKVM, vecTimestamp),
		"zkvm":      HeaderBlockHash(vecTxsHash, vecCoinbase, hbAddr("0x99"), vecTimestamp),
		"timestamp": HeaderBlockHash(vecTxsHash, vecCoinbase, vecZKVM, vecTimestamp+1),
		"swap":      HeaderBlockHash(vecTxsHash, vecZKVM, vecCoinbase, vecTimestamp),
	} {
		if h == base {
			t.Fatalf("changing %s did not change the hash", name)
		}
	}
}

func TestExpectedBlockHash_Activation(t *testing.T) {
	withHeaderBinding(t, HeaderBindingConfig{FromHeight: 100})
	b := &config.ZKBlock{BlockNumber: 99, CoinbaseAddr: vecCoinbase, ZKVMAddr: vecZKVM, Timestamp: vecTimestamp}
	if got := ExpectedBlockHash(b, vecTxsHash); got != vecTxsHash {
		t.Fatal("below the activation height the hash must stay transactions-only")
	}
	b.BlockNumber = 100
	if got := ExpectedBlockHash(b, vecTxsHash); got != HeaderBlockHash(vecTxsHash, vecCoinbase, vecZKVM, vecTimestamp) {
		t.Fatal("at the activation height the hash must cover the header fields")
	}
	withHeaderBinding(t, HeaderBindingConfig{})
	if got := ExpectedBlockHash(b, vecTxsHash); got != vecTxsHash {
		t.Fatal("with binding off the hash must stay transactions-only")
	}
}

// CheckBlockHash on a bound block: the header hash verifies, a changed
// coinbase, zkvm address or timestamp does not.
func TestCheckBlockHash_HeaderBound(t *testing.T) {
	withHeaderBinding(t, HeaderBindingConfig{FromHeight: 1})
	b := &config.ZKBlock{BlockNumber: 5, CoinbaseAddr: vecCoinbase, ZKVMAddr: vecZKVM, Timestamp: vecTimestamp}
	b.BlockHash = HeaderBlockHash(RecomputeBlockHashFromContents(nil), b.CoinbaseAddr, b.ZKVMAddr, b.Timestamp)
	if ok, err := CheckBlockHash(b); !ok || err != nil {
		t.Fatalf("honest bound block refused: %v", err)
	}
	for name, edit := range map[string]func(*config.ZKBlock){
		"coinbase":  func(x *config.ZKBlock) { x.CoinbaseAddr = hbAddr("0xa77ac4e7") },
		"zkvm":      func(x *config.ZKBlock) { x.ZKVMAddr = hbAddr("0xa77ac4e7") },
		"timestamp": func(x *config.ZKBlock) { x.Timestamp = 4_102_444_800 },
	} {
		c := *b
		edit(&c)
		if ok, _ := CheckBlockHash(&c); ok {
			t.Fatalf("block with a changed %s passed CheckBlockHash", name)
		}
	}
}

func TestCheckHeaderFields(t *testing.T) {
	now := time.Unix(vecTimestamp, 0)
	withHeaderBinding(t, HeaderBindingConfig{FromHeight: 10, Coinbase: vecCoinbase, ZKVM: vecZKVM, MaxFutureDrift: 15 * time.Second})
	good := func() *config.ZKBlock {
		return &config.ZKBlock{BlockNumber: 10, CoinbaseAddr: vecCoinbase, ZKVMAddr: vecZKVM, Timestamp: vecTimestamp}
	}
	if err := CheckHeaderFields(good(), vecTimestamp-5, now); err != nil {
		t.Fatalf("honest block refused: %v", err)
	}
	if err := CheckHeaderFields(good(), vecTimestamp, now); err != nil {
		t.Fatalf("timestamp equal to the parent's refused: %v", err)
	}
	cases := map[string]func(*config.ZKBlock){
		"other coinbase": func(b *config.ZKBlock) { b.CoinbaseAddr = hbAddr("0xa77ac4e7") },
		"no coinbase":    func(b *config.ZKBlock) { b.CoinbaseAddr = nil },
		"other zkvm":     func(b *config.ZKBlock) { b.ZKVMAddr = hbAddr("0xa77ac4e7") },
		"before parent":  func(b *config.ZKBlock) { b.Timestamp = vecTimestamp - 10 },
		"far future":     func(b *config.ZKBlock) { b.Timestamp = 4_102_444_800 },
	}
	for name, edit := range cases {
		b := good()
		edit(b)
		if err := CheckHeaderFields(b, vecTimestamp-5, now); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// Below the activation height nothing is checked.
	b := good()
	b.BlockNumber, b.CoinbaseAddr, b.Timestamp = 9, hbAddr("0xa77ac4e7"), 4_102_444_800
	if err := CheckHeaderFields(b, vecTimestamp, now); err != nil {
		t.Fatalf("pre-activation block checked: %v", err)
	}
}

// ConsensusHash: unchanged below the activation height; above it, every
// header field and the fee recipients change it.
func TestConsensusHash_HeaderBound(t *testing.T) {
	mk := func(n uint64) *config.ZKBlock {
		return &config.ZKBlock{BlockNumber: n, PrevHash: common.HexToHash("0x01"), Slot: n,
			CoinbaseAddr: vecCoinbase, ZKVMAddr: vecZKVM, Timestamp: vecTimestamp,
			FeeRecipients: []config.FeeRecipient{{Addr: common.HexToAddress("0x44"), Weight: 1}, {Addr: common.HexToAddress("0x55"), Weight: 2}}}
	}
	withHeaderBinding(t, HeaderBindingConfig{})
	offBelow := RecomputeBlockHashWithConsensusFields(mk(50))

	withHeaderBinding(t, HeaderBindingConfig{FromHeight: 100})
	if RecomputeBlockHashWithConsensusFields(mk(50)) != offBelow {
		t.Fatal("ConsensusHash changed below the activation height")
	}
	tampered := mk(50)
	tampered.CoinbaseAddr, tampered.Timestamp, tampered.FeeRecipients = hbAddr("0x99"), 1, nil
	if RecomputeBlockHashWithConsensusFields(tampered) != offBelow {
		t.Fatal("below the activation height the header fields must not be hashed")
	}

	base := RecomputeBlockHashWithConsensusFields(mk(100))
	edits := map[string]func(*config.ZKBlock){
		"coinbase":      func(b *config.ZKBlock) { b.CoinbaseAddr = hbAddr("0x99") },
		"nil coinbase":  func(b *config.ZKBlock) { b.CoinbaseAddr = nil },
		"zkvm":          func(b *config.ZKBlock) { b.ZKVMAddr = hbAddr("0x99") },
		"timestamp":     func(b *config.ZKBlock) { b.Timestamp++ },
		"fee recipient": func(b *config.ZKBlock) { b.FeeRecipients[0].Addr = common.HexToAddress("0x99") },
		"fee weight":    func(b *config.ZKBlock) { b.FeeRecipients[1].Weight = 3 },
		"fee order": func(b *config.ZKBlock) {
			b.FeeRecipients[0], b.FeeRecipients[1] = b.FeeRecipients[1], b.FeeRecipients[0]
		},
		"no fee recipient": func(b *config.ZKBlock) { b.FeeRecipients = nil },
	}
	seen := map[common.Hash]string{base: "base"}
	for name, edit := range edits {
		b := mk(100)
		edit(b)
		h := RecomputeBlockHashWithConsensusFields(b)
		if other, dup := seen[h]; dup {
			t.Fatalf("%s gives the same ConsensusHash as %s", name, other)
		}
		seen[h] = name
	}
	nilCB, zeroCB := mk(100), mk(100)
	nilCB.CoinbaseAddr, zeroCB.CoinbaseAddr = nil, &common.Address{}
	if RecomputeBlockHashWithConsensusFields(nilCB) != RecomputeBlockHashWithConsensusFields(zeroCB) {
		t.Fatal("nil and zero coinbase must hash alike (storage may turn one into the other)")
	}
	if RecomputeConsensusHashFromTxHashes(mk(100)) != base {
		t.Fatal("tx-hash variant disagrees on a block without transactions")
	}
}
