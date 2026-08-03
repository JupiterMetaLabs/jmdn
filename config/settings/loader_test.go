package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadThebeYAMLValues(t *testing.T) {
	t.Setenv("THEBE_SQL_DSN", "")
	t.Setenv("THEBE_REDIS_URL", "")
	t.Setenv("THEBE_STREAM_NAME", "")
	t.Setenv("THEBE_MAX_LEN", "")
	t.Setenv("THEBE_GROUP_NAME", "")

	cfg := loadFromTempConfig(t, `
thebe:
  enabled: true
  kv_path: "./custom-kv"
  sql_dsn: "postgres://yaml"
  redis_url: "redis://yaml"
  stream_name: "yaml.stream"
  max_len: 2048
  group_name: "yaml-group"
`)

	if cfg.Thebe.KVPath != "./custom-kv" {
		t.Fatalf("kv_path mismatch: got %q", cfg.Thebe.KVPath)
	}
	if cfg.Thebe.SQLDSN != "postgres://yaml" {
		t.Fatalf("sql_dsn mismatch: got %q", cfg.Thebe.SQLDSN)
	}
	if cfg.Thebe.RedisURL != "redis://yaml" {
		t.Fatalf("redis_url mismatch: got %q", cfg.Thebe.RedisURL)
	}
	if cfg.Thebe.StreamName != "yaml.stream" {
		t.Fatalf("stream_name mismatch: got %q", cfg.Thebe.StreamName)
	}
	if cfg.Thebe.MaxLen != 2048 {
		t.Fatalf("max_len mismatch: got %d", cfg.Thebe.MaxLen)
	}
	if cfg.Thebe.GroupName != "yaml-group" {
		t.Fatalf("group_name mismatch: got %q", cfg.Thebe.GroupName)
	}
}

func TestLoadThebeEnvOverrides(t *testing.T) {
	t.Setenv("THEBE_SQL_DSN", "postgres://env")
	t.Setenv("THEBE_REDIS_URL", "redis://env")
	t.Setenv("THEBE_STREAM_NAME", "env.stream")
	t.Setenv("THEBE_MAX_LEN", "3333")
	t.Setenv("THEBE_GROUP_NAME", "env-group")

	cfg := loadFromTempConfig(t, `
thebe:
  sql_dsn: "postgres://yaml"
  redis_url: "redis://yaml"
  stream_name: "yaml.stream"
  max_len: 10
  group_name: "yaml-group"
`)

	if cfg.Thebe.SQLDSN != "postgres://env" {
		t.Fatalf("sql_dsn env override failed: got %q", cfg.Thebe.SQLDSN)
	}
	if cfg.Thebe.RedisURL != "redis://env" {
		t.Fatalf("redis_url env override failed: got %q", cfg.Thebe.RedisURL)
	}
	if cfg.Thebe.StreamName != "env.stream" {
		t.Fatalf("stream_name env override failed: got %q", cfg.Thebe.StreamName)
	}
	if cfg.Thebe.MaxLen != 3333 {
		t.Fatalf("max_len env override failed: got %d", cfg.Thebe.MaxLen)
	}
	if cfg.Thebe.GroupName != "env-group" {
		t.Fatalf("group_name env override failed: got %q", cfg.Thebe.GroupName)
	}
}

func TestLoadThebeDefaultsWhenOmittedOrInvalid(t *testing.T) {
	t.Setenv("THEBE_SQL_DSN", "")
	t.Setenv("THEBE_REDIS_URL", "")
	t.Setenv("THEBE_STREAM_NAME", "")
	t.Setenv("THEBE_MAX_LEN", "")
	t.Setenv("THEBE_GROUP_NAME", "")

	cfg := loadFromTempConfig(t, `
thebe:
  stream_name: "   "
  max_len: 0
  group_name: ""
`)

	if cfg.Thebe.StreamName != "thebedb.events" {
		t.Fatalf("stream_name default failed: got %q", cfg.Thebe.StreamName)
	}
	if cfg.Thebe.MaxLen != 1000 {
		t.Fatalf("max_len default failed: got %d", cfg.Thebe.MaxLen)
	}
	if cfg.Thebe.GroupName != "projector" {
		t.Fatalf("group_name default failed: got %q", cfg.Thebe.GroupName)
	}
}

func loadFromTempConfig(t *testing.T, body string) *NodeConfig {
	t.Helper()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "jmdn.yaml")
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
		globalCfg = nil
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	return cfg
}
