package BLS_Signer

import (
	"fmt"
	"testing"
)

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