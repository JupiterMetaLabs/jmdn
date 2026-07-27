package BLS_Signer

import (
	"fmt"
	"os"
	"testing"
)

// Tests have no provisioned config/bls.json, so allow the signer to mint an
// ephemeral keypair (production loads-only and fails closed — see getBLSKeypair).
func init() { os.Setenv("JMDN_BLS_AUTOGEN", "1") }

func TestSignMessage(t *testing.T) {
	blsResp, status, err := SignMessage(1)
	if err != nil {
		t.Fatalf("Failed to sign message: %v", err)
	}
	if !status {
		t.Fatalf("Failed to sign message: %v", err)
	}
	fmt.Printf("Signed message: %v\n", blsResp)

	blsResp, status, err = SignMessage(-1)
	if err != nil {
		t.Fatalf("Failed to sign message: %v", err)
	}
	if !status {
		t.Fatalf("Failed to sign message: %v", err)
	}
	fmt.Printf("Signed message: %v\n", blsResp)
}
