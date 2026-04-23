package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gossipnode/config"

	"github.com/JupiterMetaLabs/ion"
)

// GetMultipleAccounts retrieves multiple accounts from the DB using a single network call (GetAll).
func GetMultipleAccounts(PooledConnection *config.PooledConnection, accounts *AccountsSet) (map[string]*Account, error) {
	if len(accounts.Accounts) == 0 {
		return map[string]*Account{}, nil
	}

	// 1. Setup Context and Connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	loggerCtx, cancelLog := context.WithCancel(context.Background())
	defer cancelLog()

	var err error
	var shouldReturnConnection bool

	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get connection: %w", err)
		}
		shouldReturnConnection = true

		PooledConnection.Client.Logger.Debug(loggerCtx, "Acquired connection for BulkGetAccounts",
			ion.String("function", "DB_OPs.GetMultipleAccounts"))
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Debug(loggerCtx, "Returning connection to pool",
				ion.String("function", "DB_OPs.GetMultipleAccounts"))
			PutAccountsConnection(PooledConnection)
		}()
	}

	start := time.Now()
	defer func() {
		PooledConnection.Client.Logger.Debug(loggerCtx, "BulkGetAccounts completed",
			ion.Int64("duration_ms", time.Since(start).Milliseconds()),
			ion.Int("count", len(accounts.Accounts)),
			ion.String("function", "DB_OPs.GetMultipleAccounts"))
	}()

	// 2. Ensure Accounts DB is selected
	if err := ensureAccountsDBSelected(PooledConnection); err != nil {
		return nil, fmt.Errorf("failed to select accounts DB: %w", err)
	}

	// 3. Prepare Keys
	keys := make([][]byte, 0, len(accounts.Accounts))
	for addr := range accounts.Accounts {
		// Key format: "account:<address_hex>"
		key := []byte(fmt.Sprintf("%s%s", Prefix, addr))
		keys = append(keys, key)
	}

	// 4. Exec GetAll
	// Check if client is valid
	if PooledConnection.Client == nil || PooledConnection.Client.Client == nil {
		return nil, fmt.Errorf("invalid database client")
	}

	entriesList, err := PooledConnection.Client.Client.GetAll(ctx, keys)
	if err != nil {
		PooledConnection.Client.Logger.Error(loggerCtx, "GetAll failed", err,
			ion.Int("key_count", len(keys)),
			ion.String("function", "DB_OPs.GetMultipleAccounts"))
		return nil, fmt.Errorf("GetAll failed: %w", err)
	}

	if entriesList == nil {
		return map[string]*Account{}, nil
	}

	// 5. Parse Results
	result := make(map[string]*Account)
	for _, entry := range entriesList.Entries {
		if entry == nil || entry.Value == nil {
			continue
		}

		var acc Account
		if err := json.Unmarshal(entry.Value, &acc); err != nil {
			PooledConnection.Client.Logger.Warn(loggerCtx, "Failed to unmarshal account",
				ion.String("key", string(entry.Key)),
				ion.String("error", err.Error()),
				ion.String("function", "DB_OPs.GetMultipleAccounts"))
			continue
		}

		// Map key is the hex address string (0x...)
		result[acc.Address.Hex()] = &acc
	}

	return result, nil
}
