package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
