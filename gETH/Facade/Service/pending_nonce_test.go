package Service

import (
	"testing"

	block "gossipnode/Block"
)

// stubTx is a minimal block.PendingTx for the pure-function tests.
type stubTx struct {
	from  string
	nonce uint64
}

func (s stubTx) GetHash() string           { return "0xh" }
func (s stubTx) GetFrom() string           { return s.from }
func (s stubTx) GetTo() string             { return "" }
func (s stubTx) GetValue() string          { return "0" }
func (s stubTx) GetNonce() uint64          { return s.nonce }
func (s stubTx) GetGasLimit() string       { return "0" }
func (s stubTx) GetGasPrice() string       { return "0" }
func (s stubTx) GetMaxFee() string         { return "0" }
func (s stubTx) GetMaxPriorityFee() string { return "0" }
func (s stubTx) GetData() []byte           { return nil }
func (s stubTx) GetType() uint32           { return 0 }
func (s stubTx) GetTimestamp() uint64      { return 0 }
func (s stubTx) GetV() string              { return "0x0" }
func (s stubTx) GetR() string              { return "0x0" }
func (s stubTx) GetS() string              { return "0x0" }

func pool(sender string, nonces ...uint64) []block.PendingTx {
	txs := make([]block.PendingTx, 0, len(nonces))
	for _, n := range nonces {
		txs = append(txs, stubTx{from: sender, nonce: n})
	}
	return txs
}

// Pins the geth-exact contiguous-run semantics from the S2 design:
// gapped pool nonces beyond the run must NOT advance the pending nonce
// (over-advancing would manufacture orphans under chain nonce-jumping).
func TestNextAfterContiguousRun(t *testing.T) {
	cases := []struct {
		name      string
		confirmed uint64
		txs       []block.PendingTx
		want      uint64
	}{
		{"design example: gap stops the run", 5, pool("0xA", 5, 6, 8), 7},
		{"empty pool returns confirmed", 5, nil, 5},
		{"run never starts (future-only nonces)", 5, pool("0xA", 7, 8), 5},
		{"full contiguous run", 5, pool("0xA", 5, 6, 7), 8},
		{"stale below-confirmed nonces ignored", 5, pool("0xA", 3, 4), 5},
		{"stale plus contiguous", 5, pool("0xA", 4, 5, 6), 7},
		{"other senders never counted", 5, pool("0xB", 5, 6), 5},
		{"case-insensitive sender match", 5, pool("0xAbCd", 5), 6},
		{"from zero for new account", 0, pool("0xA", 0, 1), 2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sender := "0xa"
			if c.name == "case-insensitive sender match" {
				sender = "0xABCD"
			}
			if got := nextAfterContiguousRun(sender, c.confirmed, c.txs); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestNextAfterContiguousRun_MixedSenders(t *testing.T) {
	txs := append(pool("0xA", 5, 6), pool("0xB", 7, 8, 9)...)
	if got := nextAfterContiguousRun("0xa", 5, txs); got != 7 {
		t.Errorf("mixed senders: got %d, want 7 (only 0xA's run counts)", got)
	}
}
