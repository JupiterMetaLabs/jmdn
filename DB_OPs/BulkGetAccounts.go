package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"
)

// GetMultipleAccounts retrieves multiple accounts from ThebeDB in a single bulk SQL read.
// PooledConnection may be nil — getHandle falls back to the global ThebeDB handle.
func GetMultipleAccounts(PooledConnection *config.PooledConnection, accounts *AccountsSet) (map[string]*Account, error) {
	if len(accounts.Accounts) == 0 {
		return map[string]*Account{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h, err := getHandle(PooledConnection)
	if err != nil {
		return nil, fmt.Errorf("GetMultipleAccounts: %w", err)
	}

	// Collect requested addresses (map keys are address hex strings).
	addresses := make([]string, 0, len(accounts.Accounts))
	for addr := range accounts.Accounts {
		addresses = append(addresses, addr)
	}

	// Time: O(n) — single bulk SQL read (WHERE address = ANY($1)); n = len(addresses).
	storeAccounts, err := h.BulkGetAccounts(ctx, addresses)
	if err != nil {
		return nil, fmt.Errorf("GetMultipleAccounts: %w", err)
	}

	result := make(map[string]*Account, len(storeAccounts))
	for _, sa := range storeAccounts {
		if sa == nil {
			continue
		}
		result[sa.Address.Hex()] = storeAccountFromStore(sa)
	}
	return result, nil
}
