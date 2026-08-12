package RejectionReport

// Tests for the consensus-rejection report sent to the orchestrator.
//
// The highest-value assertion here is the WIRE SHAPE. The orchestrator decodes
// this payload with DisallowUnknownFields and parses the amounts with
// big.Int.SetString(s, 10). So a renamed field becomes a 400, and a hex amount
// becomes a silently wrong balance in its failed-transaction table. Neither
// failure is visible from this repo, which is why it is pinned by a test.

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func addr(hexStr string) *common.Address {
	a := common.HexToAddress(hexStr)
	return &a
}

func sampleBlock() *config.ZKBlock {
	return &config.ZKBlock{
		BlockNumber: 4211,
		BlockHash:   common.HexToHash("0xbbb1"),
		Transactions: []config.Transaction{
			{
				Hash:     common.HexToHash("0xdead01"),
				From:     addr("0x9ec1b68b660d5d1b7a8ede382233da7674434c22"),
				To:       addr("0x738c03d05dc60693a77c0da0ca7f40018e80a248"),
				Nonce:    3,
				Value:    big.NewInt(2_000_000_000_000_000),
				GasPrice: big.NewInt(35_000_000_000),
				ChainID:  big.NewInt(7000700),
				Type:     0,
				Data:     []byte{0xde, 0xad, 0xbe, 0xef},
			},
		},
	}
}

// Field names are a cross-repo contract. This literal must match
// ConsensusRejectedRequest / ConsensusRejectedTxn in the orchestrator's
// cmd/orchestrator/consensus_reject_api.go.
func TestReportWireShape(t *testing.T) {
	raw, err := json.Marshal(build(sampleBlock(), "quorum not reached", "peerA: bad nonce"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantTop := []string{"block_hash", "block_number", "detail", "reason", "rejected_at", "transactions"}
	assertKeys(t, "report", top, wantTop)

	var txns []map[string]json.RawMessage
	if err := json.Unmarshal(top["transactions"], &txns); err != nil {
		t.Fatalf("unmarshal transactions: %v", err)
	}
	if len(txns) != 1 {
		t.Fatalf("len(transactions) = %d, want 1", len(txns))
	}
	wantTxn := []string{"chain_id", "data", "from", "gas_price", "hash", "nonce", "to", "type", "value"}
	assertKeys(t, "transaction", txns[0], wantTxn)
}

// goldenBlock is the fixture whose serialization is pinned by
// TestReportGoldenWire and replayed verbatim against the orchestrator's real
// handler in its TestConsensusRejectAcceptsJMDNGoldenPayload. Keep the two in
// sync: they are the only check that the two repos actually agree on the wire.
func goldenBlock() *config.ZKBlock {
	return &config.ZKBlock{
		BlockNumber: 4211,
		BlockHash:   common.HexToHash("0xbbb1"),
		Transactions: []config.Transaction{
			{ // legacy, with calldata
				Hash:  common.HexToHash("0xdead01"),
				From:  addr("0x9ec1b68b660d5d1b7a8ede382233da7674434c22"),
				To:    addr("0x738c03d05dc60693a77c0da0ca7f40018e80a248"),
				Nonce: 3, Value: big.NewInt(2_000_000_000_000_000),
				GasPrice: big.NewInt(35_000_000_000), ChainID: big.NewInt(7000700),
				Type: 0, Data: []byte{0xde, 0xad, 0xbe, 0xef},
			},
			{ // type-2 contract creation: nil To, nil Value, MaxFee fallback
				Hash:  common.HexToHash("0xdead02"),
				From:  addr("0x9ec1b68b660d5d1b7a8ede382233da7674434c22"),
				To:    nil,
				Nonce: 4, Value: nil,
				MaxFee: big.NewInt(42_000_000_000), ChainID: big.NewInt(7000700),
				Type: 2,
			},
		},
	}
}

const goldenReason = "insufficient yes votes: 3 of 7 valid, need 5 (committee size 7)"
const goldenDetail = "12D3KooWabc: bad nonce"

// The exact bytes jmdn puts on the wire, minus the rejected_at timestamp
// (which is generated per call). The orchestrator decodes this with
// DisallowUnknownFields, so this literal is a contract, not a snapshot.
const goldenWireJSON = `{"block_number":4211,"block_hash":"0x000000000000000000000000000000000000000000000000000000000000bbb1","reason":"insufficient yes votes: 3 of 7 valid, need 5 (committee size 7)","detail":"12D3KooWabc: bad nonce","rejected_at":"REDACTED","transactions":[{"hash":"0x0000000000000000000000000000000000000000000000000000000000dead01","from":"0x9EC1b68b660D5d1B7a8edE382233Da7674434c22","to":"0x738c03D05Dc60693A77c0dA0CA7f40018e80a248","nonce":3,"value":"2000000000000000","gas_price":"35000000000","chain_id":"7000700","type":0,"data":"0xdeadbeef"},{"hash":"0x0000000000000000000000000000000000000000000000000000000000dead02","from":"0x9EC1b68b660D5d1B7a8edE382233Da7674434c22","to":"","nonce":4,"value":"","gas_price":"42000000000","chain_id":"7000700","type":2,"data":""}]}`

func TestReportGoldenWire(t *testing.T) {
	rep := build(goldenBlock(), goldenReason, goldenDetail)
	rep.RejectedAt = "REDACTED" // generated per call; not part of the contract

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != goldenWireJSON {
		t.Fatalf("wire format changed.\n got: %s\nwant: %s\n\nIf this change is intended, update goldenWireJSON here AND the copy in the orchestrator's cmd/orchestrator/consensus_reject_api_test.go — it decodes with DisallowUnknownFields and will 400 on drift.",
			raw, goldenWireJSON)
	}
}

func assertKeys(t *testing.T, what string, got map[string]json.RawMessage, want []string) {
	t.Helper()
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != len(want) {
		t.Fatalf("%s keys = %v, want %v", what, keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("%s keys = %v, want %v", what, keys, want)
		}
	}
}

// Amounts must be base-10. The orchestrator parses them with SetString(s, 10);
// a hex string would degrade to 0 there, silently misreporting the value.
func TestReportAmountsAreDecimal(t *testing.T) {
	rep := build(sampleBlock(), "r", "")
	tx := rep.Transactions[0]

	if tx.Value != "2000000000000000" {
		t.Fatalf("value = %q, want decimal 2000000000000000", tx.Value)
	}
	if tx.GasPrice != "35000000000" {
		t.Fatalf("gas_price = %q, want decimal 35000000000", tx.GasPrice)
	}
	if tx.ChainID != "7000700" {
		t.Fatalf("chain_id = %q, want decimal 7000700", tx.ChainID)
	}
	for _, s := range []string{tx.Value, tx.GasPrice, tx.ChainID} {
		if _, ok := new(big.Int).SetString(s, 10); !ok {
			t.Fatalf("%q is not parseable as base-10 — the orchestrator would store 0", s)
		}
	}
	if tx.Data != "0xdeadbeef" {
		t.Fatalf("data = %q, want 0x-prefixed hex", tx.Data)
	}
	if tx.Hash != "0x000000000000000000000000000000000000000000000000000000000000dead01"[:66] {
		// Hash must be the 0x-prefixed 32-byte form the orchestrator keys on.
		if len(tx.Hash) != 66 || tx.Hash[:2] != "0x" {
			t.Fatalf("hash = %q, want 0x-prefixed 32-byte hex", tx.Hash)
		}
	}
}

// Type-2 (1559) transactions carry MaxFee, not GasPrice. The orchestrator
// applies the same fallback, so the reported gasPrice must match it.
func TestReportFallsBackToMaxFee(t *testing.T) {
	blk := sampleBlock()
	blk.Transactions[0].GasPrice = nil
	blk.Transactions[0].MaxFee = big.NewInt(42_000_000_000)
	blk.Transactions[0].Type = 2

	tx := build(blk, "r", "").Transactions[0]
	if tx.GasPrice != "42000000000" {
		t.Fatalf("gas_price = %q, want the max_fee fallback 42000000000", tx.GasPrice)
	}
	if tx.Type != 2 {
		t.Fatalf("type = %d, want 2", tx.Type)
	}
}

// Contract creation has a nil To. It must serialize as empty, not panic.
func TestReportHandlesContractCreationAndNilFields(t *testing.T) {
	blk := sampleBlock()
	blk.Transactions[0].To = nil
	blk.Transactions[0].Value = nil
	blk.Transactions[0].GasPrice = nil
	blk.Transactions[0].MaxFee = nil
	blk.Transactions[0].ChainID = nil
	blk.Transactions[0].Data = nil

	tx := build(blk, "r", "").Transactions[0]
	if tx.To != "" || tx.Value != "" || tx.GasPrice != "" || tx.ChainID != "" || tx.Data != "" {
		t.Fatalf("nil fields must render empty, got %+v", tx)
	}
}

func TestReportTruncatesReason(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'A'
	}
	rep := build(sampleBlock(), string(long), string(long))
	if len(rep.Reason) > maxReasonLen || len(rep.Detail) > maxReasonLen {
		t.Fatalf("reason/detail not truncated: %d/%d", len(rep.Reason), len(rep.Detail))
	}
}

// A disabled reporter must be a silent no-op — reporting can never be allowed
// to affect consensus.
func TestSendIsNoOpWhenDisabled(t *testing.T) {
	// Enabled() resolves the reporter with NO panic guard, so this is the real
	// assertion: settings.Get() panics when Load() was never called, and this
	// code runs inside a consensus round. The reporter must gate on
	// settings.IsLoaded() instead. If that gate regresses, this line panics.
	if Enabled() {
		t.Skip("orchestrator callback is configured in this environment; skipping no-op assertion")
	}

	// And Send must simply return rather than reaching a nil reporter.
	Send(sampleBlock(), "quorum not reached", "")
	Send(nil, "quorum not reached", "")
}

// 5xx and transport failures are retried; the report must eventually land.
func TestSendRetriesServerErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "k" {
			t.Errorf("missing shared secret header")
		}
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := &reporter{
		url: srv.URL, apiKey: "k", maxAttempts: 3,
		client: &http.Client{Timeout: 2 * time.Second},
	}
	r.send(build(sampleBlock(), "quorum not reached", ""))

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("attempts = %d, want 3 (two 5xx retries then success)", got)
	}
}

// A 4xx is the orchestrator refusing the request itself (bad secret, bad body).
// Retrying cannot help and would just amplify the noise.
func TestSendDoesNotRetryClientErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	r := &reporter{
		url: srv.URL, apiKey: "wrong", maxAttempts: 3,
		client: &http.Client{Timeout: 2 * time.Second},
	}
	r.send(build(sampleBlock(), "quorum not reached", ""))

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("attempts = %d, want 1 — a 401 must not be retried", got)
	}
}

// The queue drops rather than blocking: a stalled orchestrator must never stall
// the consensus goroutine.
func TestEnqueueDropsWhenFull(t *testing.T) {
	r := &reporter{queue: make(chan *Report, 1)} // no drain goroutine
	done := make(chan struct{})
	go func() {
		for i := 0; i < QueueCapacity+10; i++ {
			r.enqueue(&Report{BlockNumber: uint64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue blocked on a full queue — this would stall consensus")
	}
}

// An empty block carries nothing to mark failed and the orchestrator rejects an
// empty list, so it must not be sent.
func TestSendSkipsEmptyBlock(t *testing.T) {
	blk := sampleBlock()
	blk.Transactions = nil
	Send(blk, "quorum not reached", "") // must not panic or enqueue
}
