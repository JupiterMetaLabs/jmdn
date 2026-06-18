package NodeInfo

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
	"gossipnode/DB_OPs"
)

type account_manager struct{}

// Time Complexity: O(N) where N is the total number of transactions scanned or retrieved
func (am *account_manager) GetTransactionsForAccount(accountAddress string) ([]types.DBTransaction, error) {
	addr := common.HexToAddress(accountAddress)
	cfgTxs, err := DB_OPs.GetTransactionsByAccount(nil, &addr)
	if err != nil {
		return nil, fmt.Errorf("failed to get transactions by account: %w", err)
	}

	var result []types.DBTransaction
	for _, tx := range cfgTxs {
		b, err := json.Marshal(tx)
		if err == nil {
			var dbTx types.DBTransaction
			if json.Unmarshal(b, &dbTx) == nil {
				result = append(result, dbTx)
			}
		}
	}
	return result, nil
}

// Time Complexity: O(1)
func (am *account_manager) GetAccountBalance(accountAddress string) (*big.Int, uint64, error) {
	addr := common.HexToAddress(accountAddress)
	acc, err := DB_OPs.GetAccount(nil, addr)
	if err != nil {
		if strings.Contains(err.Error(), "key not found") {
			return big.NewInt(0), 0, nil
		}
		return nil, 0, fmt.Errorf("failed to get account: %w", err)
	}

	balance := new(big.Int)
	balance.SetString(acc.Balance, 10)
	return balance, acc.Nonce, nil
}

// Time Complexity: O(1) — read-modify-write to update both balance and nonce atomically.
func (am *account_manager) UpdateAccountBalance(accountAddress string, balance *big.Int, nonce uint64) error {
	addr := common.HexToAddress(accountAddress)

	doc, err := DB_OPs.GetAccount(nil, addr)
	if err != nil {
		if strings.Contains(err.Error(), "key not found") {
			return am.CreateAccount(accountAddress, balance, nonce)
		}
		return fmt.Errorf("failed to get account for update: %w", err)
	}

	doc.Balance = balance.String()
	doc.Nonce = nonce
	doc.UpdatedAt = time.Now().UTC().UnixNano()

	if err := DB_OPs.SaveAccount(nil, doc); err != nil {
		return fmt.Errorf("failed to write updated account: %w", err)
	}

	return nil
}

// Time Complexity: O(1)
func (am *account_manager) CreateAccount(accountAddress string, balance *big.Int, nonce uint64) error {
	addr := common.HexToAddress(accountAddress)

	meta := make(map[string]interface{})
	if err := DB_OPs.CreateAccount(nil, accountAddress, addr, meta); err != nil {
		return fmt.Errorf("failed to create account: %w", err)
	}

	doc, err := DB_OPs.GetAccount(nil, addr)
	if err != nil {
		return fmt.Errorf("failed to read back created account: %w", err)
	}

	doc.Balance = balance.String()
	doc.Nonce = nonce
	doc.UpdatedAt = time.Now().UTC().UnixNano()

	if err := DB_OPs.SaveAccount(nil, doc); err != nil {
		return fmt.Errorf("failed to write account with correct balance/nonce: %w", err)
	}

	return nil
}

// Time Complexity: O(N) where N is the number of updates
func (am *account_manager) BatchUpdateAccounts(updates []types.AccountUpdate) error {
	var entries []struct {
		Key   string
		Value []byte
	}

	for _, u := range updates {
		addr := common.HexToAddress(u.Address)
		acc := &DB_OPs.Account{
			DIDAddress:  u.Address,
			Address:     addr,
			Balance:     u.NewBalance.String(),
			Nonce:       u.Nonce,
			AccountType: "user",
			UpdatedAt:   time.Now().UTC().UnixNano(),
		}

		val, err := json.Marshal(acc)
		if err != nil {
			return fmt.Errorf("failed to marshal account %s: %w", u.Address, err)
		}
		entries = append(entries, struct {
			Key   string
			Value []byte
		}{
			Key:   DB_OPs.Prefix + addr.Hex(),
			Value: val,
		})
	}

	return DB_OPs.BatchRestoreAccounts(nil, entries)
}

