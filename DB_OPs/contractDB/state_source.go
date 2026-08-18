package contractDB

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// Local-ledger account source (audit EVM-A16).
//
// The default ContractDB sources balances/nonces from the DID service over gRPC
// at execution time (state_object.go loadAccountFromDID), swallowing a read error
// to a zero balance. On the CONSENSUS APPLY PATH that is fatal: a transient
// per-node DID hiccup makes one node execute against balance 1000 and another
// against 0, forking state. On the apply path the balance authority must be the
// node's own committed ledger — deterministic and identical fleet-wide — and any
// read error must FAIL CLOSED (abort the tx/block), never default to zero.

// AccountReader sources authoritative balance + nonce from the local committed
// ledger. Implemented by the apply-path adapter over the node's account store;
// the debug/RPC path leaves it unset and keeps using the DID client.
type AccountReader interface {
	// AccountState returns the committed balance and tx-nonce for addr. A
	// never-seen address is (0, 0, nil), NOT an error. An error means the ledger
	// read genuinely failed and the caller must abort (fail-closed).
	AccountState(addr common.Address) (balance *big.Int, nonce uint64, err error)
}

// NewContractDBWithAccountSource builds a ContractDB whose balances/nonces come
// from the local committed ledger (deterministic apply path), not the DID gRPC
// service. Use this for block application; use NewContractDB(didClient, repo) for
// the loopback debug/RPC surface.
func NewContractDBWithAccountSource(src AccountReader, repo StateRepository) *ContractDB {
	c := NewContractDB(nil, repo)
	c.accountSrc = src
	return c
}

// DBError returns the first sticky account-read error since construction, or nil.
// The executor MUST check this after EVM execution and abort (discard all state
// changes) when it is non-nil — state computed from a defaulted-zero read must
// never be committed.
func (c *ContractDB) DBError() error {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.dbErr
}

// setDBError records the first sticky read error. Caller MUST hold c.lock (write).
func (c *ContractDB) setDBError(err error) {
	if c.dbErr == nil {
		c.dbErr = err
	}
}

// loadAccountFromReader reads balance+nonce from the local-ledger source and maps
// them into an AccountData. Overflow is treated as a hard error (fail-closed).
func loadAccountFromReader(src AccountReader, addr common.Address) (*AccountData, error) {
	balance, nonce, err := src.AccountState(addr)
	if err != nil {
		return nil, err
	}
	account := NewAccountData()
	if balance != nil {
		account.Balance = new(uint256.Int)
		if account.Balance.SetFromBig(balance) {
			return nil, fmt.Errorf("account %s balance overflows uint256: %s", addr.Hex(), balance)
		}
	}
	account.Nonce = nonce
	return account, nil
}
