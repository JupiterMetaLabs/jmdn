package DB_OPs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/config"
	"gossipnode/logging"
	"strings"
	"sync"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
)

const(
	Prefix = "address:"
	DIDPrefix = "did:"
)

const (
	LOG_FILE        = "ImmuDB.log"
	LOG_DIR         = "logs"
	LOKI_URL        = "http://localhost:3100"
	LOKI_BATCH_SIZE = 128 * 1024
	LOKI_BATCH_WAIT = 1 * time.Second
	LOKI_TIMEOUT    = 5 * time.Second
	KEEP_LOGS       = true
	TOPIC           = "ImmuDB_ImmuClient"
)

func LoggingStruct() *logging.Logging {
	LogStruct := &logging.Logging{
		FileName: LOG_FILE,
		URL:      LOKI_URL,
		Metadata: logging.LoggingMetadata{
			DIR:       LOG_DIR,
			BatchSize: LOKI_BATCH_SIZE,
			BatchWait: LOKI_BATCH_WAIT,
			Timeout:   LOKI_TIMEOUT,
			KeepLogs:  KEEP_LOGS,
		},
		Topic: TOPIC,
	}
	return LogStruct
}

// DIDDocument represents a DID document
// Goal is to Migrate from old DID based accounts to PublicKey based accounts
// Second Goal is to Clean up the code in this file. Migrate everything to connection pool based and for production
type KeyDocument struct {
	DIDAddress string         `json:"did"`
	Address    string `json:"address"`
}

// This will be stored in the DB
type Account struct {
	// Legacy DID fields (for backward compatibility)
	DIDAddress string `json:"did,omitempty"`

	// New PublicKey based fields
	Address string `json:"address"` // Derived from PublicKey
	Balance string         `json:"balance,omitempty"`
	Nonce   uint64         `json:"nonce"`

	// Account metadata
	AccountType string `json:"account_type"` // "did" or "publickey"
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`

	// Optional metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

var (
	clientInitMutex sync.Mutex // Keep existing mutex for backward compatibility
)

// Get the Nonce of a account
func PutNonceofAccount() (uint64, error) {
	// Nonce is created based on the timestamps for each account
	timeNow := time.Now().UnixNano()
	return uint64(timeNow), nil
}

// // StoreAccount stores a Key document in the accounts database
// func StoreAccount(PooledConnection *config.PooledConnection, KeyDoc *KeyDocument) error {

// 	var err error
// 	var AccountDoc *Account

// 	// Try to use connection pool if available, otherwise fall back to traditional approach
// 	if PooledConnection.Client == nil {
// 		// Make a quick connection from Connection pool and defer
// 		// Make a quick connect to the accounts database from connection pool
// 		PooledConnection, err = GetAccountsConnection()
// 		if err != nil {
// 			return err
// 		}
// 		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
// 			zap.String(logging.Connection_database, config.AccountsDBName),
// 			zap.Time(logging.Created_at, time.Now()),
// 			zap.String(logging.Log_file, LOG_FILE),
// 			zap.String(logging.Topic, TOPIC),
// 			zap.String(logging.Loki_url, LOKI_URL),	
// 			zap.String(logging.Function, "DB_OPs.StoreAccount"),
// 		)
// 	}

// 	// Use the Client pointer directly instead of dereferencing it
// 	ic := PooledConnection.Client
// 	if KeyDoc == nil {
// 		return fmt.Errorf("Key document cannot be nil")
// 	}

// 	// Return the connection to the pool when done
// 	defer func() {
// 		PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
// 			zap.String(logging.Connection_database, config.AccountsDBName),
// 			zap.Time(logging.Created_at, time.Now()),
// 			zap.String(logging.Log_file, LOG_FILE),
// 			zap.String(logging.Topic, TOPIC),
// 			zap.String(logging.Loki_url, LOKI_URL),
// 			zap.String(logging.Function, "DB_OPs.StoreAccount"),
// 		)
// 		PutAccountsConnection(PooledConnection)
// 	}()

// 	// Get nonce based on the timestamps
// 	Nonce, err := PutNonceofAccount()
// 	if err != nil {
// 		return err
// 	}

// 	InitialBalance := "0"

// 	// Create the account document
// 	AccountDoc = &Account{
// 		DIDAddress:  KeyDoc.DIDAddress,
// 		Address:     KeyDoc.Address,
// 		Balance:     InitialBalance,
// 		Nonce:       Nonce,
// 		AccountType: "user",
// 		CreatedAt:   time.Now().Unix(),
// 		UpdatedAt:   time.Now().Unix(),
// 		Metadata:    nil,
// 	}

// 	key := fmt.Sprintf("%s%s", Prefix, KeyDoc.Address)

// 	// Traditional approach with single connection
// 	// Ensure we're using the accounts database
// 	if err := ensureAccountsDBSelected(ic); err != nil {
// 		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
// 			zap.String(logging.Connection_database, config.AccountsDBName),
// 			zap.Time(logging.Created_at, time.Now()),
// 			zap.String(logging.Log_file, LOG_FILE),
// 			zap.String(logging.Topic, TOPIC),
// 			zap.String(logging.Loki_url, LOKI_URL),
// 			zap.String(logging.Function, "DB_OPs.StoreAccount"),
// 		)
// 		return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
// 	}

// 	// Store using SafeCreate for cryptographic verification
// 	return SafeCreate(ic, key, AccountDoc)
// }

// StoreAccount stores a Key document in the accounts database and creates a DID reference
func StoreAccount(PooledConnection *config.PooledConnection, KeyDoc *KeyDocument) error {
    var err error
    var AccountDoc *Account

    // Try to use connection pool if available, otherwise fall back to traditional approach
    if PooledConnection.Client == nil {
        PooledConnection, err = GetAccountsConnection()
        if err != nil {
            return err
        }
        PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),    
            zap.String(logging.Function, "DB_OPs.StoreAccount"),
        )
    }

    // Use the Client pointer directly instead of dereferencing it
    ic := PooledConnection.Client
    if KeyDoc == nil {
        return fmt.Errorf("Key document cannot be nil")
    }

    // Return the connection to the pool when done
    defer func() {
        PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, "DB_OPs.StoreAccount"),
        )
        PutAccountsConnection(PooledConnection)
    }()

    // Get nonce based on the timestamps
    Nonce, err := PutNonceofAccount()
    if err != nil {
        return err
    }

    InitialBalance := "0"

    // Create the account document
    AccountDoc = &Account{
        DIDAddress:  KeyDoc.DIDAddress,
        Address:     KeyDoc.Address,
        Balance:     InitialBalance,
        Nonce:       Nonce,
        AccountType: "user",
        CreatedAt:   time.Now().Unix(),
        UpdatedAt:   time.Now().Unix(),
        Metadata:    nil,
    }

    // Create the account key (e.g., "account:<address>")
    accKey := []byte(fmt.Sprintf("%s%s", Prefix, KeyDoc.Address))
    
    // Create the DID key (e.g., "did:did:example:123")
    didKey := []byte(DIDPrefix + KeyDoc.DIDAddress)

    // Ensure we're using the accounts database
    if err := ensureAccountsDBSelected(ic); err != nil {
        return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
    }

    // Marshal the account document
    val, err := json.Marshal(AccountDoc)
    if err != nil {
        ic.Logger.Logger.Error("Failed to marshal account document",
            zap.Error(err),
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, "DB_OPs.StoreAccount"),
        )
        return fmt.Errorf("failed to marshal account document: %w", err)
    }

    // Create atomic operations:
    // 1. Store the account document
    // 2. Create a reference from DID to account
	ops := []*schema.Op{
		{Operation: &schema.Op_Kv{Kv: &schema.KeyValue{Key: accKey, Value: val}}},
		{Operation: &schema.Op_Ref{Ref: &schema.ReferenceRequest{
			Key:         didKey,
			ReferencedKey: accKey,
			AtTx:        0,
			BoundRef:    true,
		}}},
	}

    // Execute all operations atomically
    status, err := ic.Client.ExecAll(ic.Ctx, &schema.ExecAllRequest{Operations: ops})
    if err != nil {
        ic.Logger.Logger.Error("Failed to store account and create DID reference",
            zap.Error(err),
            zap.String(logging.Header_Accounts, status.String()),
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, "DB_OPs.StoreAccount"),
        )
        return fmt.Errorf("failed to store account and create DID reference: %w", err)
    }

    ic.Logger.Logger.Info("Successfully stored account and created DID reference",
		zap.String(logging.Header_Accounts, status.String()),
        zap.String(logging.Account, KeyDoc.Address),
        zap.String(logging.DID, KeyDoc.DIDAddress),
        zap.String(logging.Connection_database, config.AccountsDBName),
        zap.Time(logging.Created_at, time.Now()),
        zap.String(logging.Log_file, LOG_FILE),
        zap.String(logging.Topic, TOPIC),
        zap.String(logging.Loki_url, LOKI_URL),
        zap.String(logging.Function, "DB_OPs.StoreAccount"),
    )

    return nil
}

// GetAccountByDID retrieves an account by its DID
func GetAccountByDID(PooledConnection *config.PooledConnection, did string) (*Account, error) {
    var err error
    // Try to use connection pool if available, otherwise fall back to traditional approach
    if PooledConnection == nil || PooledConnection.Client == nil {
        PooledConnection, err = GetAccountsConnection()
        if err != nil {
            return nil, fmt.Errorf("failed to get connection from pool: %w", err)
        }
    }

    // Return the connection to the pool when done
    defer func() {
        PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, "DB_OPs.GetAccountByDID"),
        )
        PutAccountsConnection(PooledConnection)
    }()

	ic := PooledConnection.Client

    // Ensure we're using the accounts database
    if err := ensureAccountsDBSelected(ic); err != nil {
        return nil, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
    }

    // Create the DID key
    didKey := []byte(DIDPrefix + did)

    // First, get the reference to the account key
    ref, err := ic.Client.Get(ic.Ctx, didKey)
    if err != nil {
        if strings.Contains(err.Error(), "key not found") {
            return nil, ErrNotFound
        }
        PooledConnection.Client.Logger.Logger.Error("Failed to get DID reference",
            zap.Error(err),
            zap.String(logging.DID, did),
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, "DB_OPs.GetAccountByDID"),
        )
        return nil, fmt.Errorf("failed to get DID reference: %w", err)
    }

    // Get the account using the referenced key
    entry, err := ic.Client.VerifiedGet(ic.Ctx, ref.Value)
    if err != nil {
        if strings.Contains(err.Error(), "key not found") {
            return nil, ErrNotFound
        }
        PooledConnection.Client.Logger.Logger.Error("Failed to get account by reference",
            zap.Error(err),
            zap.String(logging.DID, did),
            zap.String(logging.Account, string(ref.Value)),
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, "DB_OPs.GetAccountByDID"),
        )
        return nil, fmt.Errorf("failed to get account by reference: %w", err)
    }

    // Unmarshal the data to the Account struct
    var account Account
    if err := json.Unmarshal(entry.Value, &account); err != nil {
        PooledConnection.Client.Logger.Logger.Error("Failed to unmarshal account data",
            zap.Error(err),
            zap.String(logging.DID, did),
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, "DB_OPs.GetAccountByDID"),
        )
        return nil, fmt.Errorf("failed to unmarshal account data: %w", err)
    }

    PooledConnection.Client.Logger.Logger.Info("Successfully retrieved account by DID",
        zap.String(logging.DID, did),
        zap.String(logging.Account, account.Address),
        zap.String(logging.Connection_database, config.AccountsDBName),
        zap.Time(logging.Created_at, time.Now()),
        zap.String(logging.Log_file, LOG_FILE),
        zap.String(logging.Topic, TOPIC),
        zap.String(logging.Loki_url, LOKI_URL),
        zap.String(logging.Function, "DB_OPs.GetAccountByDID"),
    )

    return &account, nil
}

// GetAccount retrieves a Account document from the accounts database
func GetAccount(PooledConnection *config.PooledConnection, address common.Address) (*Account, error) {
	var err error
	// Try to use connection pool if available, otherwise fall back to traditional approach
	if PooledConnection.Client == nil {
		// Make a quick connection from Connection pool and defer
		// Make a quick connect to the accounts database from connection pool
		PooledConnection, err = GetAccountsConnection()
		if err != nil {
			fmt.Println("Failed to get connection from pool", err)
			return nil, err
		}
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetAccount"),
		)
	}

	// Use the Client pointer directly instead of dereferencing it
	ic := PooledConnection.Client

	// Return the connection to the pool when done
	defer func() {
		PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetAccount"),
		)
		PutAccountsConnection(PooledConnection)
	}()

	key := fmt.Sprintf("%s%s", Prefix, address)

	entry, err := ic.Client.VerifiedGet(ic.Ctx, []byte(key))
	if err != nil {
		if err.Error() == "key not found" {
			return nil, ErrNotFound
		}
		return nil, err
	}

	// Unmarshal the data to the Account struct
	var account Account
	if err := json.Unmarshal(entry.Value, &account); err != nil {
		ic.Logger.Logger.Error("Failed to Unmarshal the data",
			zap.Error(err),
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetAccount"),
		)
		return nil, fmt.Errorf("failed to unmarshal Account document: %w", err)
	}

	ic.Logger.Logger.Info("Successfully retrieved Account document: %s",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetAccount"),
	)
	return &account, nil
}

// UpdateAccountBalance updates the balance for a Account
func UpdateAccountBalance(PooledConnection *config.PooledConnection, address common.Address, newBalance string) error {
	// Ensure we're using the accounts database
	if PooledConnection != nil {
		if err := ensureAccountsDBSelected(PooledConnection.Client); err != nil {
			return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
		}
	}

	doc, err := GetAccount(PooledConnection, address)
	if err != nil {
		return err
	}

	doc.Balance = newBalance
	doc.UpdatedAt = time.Now().Unix()

	// Safe Write to the DB with the same key
	err = SafeCreate(PooledConnection.Client, fmt.Sprintf("%s%s", Prefix, address), doc)
	if err != nil {
		PooledConnection.Client.Logger.Logger.Error("Failed to update DID balance: %s",
			zap.String(logging.Account, address.String()),
			zap.Error(err),
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.UpdateAccountBalance"),
		)
		return err
	}

	PooledConnection.Client.Logger.Logger.Info("Successfully updated Account balance of address: %s",
		zap.String(logging.Account, address.String()),
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.UpdateAccountBalance"),
	)
	return nil
}

// ListAllAccounts retrieves all Accounts with a limit
func ListAllAccounts(PooledConnection *config.PooledConnection, limit int) ([]*Account, error) {
    var err error
    // Try to use connection pool if available, otherwise fall back to traditional approach
    if PooledConnection == nil || PooledConnection.Client == nil {
        // Get a connection from the pool
        PooledConnection, err = GetAccountsConnection()
        if err != nil {
            return nil, fmt.Errorf("failed to get connection from pool: %w", err)
        }
    }

	defer func() {
		PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ListAllAccounts"),
		)
		PutAccountsConnection(PooledConnection)
	}()

    // Ensure we're using the accounts database
    if err := ensureAccountsDBSelected(PooledConnection.Client); err != nil {
		PooledConnection.Client.Logger.Logger.Error("Failed to ensure accounts database is selected",
			zap.Error(err),
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ListAllAccounts"),
		)
        return nil, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
    }

    // Get all keys with "account:" prefix
    keys, err := GetAllKeys(PooledConnection.Client, Prefix)
    if err != nil {
		PooledConnection.Client.Logger.Logger.Error("Failed to get Account keys",
            zap.Error(err),
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, "DB_OPs.ListAllAccounts"),
        )
        return nil, err
    }

    // Limit the number of results if needed
    if limit > 0 && len(keys) > limit {
        keys = keys[:limit]
    }

    // Retrieve all KeyDocuments
    docs := make([]*Account, 0, len(keys))
    for _, key := range keys {
		// Convert key into Addr
		tempKey := strings.TrimPrefix(key, Prefix)
		addr := common.HexToAddress(tempKey)
        // Query the DB for the document
        Doc, err := GetAccount(PooledConnection, addr)
        if err != nil {
            PooledConnection.Client.Logger.Logger.Error("Failed to get Account document",
                zap.Error(err),
                zap.String(logging.Connection_database, config.AccountsDBName),
                zap.Time(logging.Created_at, time.Now()),
                zap.String(logging.Log_file, LOG_FILE),
                zap.String(logging.Topic, TOPIC),
                zap.String(logging.Loki_url, LOKI_URL),
                zap.String(logging.Function, "DB_OPs.ListAllAccounts"),
            )
            continue
        }
        docs = append(docs, Doc)
    }

    PooledConnection.Client.Logger.Logger.Info("Successfully retrieved DIDs",
        zap.Int(logging.Count, len(docs)),
        zap.String(logging.Connection_database, config.AccountsDBName),
        zap.Time(logging.Created_at, time.Now()),
        zap.String(logging.Log_file, LOG_FILE),
        zap.String(logging.Topic, TOPIC),
        zap.String(logging.Loki_url, LOKI_URL),
        zap.String(logging.Function, "DB_OPs.ListAllDIDs"),
    )

    return docs, nil
}

// ListDIDsPaginated retrieves a paginated list of DIDs.
// It first fetches all keys (which is fast) and then retrieves full documents only for the requested page.
// This implementation efficiently scans keys without loading all of them into memory.
func ListDIDsPaginated(PooledConnection *config.PooledConnection, limit, offset int, extendedPrefix string) ([]*Account, error) {
	// Ensure we're using the accounts database, which also handles reconnections.
	if err := ensureAccountsDBSelected(PooledConnection.Client); err != nil {
		return nil, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
	}

	paginatedKeys := make([]string, 0, limit)
	keysScanned := 0
	batchSize := 1000  // How many keys to fetch from the DB at a time
	var lastKey []byte // start at prefix floor

	prefix := []byte("did:")
	if extendedPrefix != "" {
		prefix = []byte("did:" + "did:jmdt:" + extendedPrefix)
	}

	// Loop until we have enough keys for our page or we run out of keys in the DB.
	for len(paginatedKeys) < limit {
		var scanResult *schema.Entries
		// Use the existing retry logic from the client for robustness
		err := withRetry(ic, "ScanDIDsPaginated", func() error {
			req := &schema.ScanRequest{
				Prefix:  prefix,
				Limit:   uint64(batchSize),
				SeekKey: lastKey,
				Desc:    false,
			}
			var scanErr error
			scanResult, scanErr = ic.Client.Scan(ic.Ctx, req)
			return scanErr
		})

		if err != nil {
			return nil, fmt.Errorf("failed to scan for DIDs: %w", err)
		}

		if len(scanResult.Entries) == 0 {
			break // No more keys in the database.
		}

		for _, entry := range scanResult.Entries {
			// Check if the current key is past the offset.
			if keysScanned >= offset {
				paginatedKeys = append(paginatedKeys, string(entry.Key))
				// If we have collected enough keys for the page, we can stop.
				if len(paginatedKeys) == limit {
					break
				}
			}
			keysScanned++
		}

		// If we've already collected enough keys, exit the outer loop.
		if len(paginatedKeys) == limit {
			break
		}

		// Prepare for the next batch scan.
		lastKey = scanResult.Entries[len(scanResult.Entries)-1].Key
	}

	// Now, fetch the full documents only for the keys of the current page.
	docs := make([]*DIDDocument, 0, len(paginatedKeys))
	for _, key := range paginatedKeys {
		var doc DIDDocument
		// SafeReadJSON already has retry logic.
		if err := SafeReadJSON(ic, key, &doc); err != nil {
			config.Warning(ic.Logger, "Error reading DID %s, skipping: %v", key, err)
			continue // Skip if a single DID fails to read.
		}
		docs = append(docs, &doc)
	}

	return docs, nil
}

// CountAccounts returns the total number of Accounts in the database.
// This implementation scans keys without loading them all into memory.
func CountAccounts(PooledConnection *config.PooledConnection) (int, error) {
	// Ensure we're using the accounts database, which also handles reconnections.
	if err := ensureAccountsDBSelected(PooledConnection.Client); err != nil {
		return 0, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
	}
	ic := PooledConnection.Client
	var totalKeys int
	batchSize := 1000 // How many keys to fetch from the DB at a time
	var lastKey []byte

	for {
		var scanResult *schema.Entries
		// Use the existing retry logic from the client for robustness
		err := withRetry(ic, "ScanAccountsCount", func() error {
			req := &schema.ScanRequest{
				Prefix:  []byte(Prefix),
				Limit:   uint64(batchSize),
				SeekKey: lastKey,
				Desc:    false,
			}
			var scanErr error
			scanResult, scanErr = ic.Client.Scan(ic.Ctx, req)
			return scanErr
		})

		if err != nil {
			ic.Logger.Logger.Error("Failed to scan for Accounts count",
				zap.Error(err),
				zap.String(logging.Connection_database, config.AccountsDBName),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.CountAccounts"),
			)
			return 0, fmt.Errorf("failed to scan for Accounts count: %w", err)
		}

		count := len(scanResult.Entries)
		totalKeys += count

		if count < batchSize {
			break // Reached the end of the keys.
		}

		// Prepare for the next batch scan.
		lastKey = scanResult.Entries[count-1].Key
	}
	ic.Logger.Logger.Info("Successfully retrieved Accounts count",
		zap.Int(logging.Count, totalKeys),
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.CountAccounts"),
	)
	return totalKeys, nil
}

// GetTransactionsByDID retrieves all transactions associated with a given DID
// This implementation iterates through all blocks to find matching transactions,
// which is more efficient than fetching each transaction individually.
func GetTransactionsByDID(mainDBClient *config.ImmuClient, did string) ([]*config.ZKBlockTransaction, error) {
	// Extract the address from the DID.
	addr, err := ExtractAddressFromDID(did)
	if err != nil {
		return nil, fmt.Errorf("invalid DID format: %w", err)
	}

	// Get the latest block number to define the search range.
	latestBlockNumber, err := GetLatestBlockNumber(mainDBClient)
	if err != nil {
		// GetLatestBlockNumber returns 0, nil if no blocks are found, so we only check for other errors.
		return nil, fmt.Errorf("failed to get latest block number: %w", err)
	}

	if latestBlockNumber == 0 {
		return []*config.ZKBlockTransaction{}, nil // No blocks, so no transactions.
	}

	var matchingTxs []*config.ZKBlockTransaction
	var logger *config.AsyncLogger
	if mainDBClient != nil {
		logger = mainDBClient.Logger
	} else {
		// Fallback to global pool logger if client is nil, though it's unlikely for this function.
		logger = GetGlobalPool().logger
	}

	// Iterate through each block to find transactions.
	// We start from block 1, as block 0 is often the genesis block and might be handled differently.
	for i := uint64(1); i <= latestBlockNumber; i++ {
		block, err := GetZKBlockByNumber(mainDBClient, i)
		if err != nil {
			config.Warning(logger, "Error retrieving block %d, skipping: %v", i, err)
			continue
		}

		// Check each transaction in the current block.
		for _, tx := range block.Transactions {
			// Convert the DID into common.Address
			sender, err := ExtractAddressFromDID(tx.From)
			if err != nil {
				config.Warning(logger, "Error extracting address from DID %s, skipping: %v", tx.From, err)
				continue
			}
			receiver, err := ExtractAddressFromDID(tx.To)
			if err != nil {
				config.Warning(logger, "Error extracting address from DID %s, skipping: %v", tx.To, err)
				continue
			}

			// Check if the address from the DID matches the sender or receiver by converting them to common.Address
			isSender := tx.From != "" && bytes.Equal(sender.Bytes(), addr.Bytes())
			isReceiver := tx.To != "" && bytes.Equal(receiver.Bytes(), addr.Bytes())

			if isSender || isReceiver {
				matchingTxs = append(matchingTxs, &tx)
			}
		}
	}

	config.Info(logger, "Found %d transactions for DID %s", len(matchingTxs), did)
	return matchingTxs, nil
}

// GetTransactionHashes retrieves transaction hashes with pagination
func GetTransactionHashes(mainDBClient *config.ImmuClient, offset, limit int) ([]string, int, error) {
	// Get total count first
	allHashes, err := getAllTransactionHashes(mainDBClient)
	if err != nil {
		return nil, 0, err
	}

	total := len(allHashes)

	// Apply pagination
	if offset >= total {
		return []string{}, total, nil
	}

	end := offset + limit
	if end > total {
		end = total
	}

	return allHashes[offset:end], total, nil
}

// getAllTransactionHashes gets all transaction hashes (cached)
func getAllTransactionHashes(mainDBClient *config.ImmuClient) ([]string, error) {
	// Use the existing GetAllKeys helper for robustness
	keys, err := GetAllKeys(mainDBClient, "tx:")
	if err != nil {
		return nil, err
	}

	// Extract just the transaction hashes from the keys
	var hashes []string
	for _, key := range keys {
		// Key is in format "tx:<hash>", so we take everything after "tx:"
		if len(key) > 3 {
			hashes = append(hashes, key[3:])
		}
	}

	return hashes, nil
}

// ensureAccountsDBSelected makes sure we're using the accounts database (UNCHANGED)
// This helps prevent the "please select a database first" error
func ensureAccountsDBSelected(ic *config.ImmuClient) error {
	if ic == nil || ic.Client == nil || !ic.IsConnected {
		return fmt.Errorf("client not connected")
	}

	// Try a simple operation to see if the database selection is still valid
	ctx, cancel := context.WithTimeout(ic.BaseCtx, 5*time.Second)
	defer cancel()

	// Use the stored token
	md := metadata.Pairs("authorization", ic.Token)
	ctx = metadata.NewOutgoingContext(ctx, md)

	// Try to execute a simple operation to check if we're still connected
	_, err := ic.Client.CurrentState(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Database state check failed, reconnecting...")
		return reconnectToAccountsDB(ic)
	}

	return nil
}

// ensureAccountsDBSelectedForConnection ensures pooled connection is valid for accounts database
func ensureAccountsDBSelectedForConnection(conn *PooledConnection) error {
	if conn == nil || conn.Client == nil {
		return fmt.Errorf("connection not available")
	}

	// Try a simple operation to see if the database selection is still valid
	_, err := conn.Client.CurrentState(conn.Ctx)
	if err != nil {
		return fmt.Errorf("accounts database connection check failed: %w", err)
	}

	return nil
}

// reconnectToAccountsDB attempts to reestablish a lost connection to the accounts database (UNCHANGED)
func reconnectToAccountsDB(ic *config.ImmuClient) error {
	config.Warning(ic.Logger, "Attempting to reconnect to ImmuDB accounts database")

	// Clean up existing connection if any
	if ic.Cancel != nil {
		ic.Cancel()
	}

	if ic.Client != nil {
		ic.Client.Disconnect()
	}

	ic.IsConnected = false

	// Create a new client
	opts := client.DefaultOptions().
		WithAddress(config.DBAddress).
		WithPort(config.DBPort).
		WithMaxRecvMsgSize(1024 * 1024 * 20) // 20MB message size

	c, err := client.NewImmuClient(opts)
	if err != nil {
		return fmt.Errorf("failed to create client during reconnect: %w", err)
	}

	// Create context
	ctx, cancel := context.WithTimeout(ic.BaseCtx, 30*time.Second)

	// Login to immudb
	config.Info(ic.Logger, "Authenticating with ImmuDB during reconnect")
	lr, err := c.Login(ctx, []byte("immudb"), []byte("immudb"))
	if err != nil {
		cancel()
		c.Disconnect()
		return fmt.Errorf("login failed during reconnect: %w", err)
	}

	// Update token
	ic.Token = lr.Token

	// Add auth token to context
	md := metadata.Pairs("authorization", lr.Token)
	ctx = metadata.NewOutgoingContext(ctx, md)

	// Select the accounts database
	log.Info().Str("database", config.AccountsDBName).Msg("Selecting accounts database during reconnection")
	dbResp, err := c.UseDatabase(ctx, &schema.Database{DatabaseName: config.AccountsDBName})
	if err != nil {
		cancel()
		c.Disconnect()
		return fmt.Errorf("failed to select accounts database during reconnect: %w", err)
	}

	// Update token with database-specific token
	ic.Token = dbResp.Token
	ic.Client = c
	ic.Ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", ic.Token))
	ic.Cancel = cancel
	ic.IsConnected = true

	config.Info(ic.Logger, "Successfully reconnected to ImmuDB accounts database")
	return nil
}

func ExtractAddressFromDID(did string) (common.Address, error) {

	// Check if we get the Correct Addr instead of DID then directly return the addr
	if common.IsHexAddress(did) {
		return common.HexToAddress(did), nil
	}

	parts := strings.Split(did, ":")
	if len(parts) < 4 {
		return common.Address{}, fmt.Errorf("invalid DID format: %s", did)
	}
	// The last part should be the Ethereum address
	addrStr := parts[len(parts)-1]
	if !common.IsHexAddress(addrStr) {
		return common.Address{}, fmt.Errorf("invalid Ethereum address in DID: %s", addrStr)
	}
	return common.HexToAddress(addrStr), nil
}

// Get the DID Document for the address (suffix match on DID key).
func GetDIDDocumentFromAddr(ic *config.ImmuClient, addr string) (*DIDDocument, error) {
	// Ensure we're using the accounts database, which also handles reconnections.
	if err := ensureAccountsDBSelected(ic); err != nil {
		return nil, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
	}

	addrSuffix := strings.ToLower(addr)
	const defaultBatch = 1000
	batchSize := defaultBatch
	var seekKey []byte // start at prefix floor

	prefix := []byte("did:")

	// Helper: next lexicographic key after k (avoids re-reading the last row)
	nextAfter := func(k []byte) []byte {
		if k == nil {
			return nil
		}
		// Appending 0x00 produces a key strictly greater than k in lexicographic order.
		n := make([]byte, len(k)+1)
		copy(n, k)
		n[len(k)] = 0x00
		return n
	}

	for {
		select {
		case <-ic.Ctx.Done():
			return nil, ic.Ctx.Err()
		default:
		}

		var scanResult *schema.Entries
		err := withRetry(ic, "ScanForDIDByAddress", func() error {
			req := &schema.ScanRequest{
				Prefix:  prefix,
				Limit:   uint64(batchSize),
				SeekKey: seekKey,
				Desc:    false,
			}
			var scanErr error
			scanResult, scanErr = ic.Client.Scan(ic.Ctx, req)
			return scanErr
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan for DIDs by address: %w", err)
		}

		if len(scanResult.Entries) == 0 {
			break // exhausted
		}

		for _, entry := range scanResult.Entries {
			// Optional guard: ensure we didn't spill out of prefix (defensive)
			if !bytes.HasPrefix(entry.Key, prefix) {
				return nil, ErrNotFound
			}

			// Lowercase once on bytes, then do suffix check.
			lowerKey := bytes.ToLower(entry.Key)
			if strings.HasSuffix(string(lowerKey), addrSuffix) {
				var doc DIDDocument

				// If the scan returned values, prefer them to avoid a second GET.
				if len(entry.Value) > 0 {
					if uErr := json.Unmarshal(entry.Value, &doc); uErr == nil {
						return &doc, nil
					}
					// Fall back to SafeReadJSON if value wasn’t present/parsable.
				}

				if readErr := SafeReadJSON(ic, string(entry.Key), &doc); readErr == nil {
					return &doc, nil
				} else {
					config.Warning(ic.Logger, "Error reading DID document for key %q, skipping: %v", string(entry.Key), readErr)
				}
			}
		}

		// Advance seekKey strictly after the last key to avoid re-reading it.
		last := scanResult.Entries[len(scanResult.Entries)-1].Key
		// If seekKey is already >= nextAfter(last), we’re stuck – break to avoid loop.
		next := nextAfter(last)
		if seekKey != nil && bytes.Compare(seekKey, next) >= 0 {
			break
		}
		seekKey = next
	}

	return nil, ErrNotFound
}

// ========================================
// OPTIONAL ENHANCED FUNCTIONS FOR CONVENIENCE
// ========================================

// EnableAccountsConnectionPooling enables connection pooling specifically for accounts database
func EnableAccountsConnectionPooling(poolConfig *ConnectionPoolConfig) error {
	return InitializeAccountsPool(poolConfig)
}

// GetAccountsPoolStatistics returns accounts connection pool statistics
func GetAccountsPoolStatistics() map[string]interface{} {
	pool := GetAccountsPool()
	return pool.GetPoolStats()
}

// CloseAccountsPool closes the accounts connection pool
func CloseAccountsPool() error {
	accountsPoolMutex.Lock()
	defer accountsPoolMutex.Unlock()

	if accountsPool != nil {
		err := accountsPool.Close()
		accountsPool = nil
		return err
	}

	return nil
}

// StoreDIDPooled stores a DID using connection pool (convenience function)
func StoreDIDPooled(didDoc *DIDDocument) error {
	return StoreDID(nil, didDoc)
}

// GetDIDPooled retrieves a DID using connection pool (convenience function)
func GetDIDPooled(did string) (*DIDDocument, error) {
	return GetDID(nil, did)
}

// UpdateDIDBalancePooled updates DID balance using connection pool (convenience function)
func UpdateDIDBalancePooled(did string, newBalance string) error {
	return UpdateDIDBalance(nil, did, newBalance)
}

// ListAllDIDsPooled retrieves all DIDs using connection pool (convenience function)
func ListAllDIDsPooled(limit int) ([]*DIDDocument, error) {
	return ListAllDIDs(nil, limit)
}
