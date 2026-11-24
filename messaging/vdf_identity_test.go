package messaging

import "testing"

// The property, not the implementation: the identity changes iff any of the
// three bound parameters (group name, modulus digest, difficulty T) changes.
// This is what makes a fleet disagreement on ANY of them detectable — D-39
// (T) and D-54 (group/modulus) as one check.
func TestVDFIdentityDigest_BindsAllThreeParameters(t *testing.T) {
	const g = "rsa-2048-testnet-ephemeral"
	const d = "a48337dd135615a9ee5c47dee0dcecca6558500151fa361b0beaf717469b778e"
	const T = uint64(476510)

	base := VDFIdentityDigest(g, d, T)
	if base == "" {
		t.Fatal("identity empty for valid inputs")
	}
	if len(base) != 64 {
		t.Fatalf("want 32-byte hex (64 chars), got %d", len(base))
	}

	// Stable for identical inputs.
	if again := VDFIdentityDigest(g, d, T); again != base {
		t.Fatalf("not deterministic: %s vs %s", base, again)
	}

	// Each parameter must move the digest.
	if VDFIdentityDigest("rsa-2048-frc", d, T) == base {
		t.Error("D-54: different group name did NOT change the identity")
	}
	if VDFIdentityDigest(g, "0000000000000000000000000000000000000000000000000000000000000000", T) == base {
		t.Error("D-54: different modulus digest did NOT change the identity")
	}
	if VDFIdentityDigest(g, d, T+1) == base {
		t.Error("D-39: different difficulty T did NOT change the identity")
	}
}

// Injective concatenation: shifting a character across the group/digest boundary
// must not produce the same digest (length-prefixing guarantees this).
func TestVDFIdentityDigest_IsInjectiveAcrossFields(t *testing.T) {
	if VDFIdentityDigest("ab", "cd", 1) == VDFIdentityDigest("a", "bcd", 1) {
		t.Error("concatenation is not injective across the group/digest boundary")
	}
}

// Empty group or digest yields the Stage-1 sentinel "".
func TestVDFIdentityDigest_EmptyInputsAreSentinel(t *testing.T) {
	if VDFIdentityDigest("", "abcd", 1) != "" {
		t.Error("empty group name should yield \"\"")
	}
	if VDFIdentityDigest("g", "", 1) != "" {
		t.Error("empty modulus digest should yield \"\"")
	}
}

func TestLocalVDFIdentity_SetGet(t *testing.T) {
	t.Cleanup(func() { SetLocalVDFIdentity("") })
	if LocalVDFIdentity() != "" {
		t.Fatalf("expected empty default, got %q", LocalVDFIdentity())
	}
	SetLocalVDFIdentity("deadbeef")
	if LocalVDFIdentity() != "deadbeef" {
		t.Fatalf("set/get mismatch: %q", LocalVDFIdentity())
	}
}
