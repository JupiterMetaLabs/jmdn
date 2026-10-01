package gatekeeper

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"gossipnode/config/settings"
)

func TestValidateAdminAuthHeader_AcceptsToken(t *testing.T) {
	os.Setenv("ADMIN_TOKEN", "test-admin-secret")
	defer os.Unsetenv("ADMIN_TOKEN")
	cfg := settings.DefaultSecurityConfig()
	cfg.ResolveTokens()
	if err := ValidateAdminAuthHeader("Bearer test-admin-secret", &cfg); err != nil {
		t.Fatalf("expected valid token: %v", err)
	}
	if err := ValidateAdminAuthHeader("Bearer wrong", &cfg); err == nil {
		t.Fatal("wrong token must fail")
	}
	if err := ValidateAdminAuthHeader("", &cfg); err == nil {
		t.Fatal("missing auth must fail")
	}
}

func TestAdminHTTPMiddleware_RejectsUnauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	os.Setenv("ADMIN_TOKEN", "sync-secret")
	defer os.Unsetenv("ADMIN_TOKEN")
	cfg := settings.DefaultSecurityConfig()
	cfg.ResolveTokens()

	r := gin.New()
	r.POST("/sync/reconcile", AdminHTTPMiddleware(&cfg), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/sync/reconcile", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated reconcile status=%d, want 401/403", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/sync/reconcile", nil)
	req2.Header.Set("Authorization", "Bearer sync-secret")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("authenticated reconcile status=%d, want 200 (body=%s)", w2.Code, w2.Body.String())
	}
}
