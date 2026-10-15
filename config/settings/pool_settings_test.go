package settings

// These tests pin the operator-facing contract for the two accounts-DB pool
// settings: their YAML keys, their defaults, and the env names Viper derives
// from those keys (JMDN_ + key with "." -> "_"). A key rename silently changes
// what operators must write, which is what these assertions catch.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPoolSettingsDefaults(t *testing.T) {
	d := DefaultConfig()
	if d.Database.AccountsPoolMax != 30 {
		t.Fatalf("database.accounts_pool_max default = %d, want 30", d.Database.AccountsPoolMax)
	}
	if d.FastSync.AccountSyncDBSlots != 4 {
		t.Fatalf("fastsync.account_sync_db_slots default = %d, want 4", d.FastSync.AccountSyncDBSlots)
	}
}

func TestPoolSettingsFromYAML(t *testing.T) {
	// Load() reads /etc/jmdn/jmdn.yaml before ./jmdn.yaml (first found wins),
	// so a machine-wide config would shadow the file written here.
	if _, err := os.Stat("/etc/jmdn/jmdn.yaml"); err == nil {
		t.Skip("/etc/jmdn/jmdn.yaml exists and would shadow ./jmdn.yaml")
	}
	t.Setenv("JMDN_DATABASE_ACCOUNTS_POOL_MAX", "")
	t.Setenv("JMDN_FASTSYNC_ACCOUNT_SYNC_DB_SLOTS", "")

	dir := t.TempDir()
	yaml := "database:\n  accounts_pool_max: 64\nfastsync:\n  account_sync_db_slots: 6\n"
	if err := os.WriteFile(filepath.Join(dir, "jmdn.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Database.AccountsPoolMax != 64 {
		t.Fatalf("database.accounts_pool_max = %d, want 64", cfg.Database.AccountsPoolMax)
	}
	if cfg.FastSync.AccountSyncDBSlots != 6 {
		t.Fatalf("fastsync.account_sync_db_slots = %d, want 6", cfg.FastSync.AccountSyncDBSlots)
	}
	// Keys absent from the file keep their defaults.
	if cfg.FastSync.SyncCheckInterval != DefaultConfig().FastSync.SyncCheckInterval {
		t.Fatalf("unrelated default changed: sync_check_interval = %v", cfg.FastSync.SyncCheckInterval)
	}
}

func TestPoolSettingsEnvVarNames(t *testing.T) {
	t.Setenv("JMDN_DATABASE_ACCOUNTS_POOL_MAX", "60")
	t.Setenv("JMDN_FASTSYNC_ACCOUNT_SYNC_DB_SLOTS", "8")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Database.AccountsPoolMax != 60 {
		t.Fatalf("JMDN_DATABASE_ACCOUNTS_POOL_MAX did not bind: %d", cfg.Database.AccountsPoolMax)
	}
	if cfg.FastSync.AccountSyncDBSlots != 8 {
		t.Fatalf("JMDN_FASTSYNC_ACCOUNT_SYNC_DB_SLOTS did not bind: %d", cfg.FastSync.AccountSyncDBSlots)
	}
}

// The pool-exhaustion hotfix (#185) rollout set JMDN_ACCOUNTS_POOL_MAX=60 on
// the sequencer unit. A node upgraded to this build without a unit-file change
// must keep that cap, not silently fall back to 30.
func TestPoolSettingsLegacyEnvVarNames(t *testing.T) {
	t.Setenv("JMDN_DATABASE_ACCOUNTS_POOL_MAX", "")
	t.Setenv("JMDN_FASTSYNC_ACCOUNT_SYNC_DB_SLOTS", "")
	t.Setenv("JMDN_ACCOUNTS_POOL_MAX", "60")
	t.Setenv("JMDN_ACCOUNTSYNC_DB_SLOTS", "8")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Database.AccountsPoolMax != 60 {
		t.Fatalf("legacy JMDN_ACCOUNTS_POOL_MAX did not bind: %d", cfg.Database.AccountsPoolMax)
	}
	if cfg.FastSync.AccountSyncDBSlots != 8 {
		t.Fatalf("legacy JMDN_ACCOUNTSYNC_DB_SLOTS did not bind: %d", cfg.FastSync.AccountSyncDBSlots)
	}
}

// When both names are set, the derived (documented) name wins.
func TestPoolSettingsDerivedEnvWinsOverLegacy(t *testing.T) {
	t.Setenv("JMDN_DATABASE_ACCOUNTS_POOL_MAX", "70")
	t.Setenv("JMDN_ACCOUNTS_POOL_MAX", "60")
	t.Setenv("JMDN_FASTSYNC_ACCOUNT_SYNC_DB_SLOTS", "9")
	t.Setenv("JMDN_ACCOUNTSYNC_DB_SLOTS", "8")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Database.AccountsPoolMax != 70 {
		t.Fatalf("derived name should win: got %d, want 70", cfg.Database.AccountsPoolMax)
	}
	if cfg.FastSync.AccountSyncDBSlots != 9 {
		t.Fatalf("derived name should win: got %d, want 9", cfg.FastSync.AccountSyncDBSlots)
	}
}
