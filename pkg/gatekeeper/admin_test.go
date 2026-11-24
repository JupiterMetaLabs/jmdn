package gatekeeper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"gossipnode/config/settings"
)

func adminCfg(token string) *settings.SecurityConfig {
	cfg := settings.DefaultSecurityConfig()
	cfg.AdminToken = token
	cfg.ResolveTokens()
	return &cfg
}

func TestValidateAdminAuthHeader(t *testing.T) {
	cfg := adminCfg("s3cret-admin")
	if err := ValidateAdminAuthHeader("Bearer s3cret-admin", cfg); err != nil {
		t.Fatalf("valid admin token rejected: %v", err)
	}
	for _, h := range []string{"", "Bearer wrong", "s3cret-admin"} {
		if ValidateAdminAuthHeader(h, cfg) == nil {
			t.Errorf("header %q accepted", h)
		}
	}
	if ValidateAdminAuthHeader("Bearer ", adminCfg("")) == nil {
		t.Error("an empty admin token must authorize nothing")
	}
	if ValidateAdminAuthHeader("Bearer anything", nil) == nil {
		t.Error("nil config must authorize nothing")
	}
}

func TestAdminHTTPMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/op", AdminHTTPMiddleware(adminCfg("tok")), func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, tc := range []struct {
		header string
		want   int
	}{{"", http.StatusUnauthorized}, {"Basic x", http.StatusUnauthorized}, {"Bearer bad", http.StatusForbidden}, {"Bearer tok", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodPost, "/op", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("header %q: status %d, want %d", tc.header, rec.Code, tc.want)
		}
	}
}
