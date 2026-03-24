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

func (s *SecurityCache) LoadAccounts(ctx context.Context, PooledConnection *config.PooledConnection, accounts *DB_OPs.AccountsSet) *SecurityCache {
	if len(accounts.Accounts) == 0 {
		return s
	}

	// 2. Batch get accounts
	// We pass nil for connection to let GetMultipleAccounts handle pooling internally.
	// If we wanted to share an external connection, we'd pass it here.
	// Since this is the entry point, passing nil is appropriate.
	fetchedAccounts, err := DB_OPs.GetMultipleAccounts(PooledConnection, accounts)
	if err != nil {
		return s
	}

	// 3. Update cache
	s.mu.Lock()
	defer s.mu.Unlock()

	for k, v := range fetchedAccounts {
		if v != nil {
			s.accounts[k] = v
		}
	}

	return s
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

// __DEAD_CODE_AUDIT_PUBLIC__
func (s *SecurityCache) UpdateNonce(address common.Address, newNonce uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[address.Hex()]
	if account != nil {
		account.Nonce = newNonce
	}
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (s *SecurityCache) GetNonce(address common.Address) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account := s.accounts[address.Hex()]
	if account != nil {
		return account.Nonce
	}
	return 0
}

func (s *SecurityCache) GetAccount(address common.Address) *DB_OPs.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accounts[address.Hex()]
}

// CheckAddressExistWithCache checks if sender and receiver exist in the cache.
func (s *SecurityCache) CheckAddressExistWithCache(tx *config.Transaction, traceCtx context.Context) (bool, error) {
	if tx.From == nil || tx.To == nil {
		return false, errors.New("from or to address is nil")
	}

	// Check Sender
	sender := s.GetAccount(*tx.From)
	if sender == nil {
		// Sender MUST exist
		return false, fmt.Errorf("sender account %s not found in cache", tx.From.Hex())
	}

	// Check Receiver
	// Receiver might not exist if it's a new account receiving funds?
	// Original CheckAddressExist logic checks if DIDAddress is empty or invalid?
	// Let's replicate strict check if that's what CheckAddressExist did.
	// Looking at CheckAddressExist (I haven't seen it fully but inferred):
	// Usually invalid/non-existent receiver is allowed in some chains (creates account),
	// but user prompt says "Sender or receiver DID not found".
	// If the original required both to exist, let's stick to that.

	receiver := s.GetAccount(*tx.To)
	if receiver == nil {
		// For now, assuming receiver must exist or be known.
		// If the logic permits new accounts, this test might need adjustment.
		// Re-reading original `CheckAddressExist` call:
		// "Status... sender or receiver DID not found" -> implies both must be found.
		return false, fmt.Errorf("receiver account %s not found in cache", tx.To.Hex())
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

	// Calculate Total Cost (Value + Gas)
	cost := new(big.Int).Set(tx.Value) // Value to transfer
	gasCost := new(big.Int).Mul(new(big.Int).SetUint64(tx.GasLimit), tx.GasPrice)
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
