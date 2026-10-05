package DB_OPs

import "testing"

func TestAccountsPoolMax_Env(t *testing.T) {
	cases := map[string]int{"": 30, "60": 60, "30": 30, "256": 256, "29": 30, "257": 30, "x": 30, "-1": 30, " 64 ": 64}
	for raw, want := range cases {
		t.Setenv(accountsPoolMaxEnv, raw)
		if got := accountsPoolMax(30); got != want {
			t.Errorf("%s=%q → %d, want %d", accountsPoolMaxEnv, raw, got, want)
		}
	}
}
