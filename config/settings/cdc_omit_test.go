package settings

import (
	"reflect"
	"testing"

	"github.com/spf13/viper"
)

func TestOmitColumnsByTable(t *testing.T) {
	c := ThebeCDCConfig{OmitColumns: []string{"zk_proofs.stark_proof", " public.blocks.extra_data ", "zk_proofs.commitment", "nodot", ".x", "t."}}
	got, bad := c.OmitColumnsByTable()
	want := map[string][]string{"zk_proofs": {"stark_proof", "commitment"}, "public.blocks": {"extra_data"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("map = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(bad, []string{"nodot", ".x", "t."}) {
		t.Fatalf("invalid = %v", bad)
	}
}

func TestDefaultConfigOmitsProofBody(t *testing.T) {
	d := DefaultConfig()
	got, bad := d.Thebe.CDC.OmitColumnsByTable()
	if len(bad) != 0 || !reflect.DeepEqual(got, map[string][]string{"zk_proofs": {"stark_proof"}}) {
		t.Fatalf("default omit = %v (invalid %v)", got, bad)
	}
}

// Every shipped yaml loads with the proof body omitted, and so does a yaml
// with no omit_columns key (the default applies).
func TestShippedYAMLOmitsProofBody(t *testing.T) {
	for _, name := range []string{"jmdn_default.yaml", "jmdn_testnet.yaml", "jmdn_exchange.yaml"} {
		v := viper.New()
		setDefaults(v)
		v.SetConfigFile(repoFile(t, name))
		if err := v.ReadInConfig(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var cfg NodeConfig
		if err := v.Unmarshal(&cfg); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, _ := cfg.Thebe.CDC.OmitColumnsByTable()
		if !reflect.DeepEqual(got["zk_proofs"], []string{"stark_proof"}) {
			t.Fatalf("%s: omit_columns = %v", name, cfg.Thebe.CDC.OmitColumns)
		}
	}
}

func TestOmitColumnsDefaultWithoutKey(t *testing.T) {
	v := viper.New()
	setDefaults(v)
	var cfg NodeConfig
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Thebe.CDC.OmitColumns, []string{"zk_proofs.stark_proof"}) {
		t.Fatalf("omit_columns = %v", cfg.Thebe.CDC.OmitColumns)
	}
}
