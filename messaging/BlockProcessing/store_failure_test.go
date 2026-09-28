package BlockProcessing

// D-858 review B8: a Postgres-free, always-run test of the B2 store-failure decision.
// The full applygate suite (TestStoreFailureRollback_ContractRefusesRollback) needs a
// live Postgres + solc-compiled bytecode and is skipped in CI, so the B2 refusal path
// shipped with no runnable test. decideStoreFailure isolates the decision so it can be
// verified here with a stubbed rollback func and no infrastructure.

import (
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestDecideStoreFailure_ContractRefusesRollback(t *testing.T) {
	bh := common.HexToHash("0xC0FFEE")
	markContractStateCommitted(bh)
	defer clearContractStateCommitted(bh)

	rolledBackCalled := false
	err, rolledBack := decideStoreFailure(bh, 858, errors.New("uq_txn_block_index 23505"), func() {
		rolledBackCalled = true
	})

	if rolledBack || rolledBackCalled {
		t.Fatalf("contract-bearing block MUST NOT roll back (storage has no undo); rolledBack=%v called=%v", rolledBack, rolledBackCalled)
	}
	if err == nil {
		t.Fatal("expected a fail-closed error")
	}
	if !strings.Contains(err.Error(), "refusing incomplete rollback") {
		t.Fatalf("expected refusal error, got %v", err)
	}
	if !strings.Contains(err.Error(), "uq_txn_block_index") {
		t.Fatalf("refusal error should wrap the underlying store error, got %v", err)
	}
}

func TestDecideStoreFailure_NonContractRollsBack(t *testing.T) {
	bh := common.HexToHash("0xBEEF") // never marked as committing contract state

	rolledBackCalled := false
	err, rolledBack := decideStoreFailure(bh, 844, errors.New("disk full"), func() {
		rolledBackCalled = true
	})

	if !rolledBack || !rolledBackCalled {
		t.Fatalf("non-contract block MUST roll back the applied prefix; rolledBack=%v called=%v", rolledBack, rolledBackCalled)
	}
	if err == nil {
		t.Fatal("expected a fail-closed error")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("expected rollback error, got %v", err)
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("rollback error should wrap the underlying store error, got %v", err)
	}
}

// The flag registry is per-block-hash and isolated: a mark on one hash does not leak
// into the decision for another (two blocks applying concurrently must not cross).
func TestDecideStoreFailure_FlagIsolationPerBlock(t *testing.T) {
	contract := common.HexToHash("0x1111")
	plain := common.HexToHash("0x2222")
	markContractStateCommitted(contract)
	defer clearContractStateCommitted(contract)

	if _, rolledBack := decideStoreFailure(plain, 10, errors.New("x"), func() {}); !rolledBack {
		t.Fatal("unmarked block must roll back even while another block is marked")
	}
	if _, rolledBack := decideStoreFailure(contract, 11, errors.New("x"), func() {}); rolledBack {
		t.Fatal("marked block must refuse rollback")
	}
}
