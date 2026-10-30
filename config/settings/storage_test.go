package settings

import (
	"strings"
	"testing"
	"time"
)

func TestValidateStorage(t *testing.T) {
	light := func() *NodeConfig {
		c := DefaultConfig()
		c.Storage.Profile = "light"
		return &c
	}
	if err := func() error { c := DefaultConfig(); return c.ValidateStorage(true) }(); err != nil {
		t.Fatalf("default full profile on the sequencer: %v", err)
	}
	if err := light().ValidateStorage(false); err != nil {
		t.Fatalf("light with defaults: %v", err)
	}
	if !light().Storage.IsLight() {
		t.Fatal("IsLight false for profile light")
	}

	cases := []struct {
		name   string
		mod    func(*NodeConfig)
		seq    bool
		errHas string
	}{
		{"sequencer", func(*NodeConfig) {}, true, "sequencer"},
		{"checkpoint off", func(c *NodeConfig) { c.Checkpoint.Enabled = false }, false, "checkpoint.enabled"},
		{"no floor", func(c *NodeConfig) { c.Checkpoint.FloorFile = " " }, false, "floor_file"},
		{"keep too small", func(c *NodeConfig) { c.Storage.LightKeepBlocks = 999 }, false, "minimum"},
		{"no interval", func(c *NodeConfig) { c.Storage.PruneInterval = 0 }, false, "prune_interval"},
		{"unknown profile", func(c *NodeConfig) { c.Storage.Profile = "archive" }, false, "storage.profile"},
	}
	for _, tc := range cases {
		c := light()
		tc.mod(c)
		err := c.ValidateStorage(tc.seq)
		if err == nil || !strings.Contains(err.Error(), tc.errHas) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.errHas)
		}
	}
	c := light()
	c.Storage.LightKeepBlocks = 1000
	c.Storage.PruneInterval = time.Minute
	if err := c.ValidateStorage(false); err != nil {
		t.Fatalf("keep 1000: %v", err)
	}
}
