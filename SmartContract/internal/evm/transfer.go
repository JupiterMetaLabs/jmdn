package evm

import (
	"gossipnode/helper"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// CanTransfer checks if the account has enough balance to transfer the specified amount
// This implements vm.CanTransferFunc
func CanTransfer(db vm.StateDB, addr common.Address, amount *uint256.Int) bool {
	balance := db.GetBalance(addr)
	return balance.Cmp(amount) >= 0
}

// Transfer transfers funds from one account to another
// This implements vm.TransferFunc
func Transfer(db vm.StateDB, sender, recipient common.Address, amount *uint256.Int) {
	db.SubBalance(sender, amount, tracing.BalanceChangeTransfer)
	db.AddBalance(recipient, amount, tracing.BalanceChangeTransfer)
}

// CanTransferAdapter adapts the new signature to the old one if needed, or vice-versa
// Providing a helper for bigint based check used in some direct calls
func CanTransferBigInt(db vm.StateDB, addr common.Address, amount *big.Int) bool {
	balance := db.GetBalance(addr)

	uintAmount, overflow := uint256.FromBig(amount)
	if overflow {
		return false
	}
	return balance.Cmp(uintAmount) >= 0
}

// TransferBigInt adapts BigInt transfer touint256
func TransferBigInt(db vm.StateDB, sender, recipient common.Address, amount *big.Int) {
	amount256, overflow := helper.ConvertBigToUint256(amount)
	if !overflow {
		db.SubBalance(sender, amount256, tracing.BalanceChangeTransfer)
		db.AddBalance(recipient, amount256, tracing.BalanceChangeTransfer)
	} else {
		// In a real system we should handle this gracefully, but panic matches current behavior
		panic("Overflow occurred during transfer")
	}
}

// CreateAddress creates a new contract address based on sender and nonce
func CreateAddress(caller common.Address, nonce uint64) common.Address {
	return crypto.CreateAddress(caller, nonce)
}
