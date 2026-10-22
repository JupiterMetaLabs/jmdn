package gatekeeper

import (
	"errors"
	"net/http"

	"gossipnode/config/settings"

	"github.com/gin-gonic/gin"
)

// adminPolicy returns the admin_http policy, falling back to token auth with
// ADMIN_TOKEN when the config does not define one.
func adminPolicy(cfg *settings.SecurityConfig) settings.Policy {
	if p, ok := cfg.Services[settings.ServiceAdminHTTP]; ok {
		return p
	}
	return settings.Policy{AuthType: settings.AuthTypeToken, TokenEnv: "ADMIN_TOKEN"}
}

// ValidateAdminAuthHeader checks an Authorization header against the
// admin_http policy. It fails closed: a nil config or an empty admin token
// authorizes nothing.
func ValidateAdminAuthHeader(authHeader string, cfg *settings.SecurityConfig) error {
	if cfg == nil {
		return errInvalidToken
	}
	_, err := ValidateAuthHeader(authHeader, settings.ServiceAdminHTTP, adminPolicy(cfg), cfg)
	return err
}

// AdminHTTPMiddleware rejects requests that do not carry a valid admin token:
// 401 when the header is missing or malformed, 403 when the token is wrong.
func AdminHTTPMiddleware(cfg *settings.SecurityConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := ValidateAdminAuthHeader(c.GetHeader("Authorization"), cfg); err != nil {
			status := http.StatusForbidden
			if errors.Is(err, errAuthRequired) || errors.Is(err, errInvalidFormat) {
				status = http.StatusUnauthorized
			}
			c.AbortWithStatusJSON(status, gin.H{"error": err.Error()})
			return
		}
		c.Next()
	}
}
