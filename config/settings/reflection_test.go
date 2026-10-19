package settings

import "testing"

func TestGRPCReflectionAllowed(t *testing.T) {
	prev := globalCfg
	t.Cleanup(func() { globalCfg = prev })

	globalCfg = nil
	if GRPCReflectionAllowed() {
		t.Fatal("unloaded config must not allow reflection")
	}
	c := DefaultConfig()
	c.Network.Environment = "mainnet"
	globalCfg = &c
	if GRPCReflectionAllowed() {
		t.Fatal("mainnet must not allow reflection")
	}
	c.Network.Environment = "testnet"
	if !GRPCReflectionAllowed() {
		t.Fatal("testnet should allow reflection")
	}
}
