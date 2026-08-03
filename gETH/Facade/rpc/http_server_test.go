package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

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
