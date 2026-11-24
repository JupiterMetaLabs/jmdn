package adapters

import (
	"context"
	"errors"
	"testing"

	"gossipnode/config"
	"gossipnode/config/settings"
)

// These tests cover ONLY EvaluateShadow's gate/mode decision logic, by
// overriding runFullValidatorFn with a fake — they do NOT exercise
// runFullValidatorAgainstDB itself (that needs a live ImmuDB connection; see
// its doc comment). This file stays in package adapters (not adapters_test)
// specifically so it can reach the unexported runFullValidatorFn var.

func withFakeFullValidator(t *testing.T, fn func(ctx context.Context, cfg *settings.NodeConfig, zkBlock *config.ZKBlock) (bool, error)) {
	t.Helper()
	saved := runFullValidatorFn
	runFullValidatorFn = fn
	t.Cleanup(func() { runFullValidatorFn = saved })
}

func testnetCfg(enabled bool, mode string) *settings.NodeConfig {
	return &settings.NodeConfig{
		Network:  settings.NetworkSettings{Environment: "testnet", ChainID: 1337},
		Features: settings.FeatureSettings{AvcValidation: settings.AvcValidationSettings{Enabled: enabled, Mode: mode}},
	}
}

func TestEvaluateShadow_NilConfigIsNoOp(t *testing.T) {
	accept, err := EvaluateShadow(context.Background(), nil, &config.ZKBlock{}, true, nil)
	if !accept || err != nil {
		t.Fatalf("nil cfg must return legacy unchanged, got accept=%v err=%v", accept, err)
	}
}

func TestEvaluateShadow_DisabledIsNoOp(t *testing.T) {
	withFakeFullValidator(t, func(context.Context, *settings.NodeConfig, *config.ZKBlock) (bool, error) {
		t.Fatal("runFullValidatorFn must not be called when AvcValidation.Enabled is false")
		return false, nil
	})
	cfg := testnetCfg(false, "enforce")
	legacyErr := errors.New("legacy rejection reason")
	accept, err := EvaluateShadow(context.Background(), cfg, &config.ZKBlock{}, false, legacyErr)
	if accept != false || err != legacyErr {
		t.Fatalf("disabled must return legacy unchanged, got accept=%v err=%v", accept, err)
	}
}

func TestEvaluateShadow_NonTestnetRefusesEvenIfEnabled(t *testing.T) {
	withFakeFullValidator(t, func(context.Context, *settings.NodeConfig, *config.ZKBlock) (bool, error) {
		t.Fatal("runFullValidatorFn must not be called off testnet, even with enforce mode")
		return false, nil
	})
	cfg := &settings.NodeConfig{
		Network:  settings.NetworkSettings{Environment: "mainnet", ChainID: 8000800},
		Features: settings.FeatureSettings{AvcValidation: settings.AvcValidationSettings{Enabled: true, Mode: "enforce"}},
	}
	accept, err := EvaluateShadow(context.Background(), cfg, &config.ZKBlock{}, true, nil)
	if !accept || err != nil {
		t.Fatalf("mainnet must refuse to run avc validation regardless of mode, got accept=%v err=%v", accept, err)
	}
}

func TestEvaluateShadow_ShadowModeNeverChangesDecision_OnMismatch(t *testing.T) {
	withFakeFullValidator(t, func(context.Context, *settings.NodeConfig, *config.ZKBlock) (bool, error) {
		return false, nil // avc disagrees with legacy accept below
	})
	cfg := testnetCfg(true, "shadow")
	accept, err := EvaluateShadow(context.Background(), cfg, &config.ZKBlock{}, true, nil)
	if !accept || err != nil {
		t.Fatalf("shadow mode must return legacy decision unchanged even on mismatch, got accept=%v err=%v", accept, err)
	}
}

func TestEvaluateShadow_ShadowModeNeverChangesDecision_OnAvcError(t *testing.T) {
	withFakeFullValidator(t, func(context.Context, *settings.NodeConfig, *config.ZKBlock) (bool, error) {
		return false, errors.New("avc internal error")
	})
	cfg := testnetCfg(true, "shadow")
	accept, err := EvaluateShadow(context.Background(), cfg, &config.ZKBlock{}, true, nil)
	if !accept || err != nil {
		t.Fatalf("shadow mode must swallow avc errors and return legacy unchanged, got accept=%v err=%v", accept, err)
	}
}

func TestEvaluateShadow_UnrecognizedModeTreatedAsShadow(t *testing.T) {
	withFakeFullValidator(t, func(context.Context, *settings.NodeConfig, *config.ZKBlock) (bool, error) {
		return false, nil
	})
	cfg := testnetCfg(true, "bogus-mode")
	accept, err := EvaluateShadow(context.Background(), cfg, &config.ZKBlock{}, true, nil)
	if !accept || err != nil {
		t.Fatalf("an unrecognized mode must behave as shadow (safe default), got accept=%v err=%v", accept, err)
	}
}

func TestEvaluateShadow_EnforceModeUsesAvcDecision(t *testing.T) {
	withFakeFullValidator(t, func(context.Context, *settings.NodeConfig, *config.ZKBlock) (bool, error) {
		return false, nil // avc rejects even though legacy accepted
	})
	cfg := testnetCfg(true, "enforce")
	accept, err := EvaluateShadow(context.Background(), cfg, &config.ZKBlock{}, true, nil)
	if accept || err != nil {
		t.Fatalf("enforce mode must use the avc decision (reject), got accept=%v err=%v", accept, err)
	}
}

func TestEvaluateShadow_EnforceModeFailsClosedOnInternalError(t *testing.T) {
	withFakeFullValidator(t, func(context.Context, *settings.NodeConfig, *config.ZKBlock) (bool, error) {
		return false, errors.New("db connection lost")
	})
	cfg := testnetCfg(true, "enforce")
	accept, err := EvaluateShadow(context.Background(), cfg, &config.ZKBlock{}, true, nil)
	if accept || err == nil {
		t.Fatalf("enforce mode must fail CLOSED (reject) on an internal avc error, not fall back to legacy accept; got accept=%v err=%v", accept, err)
	}
}
