package BLS_Verifier

import (
	"testing"

	BLS_Signer "jmdn/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// Test end-to-end: sign then verify for positive and negative votes
func TestVerify_SignThenVerify_PositiveAndNegative(t *testing.T) {
	t.Logf("Starting TestVerify_SignThenVerify_PositiveAndNegative")

	// Sign +1
	respPlus, ok, err := BLS_Signer.SignMessage(1)
	t.Logf("Signed +1: ok=%v, err=%v, response=%+v", ok, err, respPlus)
	if err != nil || !ok {
		t.Fatalf("failed to sign +1: ok=%v err=%v", ok, err)
	} else {
		t.Logf("Successfully signed vote +1")
	}
	// Print pubkey and signature for debugging
	t.Logf("respPlus.PubKey: %s", respPlus.PubKey)
	t.Logf("respPlus.Signature: %s", respPlus.Signature)

	// Verify +1 should pass
	t.Logf("Verifying signature for +1 (should PASS)...")
	if err := Verify(respPlus, 1); err != nil {
		t.Fatalf("verify +1 failed: %v", err)
	} else {
		t.Logf("Verify(+1) passed as expected")
	}

	// Verify -1 should fail
	t.Logf("Verifying signature for -1 with +1 signature (should FAIL)...")
	if err := Verify(respPlus, -1); err == nil {
		t.Fatalf("verify should fail when vote mismatches signature (+1 signed, verified as -1)")
	} else {
		t.Logf("Verify(+1sig, -1vote) failed as expected: %v", err)
	}

	// Sign -1
	respMinus, ok, err := BLS_Signer.SignMessage(-1)
	t.Logf("Signed -1: ok=%v, err=%v, response=%+v", ok, err, respMinus)
	if err != nil || !ok {
		t.Fatalf("failed to sign -1: ok=%v err=%v", ok, err)
	} else {
		t.Logf("Successfully signed vote -1")
	}
	// Print pubkey and signature for debugging
	t.Logf("respMinus.PubKey: %s", respMinus.PubKey)
	t.Logf("respMinus.Signature: %s", respMinus.Signature)

	// Verify -1 should pass
	t.Logf("Verifying signature for -1 (should PASS)...")
	if err := Verify(respMinus, -1); err != nil {
		t.Fatalf("verify -1 failed: %v", err)
	} else {
		t.Logf("Verify(-1) passed as expected")
	}

	// Verify +1 should fail
	t.Logf("Verifying signature for +1 with -1 signature (should FAIL)...")
	if err := Verify(respMinus, 1); err == nil {
		t.Fatalf("verify should fail when vote mismatches signature (-1 signed, verified as +1)")
	} else {
		t.Logf("Verify(-1sig, +1vote) failed as expected: %v", err)
	}

	t.Logf("TestVerify_SignThenVerify_PositiveAndNegative completed")
}
