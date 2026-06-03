package thebeprofile_test

import (
	"context"
	"strings"
	"testing"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"
	"gossipnode/DB_OPs/thebeprofile"
)

func TestJMDNProfile_Name(t *testing.T) {
	p := thebeprofile.NewJMDNProfile()
	if p.Name() != "jmdn" {
		t.Errorf("Name: want %q, got %q", "jmdn", p.Name())
	}
}

func TestJMDNProfile_Namespaces(t *testing.T) {
	p := thebeprofile.NewJMDNProfile()
	ns := p.Namespaces()

	if len(ns) != 7 {
		t.Errorf("Namespaces count: want 7, got %d: %v", len(ns), ns)
	}

	required := []string{"account", "block", "snapshot", "tx", "zk", "l1_finality", "contract_receipt"}
	nsSet := make(map[string]bool, len(ns))
	for _, n := range ns {
		nsSet[n] = true
	}
	for _, r := range required {
		if !nsSet[r] {
			t.Errorf("Namespaces missing: %q", r)
		}
	}
}

func TestJMDNProfile_GetMigration(t *testing.T) {
	p := thebeprofile.NewJMDNProfile()
	m := p.GetMigration()

	if m == "" {
		t.Fatal("GetMigration returned empty string")
	}
	for _, want := range []string{"CREATE TABLE", "accounts", "contract_receipts"} {
		if !strings.Contains(m, want) {
			t.Errorf("GetMigration: missing %q", want)
		}
	}
}

func TestApply_NilRecord(t *testing.T) {
	p := thebeprofile.NewJMDNProfile()
	if err := p.Apply(context.Background(), 0, nil, nil); err != nil {
		t.Errorf("Apply(nil record): want nil error, got %v", err)
	}
}

func TestApply_UnknownNamespace(t *testing.T) {
	p := thebeprofile.NewJMDNProfile()
	rec := &core.CanonicalRecord{Namespace: "unknown_xyz", Value: []byte(`{}`)}
	if err := p.Apply(context.Background(), 1, rec, nil); err != nil {
		t.Errorf("Apply(unknown ns): want nil error, got %v", err)
	}
}

// TestApply_BadJSON verifies that each registered namespace returns an error when
// Value is not valid JSON. tx=nil is safe because unmarshal fails before any SQL tx is touched.
func TestApply_BadJSON(t *testing.T) {
	namespaces := []string{
		"account",
		"block",
		"snapshot",
		"tx",
		"zk",
		"l1_finality",
		"contract_receipt",
	}

	p := thebeprofile.NewJMDNProfile()

	for _, ns := range namespaces {
		ns := ns
		t.Run(ns, func(t *testing.T) {
			rec := &core.CanonicalRecord{
				Namespace: ns,
				Value:     []byte("not-json"),
			}
			err := p.Apply(context.Background(), 0, rec, nil)
			if err == nil {
				t.Fatalf("namespace=%q: expected error for bad JSON, got nil", ns)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "unmarshal") {
				t.Errorf("namespace=%q: error %q should mention unmarshal", ns, err.Error())
			}
		})
	}
}
