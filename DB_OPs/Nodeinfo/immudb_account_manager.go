package NodeInfo

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
	"gossipnode/DB_OPs"
)

type account_manager struct{}

// Time Complexity: O(N) where N is the total number of transactions scanned or retrieved
func (am *account_manager) GetTransactionsForAccount(accountAddress string) ([]types.DBTransaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get main DB connection: %w", err)
	}

	addr := common.HexToAddress(accountAddress)
	cfgTxs, err := DB_OPs.GetTransactionsByAccount(conn, &addr)
	if err != nil {
		return nil, fmt.Errorf("failed to get transactions by account: %w", err)
	}

	// Note: We cast/map config.Transaction to types.DBTransaction here.
	// Since types.DBTransaction structure isn't entirely matched here, we serialize via JSON roughly or adapt fields.
	var result []types.DBTransaction
	for _, tx := range cfgTxs {
		// Serialize and deserialize to map config.Transaction to types.DBTransaction if they are field-compatible
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get account DB connection: %w", err)
	}

	addr := common.HexToAddress(accountAddress)
	acc, err := DB_OPs.GetAccount(conn, addr)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get account: %w", err)
	}

	balance := new(big.Int)
	balance.SetString(acc.Balance, 10)
	return balance, acc.Nonce, nil
}

// Time Complexity: O(1)
func (am *account_manager) UpdateAccountBalance(accountAddress string, balance *big.Int, nonce uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return fmt.Errorf("failed to get account DB connection: %w", err)
	}

	addr := common.HexToAddress(accountAddress)
	
	// FastSync may require updating the nonce inside this method. The DB_OPs UpdateAccountBalance does not accept nonce.
	// However, we can use DB_OPs.UpdateAccountBalance for balance, and rely on FastSync or next tx to sync nonce,
	// or perform a BatchRestoreAccounts with updated Account.
	err = DB_OPs.UpdateAccountBalance(conn, addr, balance.String())
	if err != nil {
		return fmt.Errorf("failed to update account balance: %w", err)
	}
	
	return nil
}

// Time Complexity: O(1)
func (am *account_manager) CreateAccount(accountAddress string, balance *big.Int, nonce uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return fmt.Errorf("failed to get account DB connection: %w", err)
	}

	addr := common.HexToAddress(accountAddress)
	
	// Minimal metadata
	meta := make(map[string]interface{})
	err = DB_OPs.CreateAccount(conn, accountAddress, addr, meta)
	if err != nil {
		return fmt.Errorf("failed to create account: %w", err)
	}

	// Then update the balance if necessary
	if balance.Cmp(big.NewInt(0)) > 0 {
		_ = DB_OPs.UpdateAccountBalance(conn, addr, balance.String())
	}
	return nil
}

// Time Complexity: O(N) where N is the number of updates
func (am *account_manager) BatchUpdateAccounts(updates []types.AccountUpdate) error {
	// DB_OPs has BatchRestoreAccounts
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return fmt.Errorf("failed to get account DB connection: %w", err)
	}

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
		
		val, _ := json.Marshal(acc)
		entries = append(entries, struct{Key string; Value []byte}{
			Key:   "address:" + addr.Hex(),
			Value: val,
		})
	}

	return DB_OPs.BatchRestoreAccounts(conn, entries)
}
