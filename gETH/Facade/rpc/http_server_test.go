package rpc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gossipnode/DB_OPs/dualdb"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type noOpPrimary struct{}

func (noOpPrimary) Create(*config.PooledConnection, string, interface{}) error { return nil }
func (noOpPrimary) SafeCreate(*config.ImmuClient, string, interface{}) error   { return nil }
func (noOpPrimary) BatchCreate(*config.PooledConnection, map[string]interface{}) error {
	return nil
}
func (noOpPrimary) CreateAccount(*config.PooledConnection, string, common.Address, map[string]interface{}) error {
	return nil
}
func (noOpPrimary) UpdateAccountBalance(*config.PooledConnection, common.Address, string) error {
	return nil
}
func (noOpPrimary) StoreZKBlock(*config.PooledConnection, *config.ZKBlock) error { return nil }

type noOpShadow struct{ noOpPrimary }

func TestDualDBReport_Disabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/debug/dualdb/report", nil)

	s := &HTTPServer{}
	s.DualDBReport(c)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d got %d", http.StatusServiceUnavailable, rec.Code)
	}
	if got := rec.Body.String(); got != "dualdb not enabled" {
		t.Fatalf("body: want %q got %q", "dualdb not enabled", got)
	}
}

func TestDualDBReport_Enabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/debug/dualdb/report", nil)

	d := dualdb.New(noOpPrimary{}, noOpShadow{}, dualdb.Config{Enabled: true, ShadowAsync: false}, zap.NewNop())
	s := (&HTTPServer{}).WithDualDB(d)
	s.DualDBReport(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want %d got %d", http.StatusOK, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: want application/json got %q", ct)
	}
	if body := rec.Body.String(); body == "" {
		t.Fatal("expected non-empty JSON body")
	}
}
