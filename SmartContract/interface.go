package SmartContract

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"
)

// StateDB defines the public interface for the EVM state database.
// This allows external packages (like BlockProcessing) to interact with the state
// without directly importing internal packages.
type StateDB interface {
	vm.StateDB

	// CommitToDB commits all pending state changes to the underlying database
	// If deleteEmptyObjects is true, empty accounts will be deleted
	CommitToDB(deleteEmptyObjects bool) (common.Hash, error)

	// Finalise finalizes the state changes but doesn't commit to database yet
	// This is called at the end of transaction execution
	Finalise(deleteEmptyObjects bool)

	// Additional methods needed by BlockProcessing

	GetBalanceChanges() map[common.Address]*uint256.Int
}
