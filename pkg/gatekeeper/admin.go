package gatekeeper

import (
	"net/http"

	"gossipnode/config/settings"

	"github.com/gin-gonic/gin"
)

// AdminHTTPMiddleware returns a Gin handler that requires the admin_http token
// policy (Authorization: Bearer $ADMIN_TOKEN, or JWT when configured).
// Used for expensive / admin-only HTTP routes such as POST /sync/reconcile.
func AdminHTTPMiddleware(cfg *settings.SecurityConfig) gin.HandlerFunc {
	if cfg == nil {
		cfg = &settings.SecurityConfig{}
	}
	policy, ok := cfg.Services[settings.ServiceAdminHTTP]
	if !ok {
		policy = settings.Policy{
			AuthType: settings.AuthTypeToken,
			TokenEnv: "ADMIN_TOKEN",
		}
	}
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if _, err := ValidateAuthHeader(authHeader, policy, cfg); err != nil {
			status := http.StatusUnauthorized
			if err != errAuthRequired && err != errInvalidFormat {
				status = http.StatusForbidden
			}
			c.AbortWithStatusJSON(status, gin.H{"error": err.Error()})
			return
		}
		c.Next()
	}
}

// ValidateAdminAuthHeader checks an Authorization header against the admin_http
// policy. Shared by JSON-RPC expensive-method gating (JMDN-H05).
func ValidateAdminAuthHeader(authHeader string, cfg *settings.SecurityConfig) error {
	if cfg == nil {
		cfg = &settings.SecurityConfig{}
	}
	policy, ok := cfg.Services[settings.ServiceAdminHTTP]
	if !ok {
		policy = settings.Policy{
			AuthType: settings.AuthTypeToken,
			TokenEnv: "ADMIN_TOKEN",
		}
	}
	_, err := ValidateAuthHeader(authHeader, policy, cfg)
	return err
}
