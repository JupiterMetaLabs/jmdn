package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/viper"
)

func TestValidateEnvironment(t *testing.T) {
	cases := []struct {
		env   string
		chain int
		ok    bool
	}{
		{"mainnet", MainnetChainID, true},
		{"mainnet", TestnetChainID, true}, // stricter than needed, allowed
		{"testnet", TestnetChainID, true},
		{"devnet", 1337, true},
		{"testnet", MainnetChainID, false}, // locks off on mainnet
		{"devnet", MainnetChainID, false},
		{"", TestnetChainID, false},
		{"prod", TestnetChainID, false},
	}
	for _, c := range cases {
		cfg := DefaultConfig()
		cfg.Network.Environment, cfg.Network.ChainID = c.env, c.chain
		if err := cfg.ValidateEnvironment(); (err == nil) != c.ok {
			t.Errorf("env=%q chain=%d: ok=%v, err=%v", c.env, c.chain, c.ok, err)
		}
	}
}

func repoFile(t *testing.T, name string) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", name)
}

// The shipped profiles declare a consistent environment and chain, and the
// mainnet profile carries a rate limit for the public RPC (the production
// security posture requires one).
func TestShippedProfiles(t *testing.T) {
	for file, want := range map[string]struct {
		env   string
		chain int
	}{
		"jmdn_default.yaml": {"mainnet", MainnetChainID},
		"jmdn_testnet.yaml": {"testnet", TestnetChainID},
	} {
		v := viper.New()
		v.SetConfigFile(repoFile(t, file))
		if err := v.ReadInConfig(); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		var cfg NodeConfig
		if err := v.Unmarshal(&cfg); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if cfg.Network.Environment != want.env || cfg.Network.ChainID != want.chain {
			t.Errorf("%s: environment=%q chain_id=%d, want %q/%d", file, cfg.Network.Environment, cfg.Network.ChainID, want.env, want.chain)
		}
		if err := cfg.ValidateEnvironment(); err != nil {
			t.Errorf("%s: %v", file, err)
		}
		if cfg.Security.GlobalRateLimit <= 0 {
			t.Errorf("%s: public RPC has no global rate limit", file)
		}
	}
}

// Environment variables override nested keys (documented in the profiles).
func TestEnvOverridesChainID(t *testing.T) {
	t.Setenv("JMDN_NETWORK_CHAIN_ID", "8000800")
	t.Setenv("JMDN_NETWORK_ENVIRONMENT", "testnet")
	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.ChainID != 8000800 || cfg.Network.Environment != "testnet" {
		t.Fatalf("env override not applied: chain=%d env=%q", cfg.Network.ChainID, cfg.Network.Environment)
	}
}
