package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"gossipnode/config/settings"
	"gossipnode/pkg/gatekeeper"
)

// budgetLimiter admits up to budget requests in total.
type budgetLimiter struct{ budget, charged int }

func (b *budgetLimiter) Allow(ctx context.Context, svc, ip string) bool {
	return b.AllowN(ctx, svc, ip, 1)
}
func (b *budgetLimiter) AllowN(_ context.Context, _, _ string, n int) bool {
	if b.charged+n > b.budget {
		return false
	}
	b.charged += n
	return true
}
func (b *budgetLimiter) RetryAfterSeconds(string, string) int { return 3 }

func batchBody(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// A batch is charged one token per member, not one per HTTP request.
func TestBatchChargedPerMember(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := settings.DefaultSecurityConfig()
	lim := &budgetLimiter{budget: 10}
	s := &HTTPServer{gate: gatekeeper.NewGinMiddleware(&cfg, lim, nil)}

	// The middleware has already charged 1 for this HTTP request.
	lim.charged = 1
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(batchBody(20)))
	s.handleJSONRPC(c)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "3" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Error == nil || resp.Error.Code != -32005 {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if lim.charged != 1 {
		t.Fatalf("rejected batch consumed tokens: charged=%d", lim.charged)
	}
}
