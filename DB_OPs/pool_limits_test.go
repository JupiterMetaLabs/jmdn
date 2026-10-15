package DB_OPs

import (
	"testing"

	"gossipnode/config"
	"gossipnode/config/settings"
)

func TestAccountsPoolMax(t *testing.T) {
	cases := []struct {
		configured int
		want       int
		ok         bool
	}{
		{30, 30, true},
		{60, 60, true},
		{256, 256, true},
		{29, 30, false},
		{0, 30, false},
		{-1, 30, false},
		{257, 30, false},
	}
	for _, c := range cases {
		got, ok := accountsPoolMax(30, c.configured)
		if got != c.want || ok != c.ok {
			t.Errorf("accountsPoolMax(30, %d) = (%d, %v), want (%d, %v)", c.configured, got, ok, c.want, c.ok)
		}
	}
}

// The shipped database.accounts_pool_max must equal the shared pool default,
// so leaving the key unset changes nothing.
func TestAccountsPoolMax_DefaultMatchesPool(t *testing.T) {
	pool := config.DefaultConnectionPoolConfig().MaxConnections
	if d := settings.DefaultConfig().Database.AccountsPoolMax; d != pool {
		t.Fatalf("settings default %d != pool default %d", d, pool)
	}
}
