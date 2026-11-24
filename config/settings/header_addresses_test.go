package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// consensus.header_addresses, a list of ranges, comes through Load() from
// jmdn.yaml.
func TestHeaderAddresses_LoadFromYAML(t *testing.T) {
	dir := t.TempDir()
	yaml := `
consensus:
  header_binding_from_height: 500
  header_addresses:
    - from_height: 500
      coinbase: "0x2222222222222222222222222222222222222222"
      zkvm: "0x3333333333333333333333333333333333333333"
    - from_height: 900
      coinbase: "0x4444444444444444444444444444444444444444"
      zkvm: "0x3333333333333333333333333333333333333333"
`
	if err := os.WriteFile(filepath.Join(dir, "jmdn.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := cfg.Consensus.HeaderAddresses
	if cfg.Consensus.HeaderBindingFromHeight != 500 || len(a) != 2 || a[1].FromHeight != 900 ||
		a[1].Coinbase != "0x4444444444444444444444444444444444444444" || a[0].ZKVM != "0x3333333333333333333333333333333333333333" {
		t.Fatalf("header_addresses not loaded: %+v", a)
	}
}
