package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestThebeStatus_Disabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/debug/thebe/status", nil)

	s := &HTTPServer{}
	s.thebeStatus(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want %d got %d", http.StatusOK, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("body should report enabled=false, got %q", rec.Body.String())
	}
}

func TestThebeGetAccount_NotEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/debug/thebe/accounts/0xaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaA", nil)
	c.Params = gin.Params{gin.Param{Key: "address", Value: "0xaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaA"}}

	s := &HTTPServer{}
	s.thebeGetAccount(c)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

func TestThebeGetAccountNonce_NotEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/debug/thebe/accounts/0xaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaA/nonce", nil)
	c.Params = gin.Params{gin.Param{Key: "address", Value: "0xaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaA"}}

	s := &HTTPServer{}
	s.thebeGetAccountNonce(c)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

func TestThebeAccountTransactions_NotEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/debug/thebe/accounts/0xaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaA/transactions?limit=5&offset=0", nil)
	c.Params = gin.Params{gin.Param{Key: "address", Value: "0xaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaA"}}

	s := &HTTPServer{}
	s.thebeAccountTransactions(c)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d got %d", http.StatusServiceUnavailable, rec.Code)
	}
}
