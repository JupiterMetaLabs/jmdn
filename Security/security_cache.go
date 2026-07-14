package Security

// This file is to create a dataframe from the user accounts to check the security checks
// No security checks should access the db directly. it should only access the dataframe
// This dataframe is loaded from the db and cleared with .Close() function

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

type SecurityCache struct {
	accounts map[string]*DB_OPs.Account
	mu       sync.RWMutex
}

func NewSecurityCache() *SecurityCache {
	return &SecurityCache{
		accounts: make(map[string]*DB_OPs.Account),
	}
}

func (s *SecurityCache) LoadAccounts(ctx context.Context, PooledConnection *config.PooledConnection, accounts *DB_OPs.AccountsSet) error {
	if len(accounts.Accounts) == 0 {
		return nil
	}

	fetchedAccounts, err := DB_OPs.GetMultipleAccounts(PooledConnection, accounts)
	if err != nil {
		// Propagate — callers must fail-closed. Swallowing this turns a transient DB
		// error into "account not found", which can trigger zero-balance overwrites.
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for k, v := range fetchedAccounts {
		if v != nil {
			s.accounts[k] = v
		}
	}

	return nil
}

func (s *SecurityCache) Close() {
	s.accounts = nil
}

func (s *SecurityCache) AddBalance(address common.Address, wei *big.Int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[address.Hex()]
	if account != nil {
		balance, ok := new(big.Int).SetString(account.Balance, 10)
		if ok {
			account.Balance = new(big.Int).Add(balance, wei).String()
		}
	}
}

func (s *SecurityCache) SubBalance(address common.Address, wei *big.Int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[address.Hex()]
	if account != nil {
		balance, ok := new(big.Int).SetString(account.Balance, 10)
		if ok {
			account.Balance = new(big.Int).Sub(balance, wei).String()
		}
	}
}

func (s *SecurityCache) UpdateTxNonce(address common.Address, newNonce uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[address.Hex()]
	if account != nil {
		account.TxNonce = newNonce
	}
}

func (s *SecurityCache) GetTxNonce(address common.Address) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account := s.accounts[address.Hex()]
	if account != nil {
		return account.TxNonce
	}
	return 0
}

func (s *SecurityCache) GetAccount(address common.Address) *DB_OPs.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accounts[address.Hex()]
}

// RegisterAccount inserts an account directly into the cache.
// Used by the submit-tx path to register a newly created receiver account
// without an extra DB round-trip.
func (s *SecurityCache) RegisterAccount(address common.Address, account *DB_OPs.Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[address.Hex()] = account
}

// CheckAddressExistWithCache checks if sender (and receiver, if not a contract deployment) exist in the cache.
// tx.To == nil is valid for contract deployments — only the sender is required to exist.
func (s *SecurityCache) CheckAddressExistWithCache(tx *config.Transaction, traceCtx context.Context) (bool, error) {
	if tx.From == nil {
		return false, errors.New("from address is nil")
	}

	// Sender MUST exist
	sender := s.GetAccount(*tx.From)
	if sender == nil {
		return false, fmt.Errorf("sender account %s not found in cache", tx.From.Hex())
	}

	// tx.To is nil for contract deployments — skip receiver existence check.
	// For regular transfers, receiver must be a known DID account.
	if tx.To != nil {
		receiver := s.GetAccount(*tx.To)
		if receiver == nil {
			return false, fmt.Errorf("receiver account %s not found in cache", tx.To.Hex())
		}
	}

	return true, nil
}

// CheckBalanceWithCache checks if sender has enough balance using cache.
// It also updates the cache (simulating execution) to prevent double-spending attacks within the same block.
func (s *SecurityCache) CheckBalanceWithCache(tx *config.Transaction, traceCtx context.Context) (bool, error) {
	if tx.From == nil {
		return false, errors.New("sender address is nil")
	}

	sender := s.GetAccount(*tx.From)
	if sender == nil {
		return false, fmt.Errorf("sender account not found in cache")
	}

	// Parse Sender Balance
	balance, ok := new(big.Int).SetString(sender.Balance, 10)
	if !ok {
		return false, fmt.Errorf("invalid balance format for account %s", tx.From.Hex())
	}

	// Calculate Total Cost (Value + Gas) using consensus formula (EIP-1559 aware).
	cost := new(big.Int).Set(tx.Value) // Value to transfer
	gasCost := config.GasFee(tx.Type, tx.GasLimit, tx.GasPrice, tx.MaxFee, tx.MaxPriorityFee)
	totalCost := new(big.Int).Add(cost, gasCost)

	// Check sufficiency
	if balance.Cmp(totalCost) < 0 {
		return false, nil // Insufficient funds
	}

	// --- SIMULATE EXECUTION IN CACHE ---

	// 1. Deduct from Sender
	s.SubBalance(*tx.From, totalCost)

	// 2. Add to Receiver (if exists and is not contract creation)
	if tx.To != nil {
		// We only add value, not gas cost (gas burned/miner)
		s.AddBalance(*tx.To, cost)
	}

	return true, nil
}
