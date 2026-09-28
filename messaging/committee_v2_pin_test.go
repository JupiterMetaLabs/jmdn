package messaging

import "testing"

// Block-858 finding 4: committee-v2 on + empty sequencer pin must refuse boot; every
// other combination must be allowed.
func TestValidateCommitteeV2Pin(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		pin     string
		wantErr bool
	}{
		{"flag off, empty pin — no-op", false, "", false},
		{"flag off, pin set — no-op", false, "aa" + repeat("bb", 47), false},
		{"flag ON, empty pin — refuse boot", true, "", true},
		{"flag ON, whitespace pin — refuse boot", true, "   ", true},
		{"flag ON, pin set — allowed", true, "aa" + repeat("bb", 47), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateCommitteeV2Pin(c.enabled, c.pin)
			if c.wantErr && err == nil {
				t.Fatalf("ValidateCommitteeV2Pin(%v, %q) = nil, want error", c.enabled, c.pin)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("ValidateCommitteeV2Pin(%v, %q) = %v, want nil", c.enabled, c.pin, err)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
