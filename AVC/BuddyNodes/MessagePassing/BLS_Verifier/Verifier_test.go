package BLS_Verifier

import (
    "testing"

    BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// Test end-to-end: sign then verify for positive and negative votes
func TestVerify_SignThenVerify_PositiveAndNegative(t *testing.T) {
    // Sign +1
    respPlus, ok, err := BLS_Signer.SignMessage(1)
    if err != nil || !ok {
        t.Fatalf("failed to sign +1: ok=%v err=%v", ok, err)
    }
    // Verify +1 should pass
    if err := Verify(respPlus, 1); err != nil {
        t.Fatalf("verify +1 failed: %v", err)
    }
    // Verify -1 should fail
    if err := Verify(respPlus, -1); err == nil {
        t.Fatalf("verify should fail when vote mismatches signature (+1 signed, verified as -1)")
    }

    // Sign -1
    respMinus, ok, err := BLS_Signer.SignMessage(-1)
    if err != nil || !ok {
        t.Fatalf("failed to sign -1: ok=%v err=%v", ok, err)
    }
    // Verify -1 should pass
    if err := Verify(respMinus, -1); err != nil {
        t.Fatalf("verify -1 failed: %v", err)
    }
    // Verify +1 should fail
    if err := Verify(respMinus, 1); err == nil {
        t.Fatalf("verify should fail when vote mismatches signature (-1 signed, verified as +1)")
    }
}


