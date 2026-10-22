package rpc

import (
	"context"
	"strings"
	"testing"

	"gossipnode/config/settings"
)

func loadSettingsForTest(t *testing.T) *settings.NodeConfig {
	t.Helper()
	t.Chdir(t.TempDir()) // no jmdn.yaml: compiled defaults
	if _, err := settings.Load(); err != nil {
		t.Fatalf("settings.Load: %v", err)
	}
	return settings.Get()
}

func TestAdminMethodAllowed(t *testing.T) {
	cfg := loadSettingsForTest(t)
	solc := func(f settings.FeatureSettings) bool { return f.EnableSolcCompile }
	ctx := context.Background()

	if err := adminMethodAllowed(ctx, "solc_compile", solc); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("default config must disable solc_compile, got %v", err)
	}

	prevFeat, prevTok := cfg.Features.EnableSolcCompile, cfg.Security.AdminToken
	t.Cleanup(func() {
		cfg.Features.EnableSolcCompile, cfg.Security.AdminToken = prevFeat, prevTok
		cfg.Security.ResolveTokens()
	})
	cfg.Features.EnableSolcCompile = true
	cfg.Security.AdminToken = "admin-tok"
	cfg.Security.ResolveTokens()

	if err := adminMethodAllowed(ctx, "solc_compile", solc); err == nil {
		t.Fatal("enabled method must still require the admin token")
	}
	if err := adminMethodAllowed(withAuthHeader(ctx, "Bearer wrong"), "solc_compile", solc); err == nil {
		t.Fatal("wrong token accepted")
	}
	if err := adminMethodAllowed(withAuthHeader(ctx, "Bearer admin-tok"), "solc_compile", solc); err != nil {
		t.Fatalf("admin token rejected: %v", err)
	}
}

func TestSolcCompileDisabledByDefault(t *testing.T) {
	loadSettingsForTest(t)
	h := NewHandlers(nil)
	resp, _ := h.Handle(context.Background(), Request{Method: "solc_compile", Params: []any{map[string]any{"source": "x"}}})
	if resp.Error == nil || resp.Error.Code != errMethodNotAvailable {
		t.Fatalf("resp = %+v, want error %d", resp, errMethodNotAvailable)
	}
}
