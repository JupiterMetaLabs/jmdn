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

	// Finalise is declared by the embedded vm.StateDB above. Do not redeclare it
	// here: since go-ethereum v1.17.5 its signature is
	// Finalise(bool) *bal.ConstructionBlockAccessList, and a second declaration
	// with any signature is a duplicate-method error. This interface is
	// structurally identical to contractDB.StateDB, which carries the same note.

	// Additional methods needed by BlockProcessing

	GetBalanceChanges() map[common.Address]*uint256.Int
}
