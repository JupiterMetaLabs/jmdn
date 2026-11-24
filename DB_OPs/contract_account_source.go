package DB_OPs

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// ContractAccountSource adapts the committed account ledger to the balance/nonce
// reader the apply-path EVM StateDB consumes (contractDB.AccountReader — satisfied
// structurally, no import). Contract execution on the consensus apply path reads
// deterministic ledger balances through this instead of the non-deterministic DID
// gRPC service (audit EVM-A16).
//
// Contract: a never-seen address is (0, 0, nil) — a fresh account, not an error.
// A genuine ledger read fault returns an error, which the executor turns into a
// fail-closed tx/block abort (never a defaulted-zero balance).
type ContractAccountSource struct{}

// AccountState returns the committed balance (wei) and tx-nonce for addr.
func (ContractAccountSource) AccountState(addr common.Address) (*big.Int, uint64, error) {
	acc, err := GetAccount(nil, addr)
	if err != nil {
		if isNotFoundError(err) {
			return new(big.Int), 0, nil // fresh account
		}
		return nil, 0, err
	}
	bal := new(big.Int)
	if acc.Balance != "" {
		if _, ok := bal.SetString(acc.Balance, 10); !ok { // ledger balances are base-10
			return nil, 0, fmt.Errorf("contract account source: account %s has unparseable balance %q", addr.Hex(), acc.Balance)
		}
	}
	return bal, acc.TxNonce, nil
}
