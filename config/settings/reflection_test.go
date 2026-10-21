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

	// ValidateEnvironment lowercases only a local copy, so Network.Environment
	// can legitimately keep the spelling the operator wrote. A case- or
	// whitespace-sensitive comparison would publish the reflection service on
	// these mainnet nodes.
	for _, env := range []string{"Mainnet", "MAINNET", "mainnet ", " mainnet"} {
		c.Network.Environment = env
		if GRPCReflectionAllowed() {
			t.Fatalf("environment %q is mainnet and must not allow reflection", env)
		}
	}

	// strict_posture is production too, whatever the environment says.
	c.Network.Environment = "testnet"
	c.Security.StrictPosture = true
	if GRPCReflectionAllowed() {
		t.Fatal("strict_posture must not allow reflection")
	}
	c.Security.StrictPosture = false
}
