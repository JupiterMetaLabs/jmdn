package Security

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
)

// A transaction without a sender must be rejected, not dereferenced.
func TestCheckTransactionShape(t *testing.T) {
	if err := CheckTransactionShape(nil); err == nil {
		t.Fatal("nil tx must be rejected")
	}
	if err := CheckTransactionShape(&config.Transaction{Hash: common.HexToHash("0x01")}); err == nil {
		t.Fatal("tx without From must be rejected")
	}
	from := common.HexToAddress("0x1")
	if err := CheckTransactionShape(&config.Transaction{From: &from}); err != nil {
		t.Fatalf("well-formed tx rejected: %v", err)
	}
}

// AllChecks and block validation reject a sender-less transaction.
func TestAllChecks_NilFromReturnsError(t *testing.T) {
	ok, err := AllChecks(&config.Transaction{Hash: common.HexToHash("0x01")})
	if ok || err == nil {
		t.Fatalf("want rejection, got ok=%v err=%v", ok, err)
	}
	if ok, err := CheckZKBlockValidation(&config.ZKBlock{Transactions: []config.Transaction{{Hash: common.HexToHash("0x02")}}}); ok || err == nil {
		t.Fatalf("block with a sender-less tx: want rejection, got ok=%v err=%v", ok, err)
	}
}
