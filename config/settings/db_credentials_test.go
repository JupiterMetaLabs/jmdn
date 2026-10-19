package settings

import "testing"

func TestDefaultDBCredentialInUse(t *testing.T) {
	cases := map[string]bool{
		"postgres://jmdn:jmdndefault@127.0.0.1:5430/jmdn?sslmode=require": true,
		"postgres://thebedb:thebedb@localhost:5430/thebedb":               true,
		"postgres://jmdn@127.0.0.1:5430/jmdn?sslmode=require":             false,
		"postgres://jmdn:8f3c1e9a77d2@127.0.0.1:5430/jmdn":                false,
		"host=127.0.0.1 user=jmdn":                                        false,
	}
	for dsn, want := range cases {
		c := DefaultConfig()
		c.Thebe.SQLDSN = dsn
		if got := c.DefaultDBCredentialInUse(); got != want {
			t.Errorf("%s: got %v want %v", dsn, got, want)
		}
	}
}

func TestValidateDBCredentials_StrictOnly(t *testing.T) {
	c := DefaultConfig()
	c.Thebe.SQLDSN = "postgres://jmdn:jmdndefault@127.0.0.1:5430/jmdn"
	if err := c.ValidateDBCredentials(); err != nil {
		t.Fatalf("non-strict must only warn: %v", err)
	}
	c.Security.StrictPosture = true
	if err := c.ValidateDBCredentials(); err == nil {
		t.Fatal("strict posture must refuse a known password")
	}
}

func TestShippedDefaultsCarryNoPassword(t *testing.T) {
	d := DefaultConfig()
	if d.DefaultDBCredentialInUse() {
		t.Fatal("compiled default DSN carries a known password")
	}
}
