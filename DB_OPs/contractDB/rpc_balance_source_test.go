package contractDB

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// UNTESTED-LOCALLY: written without a Go toolchain in the authoring environment.
// Validate with:
//
//	go test ./DB_OPs/contractDB/ -run TestInitializeStateDB_UsesSharedLedgerSource -v
//
// Guards the RPC-read-path balance fix: when a shared ledger AccountReader is
// registered, InitializeStateDB (used by eth_call / eth_estimateGas handlers)
// must read balances from it — the same authority as the apply path — so that a
// CONTRACT address (which has no DID document) reports its real native balance.
// Before the fix, InitializeStateDB always built a DID-backed ContractDB and
// every contract's address(this).balance was 0 in simulation, making
// require(address(this).balance >= amount) revert in eth_estimateGas while the
// same tx succeeded on-chain (observed with JMDTVault.release on JMDT testnet).

type ledgerStub struct {
	balances map[common.Address]*big.Int
	nonces   map[common.Address]uint64
}

func (l ledgerStub) AccountState(addr common.Address) (*big.Int, uint64, error) {
	bal, ok := l.balances[addr]
	if !ok {
		return new(big.Int), 0, nil // never-seen address: fresh account, not an error
	}
	return new(big.Int).Set(bal), l.nonces[addr], nil
}

func TestInitializeStateDB_UsesSharedLedgerSource(t *testing.T) {
	vault := common.HexToAddress("0xe6e169bd3cB3Da76e213fB559Ba2aB16729bA5B7")
	locked, _ := new(big.Int).SetString("100000000000000000000", 10) // 100 JMDT

	// Save/restore process-wide singletons so this test is hermetic.
	prevSrc, prevRepo := sharedAccountSrc, sharedStateRepo
	t.Cleanup(func() { sharedAccountSrc, sharedStateRepo = prevSrc, prevRepo })

	SetSharedStateRepository(&failRepo{})
	SetSharedAccountSource(ledgerStub{
		balances: map[common.Address]*big.Int{vault: locked},
		nonces:   map[common.Address]uint64{vault: 1},
	})

	db, err := InitializeStateDB()
	if err != nil {
		t.Fatalf("InitializeStateDB: %v", err)
	}

	got := db.GetBalance(vault)
	if got.ToBig().Cmp(locked) != 0 {
		t.Fatalf("contract balance via RPC-path StateDB = %s, want %s (ledger source not used)", got, locked)
	}
	if n := db.GetNonce(vault); n != 1 {
		t.Fatalf("contract nonce via RPC-path StateDB = %d, want 1", n)
	}

	// A contract we never funded is a fresh account (0), not an error.
	other := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	if got := db.GetBalance(other); got.Sign() != 0 {
		t.Fatalf("unknown address balance = %s, want 0", got)
	}
	if dberr := db.(*ContractDB).DBError(); dberr != nil {
		t.Fatalf("DBError() = %v for a clean ledger read, want nil", dberr)
	}
}

func TestInitializeStateDB_RequiresRepoWithLedgerSource(t *testing.T) {
	prevSrc, prevRepo := sharedAccountSrc, sharedStateRepo
	t.Cleanup(func() { sharedAccountSrc, sharedStateRepo = prevSrc, prevRepo })

	SetSharedStateRepository(nil)
	SetSharedAccountSource(ledgerStub{})

	if _, err := InitializeStateDB(); err == nil {
		t.Fatal("InitializeStateDB with ledger source but no StateRepository: want error, got nil")
	}
}
