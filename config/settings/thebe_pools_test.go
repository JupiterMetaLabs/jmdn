package settings

import "testing"

func TestThebePools_Defaults(t *testing.T) {
	cfg := loadFromTempConfig(t, `
thebe:
  enabled: true
`)
	if p := cfg.Thebe.Pools; p.Write != 10 || p.Read != 10 || p.Sync != 4 {
		t.Fatalf("defaults: got %+v want {Write:10 Read:10 Sync:4}", p)
	}
}

func TestThebePools_YAMLAndEnv(t *testing.T) {
	t.Setenv("JMDN_THEBE_POOLS_SYNC", "7")
	cfg := loadFromTempConfig(t, `
thebe:
  pools:
    write: 12
    read: 20
    sync: 3
`)
	if p := cfg.Thebe.Pools; p.Write != 12 || p.Read != 20 {
		t.Fatalf("yaml: got %+v", p)
	}
	if cfg.Thebe.Pools.Sync != 7 {
		t.Fatalf("env JMDN_THEBE_POOLS_SYNC must override yaml: got %d", cfg.Thebe.Pools.Sync)
	}
}
