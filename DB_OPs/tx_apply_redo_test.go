package DB_OPs

import (
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs/store"
)

// redoHandle adds a sync-state KV (with an optional injected marker-write
// failure) to the in-memory accounts double.
type redoHandle struct {
	memAccountsHandle
	kv            map[string][]byte
	failMarkerPut bool
}

func (r *redoHandle) GetSyncKV(key string) ([]byte, error) {
	v, ok := r.kv[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (r *redoHandle) PutSyncKV(key string, value []byte) error {
	if r.failMarkerPut && strings.HasPrefix(key, "tx_processed:") {
		return errors.New("injected marker failure")
	}
	r.kv[key] = append([]byte(nil), value...)
	return nil
}

func newRedoHandle(t *testing.T, addr common.Address, balance string) *redoHandle {
	t.Helper()
	h := &redoHandle{
		memAccountsHandle: memAccountsHandle{accounts: map[string]*store.Account{
			addr.Hex(): {Address: addr, Balance: balance, AccountType: "user"},
		}},
		kv: map[string][]byte{},
	}
	SetGlobalHandle(h)
	t.Cleanup(func() { SetGlobalHandle(nil) })
	return h
}

var redoAddr = common.HexToAddress("0x7777777777777777777777777777777777777777")

func TestApplyTxAtomic_ClearsRedoOnSuccess(t *testing.T) {
	h := newRedoHandle(t, redoAddr, "100")
	if err := ApplyTxAtomic(nil, []*Account{{Address: redoAddr, Balance: "90"}}, "0xaa", 1); err != nil {
		t.Fatal(err)
	}
	if got := h.accounts[redoAddr.Hex()].Balance; got != "90" {
		t.Fatalf("balance = %s, want 90", got)
	}
	if string(h.kv[TxProcessedKey("0xaa")]) != "1" {
		t.Fatal("marker not written")
	}
	if len(h.kv[txApplyRedoKey]) != 0 {
		t.Fatal("redo record left behind after success")
	}
}

// Crash after the account write, before the marker: recovery must finish the
// tx with the ORIGINAL post-state, and the tx must then read as applied so the
// replay path skips it instead of debiting the already-debited base again.
func TestRecover_RollsForwardInterruptedApply(t *testing.T) {
	h := newRedoHandle(t, redoAddr, "100")
	h.failMarkerPut = true
	// Simulate the crash: write the redo, then run the effects with the
	// marker failing and do NOT run the error-path clear.
	redo := `{"tx_hash":"0xbb","applied_at":7,"docs":[{"address":"` + redoAddr.Hex() + `","balance":"90"}]}`
	h.kv[txApplyRedoKey] = []byte(redo)
	_ = writeTxEffects(h, []*Account{{Address: redoAddr, Balance: "90"}}, []string{"0xbb"}, 7)
	if h.accounts[redoAddr.Hex()].Balance != "90" || h.kv[TxProcessedKey("0xbb")] != nil {
		t.Fatal("setup: expected debited account with no marker")
	}

	h.failMarkerPut = false
	got, err := RecoverPendingTxApply()
	if err != nil {
		t.Fatal(err)
	}
	if got != "0xbb" {
		t.Fatalf("recovered %q, want 0xbb", got)
	}
	if h.accounts[redoAddr.Hex()].Balance != "90" {
		t.Fatalf("balance = %s, want 90 (single debit)", h.accounts[redoAddr.Hex()].Balance)
	}
	applied, err := IsMarkerApplied(nil, TxProcessedKey("0xbb"))
	if err != nil || !applied {
		t.Fatalf("marker applied=%v err=%v, want applied", applied, err)
	}
	if len(h.kv[txApplyRedoKey]) != 0 {
		t.Fatal("redo not cleared after recovery")
	}
	// Second run is a no-op.
	if got, err := RecoverPendingTxApply(); err != nil || got != "" {
		t.Fatalf("second recover = %q, %v", got, err)
	}
}

func TestApplyTxAtomic_ErrorClearsRedo(t *testing.T) {
	h := newRedoHandle(t, redoAddr, "100")
	h.failMarkerPut = true
	if err := ApplyTxAtomic(nil, []*Account{{Address: redoAddr, Balance: "90"}}, "0xcc", 1); err == nil {
		t.Fatal("expected error")
	}
	if len(h.kv[txApplyRedoKey]) != 0 {
		t.Fatal("redo must be cleared on error so rollback is not undone at boot")
	}
}

func TestApplyTxAtomic_RefusesWhileApplyPending(t *testing.T) {
	h := newRedoHandle(t, redoAddr, "100")
	h.kv[txApplyRedoKey] = []byte(`{"tx_hash":"0xdd","applied_at":1,"docs":[{"address":"` + redoAddr.Hex() + `","balance":"50"}]}`)
	err := ApplyTxAtomic(nil, []*Account{{Address: redoAddr, Balance: "80"}}, "0xee", 1)
	if err == nil || !strings.Contains(err.Error(), "0xdd") {
		t.Fatalf("err = %v, want refusal naming pending tx", err)
	}
	if h.accounts[redoAddr.Hex()].Balance != "100" {
		t.Fatal("no account write may happen while an apply is pending")
	}
}

func TestRecover_SkipsRevokedTx(t *testing.T) {
	h := newRedoHandle(t, redoAddr, "100")
	h.kv[txApplyRedoKey] = []byte(`{"tx_hash":"0xff","applied_at":1,"docs":[{"address":"` + redoAddr.Hex() + `","balance":"10"}]}`)
	h.kv[TxProcessedKey("0xff")] = []byte("-1")
	got, err := RecoverPendingTxApply()
	if err != nil || got != "" {
		t.Fatalf("recover = %q, %v; want no roll-forward", got, err)
	}
	if h.accounts[redoAddr.Hex()].Balance != "100" {
		t.Fatal("revoked tx must not be rolled forward")
	}
	if len(h.kv[txApplyRedoKey]) != 0 {
		t.Fatal("redo not cleared")
	}
}

func TestDiscardPendingTxApply(t *testing.T) {
	h := newRedoHandle(t, redoAddr, "100")
	h.kv[txApplyRedoKey] = []byte(`{"tx_hash":"0x11","applied_at":1,"docs":[{"address":"` + redoAddr.Hex() + `","balance":"10"}]}`)
	if err := DiscardPendingTxApply(); err != nil {
		t.Fatal(err)
	}
	if got, err := RecoverPendingTxApply(); err != nil || got != "" {
		t.Fatalf("recover after discard = %q, %v", got, err)
	}
	if h.accounts[redoAddr.Hex()].Balance != "100" {
		t.Fatal("discarded apply was rolled forward")
	}
}
