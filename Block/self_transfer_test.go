package Block

import (
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func TestIsSelfTransfer(t *testing.T) {
	a1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	a2 := a1 // same address, distinct pointer
	b := common.HexToAddress("0x2222222222222222222222222222222222222222")

	if !isSelfTransfer(&config.Transaction{From: &a1, To: &a2}) {
		t.Fatal("same address behind distinct pointers must be a self-transfer")
	}
	if isSelfTransfer(&config.Transaction{From: &a1, To: &b}) {
		t.Fatal("different addresses flagged")
	}
	if isSelfTransfer(&config.Transaction{From: &a1, To: nil}) {
		t.Fatal("contract creation flagged")
	}
}
