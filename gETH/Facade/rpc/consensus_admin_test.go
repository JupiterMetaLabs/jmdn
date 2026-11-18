package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/store"
	"gossipnode/internal/roundlock"

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

	r := gin.New()
	r.GET("/s", handleApplyHaltStatus)
	r.POST("/c", handleClearApplyHalt)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/s", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"halted":true`) || !strings.Contains(w.Body.String(), "block 8 failed") {
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
