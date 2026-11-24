package DB_OPs

import "testing"

func TestIsUsedAccount(t *testing.T) {
	cases := []struct {
		name string
		acct *Account
		want bool
	}{
		{"no account", nil, false},
		{"registered, never used", &Account{Balance: "0"}, false},
		{"registered, empty balance", &Account{}, false},
		{"unparseable balance", &Account{Balance: "x"}, false},
		{"funded", &Account{Balance: "1"}, true},
		{"drained to zero after sending", &Account{Balance: "0", TxNonce: 3}, true},
		{"sent count only", &Account{Balance: "0", TxCountSent: 1}, true},
	}
	for _, c := range cases {
		if got := IsUsedAccount(c.acct); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
