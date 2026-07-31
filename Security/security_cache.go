package Security

// This file is to create a dataframe from the user accounts to check the security checks
// No security checks should access the db directly. it should only access the dataframe
// This dataframe is loaded from the db and cleared with .Close() function

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// envOn reads a boolean env override. Absent => def. Anything other than an
// explicit off value counts as on. Mirrors messaging.envOn, duplicated because
// messaging imports Security and the dependency cannot be reversed.
func envOn(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// AllowNewReceiverAccounts lets a transfer name a receiver that does not exist
// yet, instead of rejecting the block that carries it.
//
// THE PROBLEM IT SOLVES: requiring the receiver to pre-exist means JMDT cannot
// be sent to a new address at all. Every voter runs
// CheckAddressExistWithCache against its own account cache, so a first-time
// recipient is rejected by the whole committee — the observed failure was block
// 13756, voted down 0-of-7 with "receiver account … not found in cache". The
// workaround (register the address out-of-band, then wait for it to propagate
// to every voter before proposing) is what AUTO_REGISTER_PROPAGATION_DELAY and
// the DID auto-registration path exist for, and it is inherently racy: the
// proposer can only observe its OWN vantage, never the voters'.
//
// WHY RELAXING IT IS SAFE:
//   - The receiver is not a security input. Only the SENDER must exist, and
//     that check is unchanged: a sender absent from the cache is still a hard
//     reject, because a funded signed transaction from an unknown account means
//     this node's account state is out of sync, which must not be papered over.
//   - Account creation already happens at APPLY time, not validation:
//     DB_OPs/Nodeinfo/immudb_account_manager.go UpdateAccountBalance falls
//     through to CreateAccount on "key not found". So the account still comes
//     into existence exactly once, as a side effect of executing the transfer.
//   - Nothing consensus-critical depends on the account record. The state root
//     is stateRootChain(parentStateRoot, blockHash) — a chain of BLOCK hashes,
//     not a Merkle root over accounts — and the fleet fingerprint hashes block
//     and transaction fields. So per-node differences in locally stamped
//     account metadata (CreatedAt/UpdatedAt, the Fastsync ART nonce) cannot
//     make nodes disagree.
//   - This is how Ethereum behaves: an account springs into existence when it
//     first receives value.
//
// ROLLOUT: this is a VALIDATION RULE. A node with it on accepts blocks a node
// with it off rejects, so a MIXED FLEET SPLITS THE VOTE and blocks fail either
// way. Upgrade the sequencer and every buddy/voter together; a long rolling
// upgrade leaves the fleet mixed for its duration.
//
// Default ON. The pre-existing behaviour is a defect — it makes sending to a
// first-time address impossible — so an operator who sets nothing should get
// the fix rather than silently keep the bug. Defaulting on also means there is
// only ONE risky window (the upgrade) instead of two (upgrade, then a later
// env-var flip).
//
// KILL SWITCH: JMDN_ALLOW_NEW_RECEIVER_ACCOUNTS=0 restores the old rule without
// redeploying. Set it on every node if you use it, for the reason above.
var AllowNewReceiverAccounts = envOn("JMDN_ALLOW_NEW_RECEIVER_ACCOUNTS", true)

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
	//
	// For regular transfers the receiver does NOT have to pre-exist when
	// AllowNewReceiverAccounts is on: it is created when the block is applied
	// (UpdateAccountBalance → CreateAccount on "key not found"), which is what
	// makes sending JMDT to a brand-new address possible at all. See the flag's
	// doc comment for why this is not a security relaxation.
	if tx.To != nil && !AllowNewReceiverAccounts {
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
		// Diagnostic: identify exactly which tx/sender fails and by how much, and
		// the balance THIS node sees — so an underfunded tx can be told apart from
		// a stale local balance (node behind the block producer). fmt.Printf so it
		// reliably reaches journald.
		fmt.Printf("🚫 insufficient funds: tx=%s sender=%s balance=%s needed=%s (value=%s gas=%s)\n",
			tx.Hash.Hex(), tx.From.Hex(), balance.String(), totalCost.String(), cost.String(), gasCost.String())
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
