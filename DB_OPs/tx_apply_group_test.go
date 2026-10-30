package DB_OPs

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs/store"
)

var redoAddr2 = common.HexToAddress("0x8888888888888888888888888888888888888888")

// markerFailHandle fails the n-th tx_processed write (1-based) and every one
// after it, to leave a group part-way through its markers.
type markerFailHandle struct {
	*redoHandle
	failFrom int
	puts     int
}

func (m *markerFailHandle) PutSyncKV(key string, value []byte) error {
	if strings.HasPrefix(key, "tx_processed:") {
		m.puts++
		if m.failFrom > 0 && m.puts >= m.failFrom {
			return errors.New("injected marker failure")
		}
	}
	return m.redoHandle.PutSyncKV(key, value)
}

func newGroupHandle(t *testing.T) *redoHandle {
	t.Helper()
	h := newRedoHandle(t, redoAddr, "100")
	h.accounts[redoAddr2.Hex()] = &store.Account{Address: redoAddr2, Balance: "5", AccountType: "user"}
	return h
}

func TestApplyTxGroupAtomic_WritesEveryMarkerAndEachAccountOnce(t *testing.T) {
	h := newGroupHandle(t)
	docs := []*Account{{Address: redoAddr, Balance: "70"}, {Address: redoAddr2, Balance: "35"}}
	if err := ApplyTxGroupAtomic(nil, docs, []string{"0x01", "0x02", "0x03"}, 9); err != nil {
		t.Fatal(err)
	}
	if h.accounts[redoAddr.Hex()].Balance != "70" || h.accounts[redoAddr2.Hex()].Balance != "35" {
		t.Fatalf("balances = %s/%s, want 70/35", h.accounts[redoAddr.Hex()].Balance, h.accounts[redoAddr2.Hex()].Balance)
	}
	for _, hash := range []string{"0x01", "0x02", "0x03"} {
		if string(h.kv[TxProcessedKey(hash)]) != "9" {
			t.Fatalf("marker %s = %q, want 9", hash, h.kv[TxProcessedKey(hash)])
		}
	}
	if len(h.kv[txApplyRedoKey]) != 0 {
		t.Fatal("redo record left behind after success")
	}
}

// A single-tx apply must keep writing the redo format older binaries read.
func TestApplyTxGroupAtomic_SingleTxRedoFormatUnchanged(t *testing.T) {
	rec := &txApplyRedo{TxHash: "0xaa", AppliedAt: 1, Docs: []*Account{{Address: redoAddr, Balance: "1"}}}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "tx_hashes") {
		t.Fatalf("single-tx redo carries tx_hashes: %s", b)
	}
}

// Crash after the account writes and the first marker: recovery must finish
// every marker of the group with the original post-state.
func TestRecover_RollsForwardInterruptedGroup(t *testing.T) {
	base := newGroupHandle(t)
	h := &markerFailHandle{redoHandle: base, failFrom: 2}
	SetGlobalHandle(h)

	docs := []*Account{{Address: redoAddr, Balance: "70"}, {Address: redoAddr2, Balance: "35"}}
	hashes := []string{"0x01", "0x02", "0x03"}
	rec, _ := json.Marshal(&txApplyRedo{TxHash: hashes[0], TxHashes: hashes, AppliedAt: 4, Docs: docs})
	base.kv[txApplyRedoKey] = rec
	_ = writeTxEffects(h, docs, hashes, 4)
	if base.kv[TxProcessedKey("0x01")] == nil || base.kv[TxProcessedKey("0x02")] != nil {
		t.Fatal("setup: expected only the first marker written")
	}

	h.failFrom = 0
	got, err := RecoverPendingTxApply()
	if err != nil {
		t.Fatal(err)
	}
	if got != "0x01,0x02,0x03" {
		t.Fatalf("recovered %q", got)
	}
	for _, hash := range hashes {
		if ok, err := IsMarkerApplied(nil, TxProcessedKey(hash)); err != nil || !ok {
			t.Fatalf("marker %s applied=%v err=%v", hash, ok, err)
		}
	}
	if base.accounts[redoAddr.Hex()].Balance != "70" || base.accounts[redoAddr2.Hex()].Balance != "35" {
		t.Fatal("recovery changed the group post-state")
	}
	if len(base.kv[txApplyRedoKey]) != 0 {
		t.Fatal("redo not cleared after recovery")
	}
}

func TestRecover_SkipsGroupWithAnyRevokedTx(t *testing.T) {
	h := newGroupHandle(t)
	hashes := []string{"0x01", "0x02"}
	rec, _ := json.Marshal(&txApplyRedo{TxHash: hashes[0], TxHashes: hashes, AppliedAt: 4,
		Docs: []*Account{{Address: redoAddr, Balance: "1"}}})
	h.kv[txApplyRedoKey] = rec
	h.kv[TxProcessedKey("0x02")] = []byte("-1")
	got, err := RecoverPendingTxApply()
	if err != nil || got != "" {
		t.Fatalf("recover = %q, %v; want no roll-forward", got, err)
	}
	if h.accounts[redoAddr.Hex()].Balance != "100" {
		t.Fatal("rolled-back group was rolled forward")
	}
	if h.kv[TxProcessedKey("0x01")] != nil {
		t.Fatal("marker written for a discarded group")
	}
}

func TestApplyTxGroupAtomic_ErrorClearsRedo(t *testing.T) {
	base := newGroupHandle(t)
	h := &markerFailHandle{redoHandle: base, failFrom: 2}
	SetGlobalHandle(h)
	err := ApplyTxGroupAtomic(nil, []*Account{{Address: redoAddr, Balance: "70"}}, []string{"0x01", "0x02"}, 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(base.kv[txApplyRedoKey]) != 0 {
		t.Fatal("redo must be cleared on error so rollback is not undone at boot")
	}
}

func TestApplyTxGroupAtomic_RejectsBadInput(t *testing.T) {
	newGroupHandle(t)
	docs := []*Account{{Address: redoAddr, Balance: "1"}}
	if err := ApplyTxGroupAtomic(nil, docs, nil, 1); err == nil {
		t.Fatal("want error for no txs")
	}
	if err := ApplyTxGroupAtomic(nil, docs, []string{"0x01", ""}, 1); err == nil {
		t.Fatal("want error for an empty hash")
	}
	if err := ApplyTxGroupAtomic(nil, nil, []string{"0x01"}, 1); err == nil {
		t.Fatal("want error for no docs")
	}
}

func TestReadTxApplyRedo_RejectsInconsistentGroup(t *testing.T) {
	h := newGroupHandle(t)
	h.kv[txApplyRedoKey] = []byte(`{"tx_hash":"0x09","tx_hashes":["0x01","0x02"],"applied_at":1,"docs":[{"address":"` + redoAddr.Hex() + `","balance":"1"}]}`)
	if _, err := readTxApplyRedo(h); err == nil {
		t.Fatal("want malformed-record error when tx_hash is not the first group hash")
	}
}
