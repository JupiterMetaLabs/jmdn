package reputation

import "testing"

func TestShouldChargeBadSignature(t *testing.T) {
	cases := []struct {
		name   string
		hasSig bool
		reason string
		charge bool
	}{
		{"empty signature (abstain / behind)", false, "", false},
		{"empty signature with reason", false, "not synced", false},
		{"genuine bad sig, no reason", true, "", true},
		{"has sig but behind: not synced", true, "node not synced", false},
		{"has sig but behind: validation false", true, "validation returned false", false},
		{"has sig but behind: sender missing", true, "sender account 0xabc not found in cache", false},
		{"has sig but behind: unknown parent", true, "unknown parent 0xdead", false},
		{"has sig, unrelated reason → real fault", true, "malformed vote payload", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShouldChargeBadSignature(c.hasSig, c.reason); got != c.charge {
				t.Fatalf("ShouldChargeBadSignature(%v, %q) = %v, want %v", c.hasSig, c.reason, got, c.charge)
			}
		})
	}
}

func TestIsBehindReason_CaseInsensitiveSubstring(t *testing.T) {
	if !IsBehindReason("StatefulChecker: sender account 0x12 NOT FOUND IN CACHE") {
		t.Error("should match case-insensitively")
	}
	if IsBehindReason("equivocation: two signed blocks at height 5") {
		t.Error("equivocation is a real fault, not a behind reason")
	}
	if IsBehindReason("") {
		t.Error("empty reason is not a behind reason")
	}
}

func TestAnyBehindReason(t *testing.T) {
	if !AnyBehindReason(map[string]string{"p1": "ok", "p2": "node not synced"}) {
		t.Error("should detect a behind reason among many")
	}
	if AnyBehindReason(map[string]string{"p1": "bad payload"}) {
		t.Error("no behind reason present")
	}
}
