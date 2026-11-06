package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
