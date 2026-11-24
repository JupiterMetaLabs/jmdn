package settings

import (
	"testing"
	"time"
)

// The whole point of the defaults: the feature ships dark. Enabling it is an
// explicit operator act, and until then the RPC surface behaves exactly as it
// did before.
func TestDefaultConfig_TxStatusIsDisabled(t *testing.T) {
	d := DefaultConfig()

	if d.TxStatus.Enabled {
		t.Error("tx_status.enabled must default to false")
	}
	if d.TxStatus.PendingTxByHash {
		t.Error("tx_status.pending_tx_by_hash must default to false — serving pending txs changes what existing clients see")
	}
}

// A default that is on but unusable is worse than off: it looks enabled and
// answers `unknown` for everything. Every knob needs a working value.
func TestDefaultConfig_TxStatusValuesAreUsable(t *testing.T) {
	ts := DefaultConfig().TxStatus

	if ts.SubmitRecordTTL <= 0 {
		t.Error("submit_record_ttl must be positive, or `processing` is unreachable")
	}
	if ts.SubmitRecordCapacity <= 0 {
		t.Error("submit_record_capacity must be positive, or no submit record is ever kept")
	}
	if ts.MempoolTimeout <= 0 {
		t.Error("mempool_timeout must be positive")
	}
	if ts.ChainTimeout <= 0 {
		t.Error("chain_timeout must be positive")
	}
	if ts.MempoolTimeout >= ts.ChainTimeout+ts.MempoolTimeout+time.Second {
		t.Error("mempool_timeout is implausibly large relative to the chain timeout")
	}
	if ts.NegativeCacheTTL <= 0 || ts.NegativeCacheSize <= 0 {
		t.Error("the negative cache must be sized, or repeated probes for unknown hashes each hit the fleet")
	}
	if ts.RateLimitPerSec <= 0 || ts.RateLimitBurst <= 0 {
		t.Error("the rate limit must be set: the JSON-RPC port is public and each miss amplifies into a fleet-wide fan-out")
	}
	if ts.BreakerFailureThreshold <= 0 || ts.BreakerCooldown <= 0 {
		t.Error("the breaker must be armed, or an unreachable mempool adds the full timeout to every request")
	}
}

// The negative cache TTL bounds how long a freshly submitted transaction can
// stay invisible after a miss for the same hash. Keep it short.
func TestDefaultConfig_NegativeCacheTTLIsShort(t *testing.T) {
	if ttl := DefaultConfig().TxStatus.NegativeCacheTTL; ttl > 30*time.Second {
		t.Errorf("negative_cache_ttl = %s; a long TTL keeps a newly submitted transaction invisible", ttl)
	}
}

// The mempool timeout is the ceiling on how long a status query can hold an RPC
// handler waiting on the network. It must stay well under a wallet's patience.
func TestDefaultConfig_MempoolTimeoutIsBounded(t *testing.T) {
	if to := DefaultConfig().TxStatus.MempoolTimeout; to > 2*time.Second {
		t.Errorf("mempool_timeout = %s; a status query must not be able to hang an RPC handler this long", to)
	}
}

// Viper's AutomaticEnv does not reach nested keys through Unmarshal, so every
// tx_status key is bound explicitly in Load(). Without those binds
// JMDN_TX_STATUS_ENABLED=true silently does nothing and the feature looks
// broken rather than off — this test is the regression guard for that.
func TestLoad_TxStatusEnvOverrides(t *testing.T) {
	t.Setenv("JMDN_TX_STATUS_ENABLED", "true")
	t.Setenv("JMDN_TX_STATUS_SUBMIT_RECORD_TTL", "45m")
	t.Setenv("JMDN_TX_STATUS_SUBMIT_RECORD_CAPACITY", "1234")
	t.Setenv("JMDN_TX_STATUS_MEMPOOL_TIMEOUT", "250ms")
	t.Setenv("JMDN_TX_STATUS_CHAIN_TIMEOUT", "3s")
	t.Setenv("JMDN_TX_STATUS_NEGATIVE_CACHE_TTL", "5s")
	t.Setenv("JMDN_TX_STATUS_NEGATIVE_CACHE_SIZE", "999")
	t.Setenv("JMDN_TX_STATUS_RATE_LIMIT_PER_SEC", "12.5")
	t.Setenv("JMDN_TX_STATUS_RATE_LIMIT_BURST", "77")
	t.Setenv("JMDN_TX_STATUS_BREAKER_FAILURE_THRESHOLD", "9")
	t.Setenv("JMDN_TX_STATUS_BREAKER_COOLDOWN", "11s")
	t.Setenv("JMDN_TX_STATUS_PENDING_TX_BY_HASH", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	ts := cfg.TxStatus
	if !ts.Enabled {
		t.Error("JMDN_TX_STATUS_ENABLED did not take effect — the BindEnv for tx_status.enabled is missing")
	}
	if !ts.PendingTxByHash {
		t.Error("JMDN_TX_STATUS_PENDING_TX_BY_HASH did not take effect")
	}
	if ts.SubmitRecordTTL != 45*time.Minute {
		t.Errorf("submit_record_ttl = %s, want 45m", ts.SubmitRecordTTL)
	}
	if ts.SubmitRecordCapacity != 1234 {
		t.Errorf("submit_record_capacity = %d, want 1234", ts.SubmitRecordCapacity)
	}
	if ts.MempoolTimeout != 250*time.Millisecond {
		t.Errorf("mempool_timeout = %s, want 250ms", ts.MempoolTimeout)
	}
	if ts.ChainTimeout != 3*time.Second {
		t.Errorf("chain_timeout = %s, want 3s", ts.ChainTimeout)
	}
	if ts.NegativeCacheTTL != 5*time.Second {
		t.Errorf("negative_cache_ttl = %s, want 5s", ts.NegativeCacheTTL)
	}
	if ts.NegativeCacheSize != 999 {
		t.Errorf("negative_cache_size = %d, want 999", ts.NegativeCacheSize)
	}
	if ts.RateLimitPerSec != 12.5 {
		t.Errorf("rate_limit_per_sec = %v, want 12.5", ts.RateLimitPerSec)
	}
	if ts.RateLimitBurst != 77 {
		t.Errorf("rate_limit_burst = %d, want 77", ts.RateLimitBurst)
	}
	if ts.BreakerFailureThreshold != 9 {
		t.Errorf("breaker_failure_threshold = %d, want 9", ts.BreakerFailureThreshold)
	}
	if ts.BreakerCooldown != 11*time.Second {
		t.Errorf("breaker_cooldown = %s, want 11s", ts.BreakerCooldown)
	}
}

// With no environment set, Load must reproduce the shipped defaults — i.e. the
// feature stays off.
func TestLoad_TxStatusDefaultsWhenEnvUnset(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TxStatus.Enabled {
		t.Error("tx_status is enabled with no configuration present")
	}
	if cfg.TxStatus != DefaultConfig().TxStatus {
		t.Errorf("loaded tx_status = %+v, want the defaults %+v", cfg.TxStatus, DefaultConfig().TxStatus)
	}
}
