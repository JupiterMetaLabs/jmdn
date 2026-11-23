package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/store"
	"gossipnode/internal/pendingcommit"
	"gossipnode/internal/roundlock"
	"gossipnode/messaging/BlockProcessing"

	"github.com/gin-gonic/gin"
)

// With no config loaded the clear endpoint authorizes nothing.
func TestClearRoundLedger_FailsClosedWithoutConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterConsensusAdminRoutes(r)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/consensus/round-ledger/clear", strings.NewReader(`{"height":5}`))
	req.Header.Set("Authorization", "Bearer anything")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
}

func TestClearRoundLedger_RequiresHeight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/c", handleClearRoundLedger)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/c", strings.NewReader(`{}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
}

// By default only the vote ledger is cleared; the signing ledger needs the
// explicit flag.
func TestClearRoundLedger_SigningLedgerOnlyOnRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prevV, prevD := roundlock.Votes, roundlock.Default
	roundlock.Votes, roundlock.Default = roundlock.NewLedger(), roundlock.NewLedger()
	t.Cleanup(func() { roundlock.Votes, roundlock.Default = prevV, prevD })
	const h = 77
	roundlock.Votes.TryLockFresh(roundlock.Round{Height: h, Period: 3}, "X/ch")
	roundlock.Default.TryLockFresh(roundlock.Round{Height: h, Period: 3}, "X/ch")

	r := gin.New()
	r.POST("/c", handleClearRoundLedger)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/c", strings.NewReader(`{"height":77}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "3=X/ch") {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if _, ok := roundlock.Default.Signed(roundlock.Round{Height: h, Period: 3}); !ok {
		t.Fatal("signing ledger cleared without include_signing_ledger")
	}
	if ok, _, _, _ := roundlock.Default.TryLockFresh(roundlock.Round{Height: h, Period: 3}, "Y/ch"); ok {
		t.Fatal("a second block was signable at a signed round")
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/c", strings.NewReader(`{"height":77,"include_signing_ledger":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if _, ok := roundlock.Default.Signed(roundlock.Round{Height: h, Period: 3}); ok {
		t.Fatal("signing ledger kept with include_signing_ledger")
	}
}

// haltRecordStore holds the node's block-apply halt record and nothing else.
type haltRecordStore struct {
	store.ThebeHandle
	kv map[string][]byte
}

func (h *haltRecordStore) GetSyncKV(k string) ([]byte, error) { return h.kv[k], nil }
func (h *haltRecordStore) PutSyncKV(k string, v []byte) error {
	h.kv[k] = v
	return nil
}

// Both apply-halt endpoints are admin-only and fail closed without config.
func TestApplyHaltEndpoints_FailClosedWithoutConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterConsensusAdminRoutes(r)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/consensus/apply-halt", nil),
		httptest.NewRequest(http.MethodPost, "/consensus/apply-halt/clear", nil),
	} {
		req.Header.Set("Authorization", "Bearer anything")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status %d", req.Method, req.URL.Path, w.Code)
		}
	}
}

// Status reports a recorded halt; clear lifts it and erases the record.
func TestApplyHaltEndpoints_StatusAndClear(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st := &haltRecordStore{kv: map[string][]byte{"block_apply_halt": []byte("block 8 failed")}}
	DB_OPs.SetGlobalHandle(st)
	t.Cleanup(func() { DB_OPs.SetGlobalHandle(nil) })
	// The halt is process-wide: leave it cleared, and re-read on the next run.
	t.Cleanup(func() {
		if _, err := BlockProcessing.ClearApplyHalt(); err != nil {
			t.Errorf("clear the halt: %v", err)
		}
	})

	r := gin.New()
	r.GET("/s", handleApplyHaltStatus)
	r.POST("/c", handleClearApplyHalt)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/s", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"halted":true`) || !strings.Contains(w.Body.String(), "block 8 failed") || !strings.Contains(w.Body.String(), `"recorded":true`) {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/c", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "block 8 failed") {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if len(st.kv["block_apply_halt"]) != 0 {
		t.Fatal("clear left the halt record in the store")
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/s", nil))
	if !strings.Contains(w.Body.String(), `"halted":false`) {
		t.Fatalf("after clear: %s", w.Body.String())
	}
}

// Both pending-commit endpoints are admin-only and fail closed without config.
func TestPendingCommitEndpoints_FailClosedWithoutConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterConsensusAdminRoutes(r)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/consensus/pending-commit", nil),
		httptest.NewRequest(http.MethodPost, "/consensus/pending-commit/drop", strings.NewReader(`{"height":9,"block_hash":"0x9"}`)),
	} {
		req.Header.Set("Authorization", "Bearer anything")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status %d", req.Method, req.URL.Path, w.Code)
		}
	}
}

// Status reports the record; drop removes it only when height and block hash
// both match, after which proposals at the height are no longer refused.
func TestPendingCommitEndpoints_StatusAndDrop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := pendingcommit.Default
	pendingcommit.Default = pendingcommit.New()
	t.Cleanup(func() { pendingcommit.Default = prev })
	const hash = "0x00000000000000000000000000000000000000000000000000000000000000aa"
	if err := pendingcommit.Default.Save(pendingcommit.Record{Height: 90, BlockHash: hash, Block: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	// Wired dropper (the sequencer wires Sequencer.DropPendingCommit): record
	// each successful drop and delegate to the tracker.
	var dropped []pendingcommit.Record
	SetPendingCommitDropper(func(h uint64, hash string) (*pendingcommit.Record, error) {
		rec, err := pendingcommit.Default.DropMatching(h, hash)
		if err == nil {
			dropped = append(dropped, *rec)
		}
		return rec, err
	}, nil)
	t.Cleanup(func() { SetPendingCommitDropper(nil, nil) })

	r := gin.New()
	r.GET("/s", handlePendingCommitStatus)
	r.POST("/d", handleDropPendingCommit)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/d", strings.NewReader(body)))
		return w
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/s", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"pending":true`) || !strings.Contains(w.Body.String(), hash) {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}

	if w := post(`{"height":90}`); w.Code != http.StatusBadRequest {
		t.Fatalf("missing block_hash: status %d", w.Code)
	}
	if w := post(`{"height":91,"block_hash":"` + hash + `"}`); w.Code != http.StatusConflict {
		t.Fatalf("wrong height: status %d", w.Code)
	}
	if w := post(`{"height":90,"block_hash":"0xbb"}`); w.Code != http.StatusConflict {
		t.Fatalf("wrong hash: status %d", w.Code)
	}
	if blocked, _, _ := pendingcommit.Default.BlocksProposal(90, 89); !blocked {
		t.Fatal("a refused drop must keep the record")
	}
	if len(dropped) != 0 {
		t.Fatalf("a refused drop must not drop anything, got %d drops", len(dropped))
	}

	if w := post(`{"height":90,"block_hash":"` + strings.ToUpper(hash[2:]) + `"}`); w.Code != http.StatusConflict {
		t.Fatalf("hash without 0x prefix: status %d", w.Code)
	}
	if w := post(`{"height":90,"block_hash":"` + hash + `"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"dropped":true`) {
		t.Fatalf("drop: %d %s", w.Code, w.Body.String())
	}
	if blocked, _, _ := pendingcommit.Default.BlocksProposal(90, 89); blocked {
		t.Fatal("proposals at the height must be accepted after the drop")
	}
	if len(dropped) != 1 || dropped[0].Height != 90 || dropped[0].BlockHash != hash {
		t.Fatalf("the dropper must drop the record once, got %+v", dropped)
	}
	if w := post(`{"height":90,"block_hash":"` + hash + `"}`); w.Code != http.StatusNotFound {
		t.Fatalf("second drop: status %d", w.Code)
	}
}

// Without a wired dropper (no sequencer in this process) the record is
// dropped directly, with the same matching rules.
func TestPendingCommitDrop_UnwiredDropsDirectly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := pendingcommit.Default
	pendingcommit.Default = pendingcommit.New()
	t.Cleanup(func() { pendingcommit.Default = prev })
	SetPendingCommitDropper(nil, nil)
	const hash = "0x00000000000000000000000000000000000000000000000000000000000000cc"
	if err := pendingcommit.Default.Save(pendingcommit.Record{Height: 91, BlockHash: hash, Block: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.POST("/d", handleDropPendingCommit)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/d", strings.NewReader(`{"height":91,"block_hash":"0xdd"}`)))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), hash) {
		t.Fatalf("mismatch: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/d", strings.NewReader(`{"height":91,"block_hash":"`+hash+`"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("drop: %d %s", w.Code, w.Body.String())
	}
	if rec, _ := pendingcommit.Default.Current(); rec != nil {
		t.Fatal("the record must be gone")
	}
}

// kvStore is a minimal pendingcommit.Store.
type kvStore struct{ kv map[string][]byte }

func (s *kvStore) Get(k string) ([]byte, error) { return s.kv[k], nil }
func (s *kvStore) Put(k string, v []byte) error {
	s.kv[k] = v
	return nil
}

// An undecodable record is reported by the status endpoint and removed by
// {"unreadable": true}; that body is refused for a readable record.
func TestPendingCommitEndpoints_UnreadableRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := pendingcommit.Default
	t.Cleanup(func() { pendingcommit.Default = prev })
	SetPendingCommitDropper(nil, nil)
	st := &kvStore{kv: map[string][]byte{pendingcommit.DefaultKey: []byte("{corrupt")}}
	pendingcommit.Default = pendingcommit.New()
	pendingcommit.Default.SetStore(st, "")

	r := gin.New()
	r.GET("/s", handlePendingCommitStatus)
	r.POST("/d", handleDropPendingCommit)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/s", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unreadable":true`) {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/d", strings.NewReader(`{"unreadable":true}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"dropped":true`) {
		t.Fatalf("drop unreadable: %d %s", w.Code, w.Body.String())
	}
	if blocked, _, _ := pendingcommit.Default.BlocksProposal(5, 4); blocked {
		t.Fatal("proposals must be accepted after the drop")
	}

	pendingcommit.Default = pendingcommit.New()
	pendingcommit.Default.SetStore(&kvStore{kv: map[string][]byte{}}, "")
	_ = pendingcommit.Default.Save(pendingcommit.Record{Height: 7, BlockHash: "0x7", Block: []byte(`{}`)})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/d", strings.NewReader(`{"unreadable":true}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("a readable record must be refused: %d %s", w.Code, w.Body.String())
	}
}
