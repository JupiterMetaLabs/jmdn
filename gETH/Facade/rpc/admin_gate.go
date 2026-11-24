package rpc

import (
	"context"
	"fmt"

	"gossipnode/config/settings"
	"gossipnode/pkg/gatekeeper"
)

// authHeaderCtxKey carries the caller's Authorization header from the HTTP or
// WebSocket front-end into JSON-RPC handlers, for admin-gated methods.
type authHeaderCtxKey struct{}

func withAuthHeader(ctx context.Context, header string) context.Context {
	return context.WithValue(ctx, authHeaderCtxKey{}, header)
}

func authHeaderFrom(ctx context.Context) string {
	h, _ := ctx.Value(authHeaderCtxKey{}).(string)
	return h
}

// errMethodNotAvailable is the JSON-RPC code for a disabled or unauthorized
// admin-only method.
const errMethodNotAvailable = -32001

// adminMethodAllowed reports whether an admin-only JSON-RPC method may run:
// its feature flag must be on and the caller must present the admin token.
// Settings not loaded counts as disabled.
func adminMethodAllowed(ctx context.Context, method string, enabled func(settings.FeatureSettings) bool) error {
	if !settings.IsLoaded() {
		return fmt.Errorf("%s is disabled", method)
	}
	cfg := settings.Get()
	if !enabled(cfg.Features) {
		return fmt.Errorf("%s is disabled on this node", method)
	}
	if err := gatekeeper.ValidateAdminAuthHeader(authHeaderFrom(ctx), &cfg.Security); err != nil {
		return fmt.Errorf("%s requires the admin token: %w", method, err)
	}
	return nil
}
