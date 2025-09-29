package DB_OPs

import (
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
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
)

const(
	Prefix = "address:"
	DIDPrefix = "did:"
)

var LOKI_URL = logging.GetLokiURL()

const (
	LOG_FILE        = "ImmuDB.log"
	LOG_DIR         = "logs"
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

// This will be stored in the DB
type Account struct {
	// Legacy DID fields (for backward compatibility)
	DIDAddress string `json:"did,omitempty"`

	// New PublicKey based fields
	Address common.Address `json:"address"` // Derived from PublicKey
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

// Create Account from DID and Address and Store using StoreAccount
func CreateAccount(PooledConnection *config.PooledConnection, DIDAddress string, Address common.Address, metadata map[string]interface{}) error {
	var err error
    var AccountDoc *Account
    if DIDAddress == "" || Address == (common.Address{}) {
        return fmt.Errorf("DIDAddress and Address cannot be empty")
    }
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
    
    // Create a Nonce First
    Nonce, err := PutNonceofAccount()
    if err != nil {
        return err
    }
    
    // Create A CreatedAt and UpdatedAt
    CreatedAt := time.Now().UnixNano()
    UpdatedAt := time.Now().UnixNano()

    // Create the account document
    AccountDoc = &Account{
        DIDAddress:  DIDAddress,
        Address:     Address,
        Balance:     "0",
        Nonce:       Nonce,
        AccountType: "user",
        CreatedAt:   CreatedAt,
        UpdatedAt:   UpdatedAt,
        Metadata:    metadata,
    }
    
    // Store the account document
    err = StoreAccount(PooledConnection, AccountDoc)
    if err != nil {
        return err
    }
    
    return nil
}

// StoreAccount stores a Key document in the accounts database and creates a DID reference
func StoreAccount(PooledConnection *config.PooledConnection, KeyDoc *Account) error {
    var err error
    var AccountDoc *Account

    if KeyDoc == nil {
        return fmt.Errorf("Key document cannot be nil")
    }

    if KeyDoc.DIDAddress == "" || KeyDoc.Address == (common.Address{}){
        return fmt.Errorf("DIDAddress and Address cannot be empty")
    }

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

    // Create the account document
    AccountDoc = &Account{
        DIDAddress:  KeyDoc.DIDAddress,
        Address:     KeyDoc.Address,
        Balance:     KeyDoc.Balance,
        Nonce:       KeyDoc.Nonce,
        AccountType: KeyDoc.AccountType,
        CreatedAt:   KeyDoc.CreatedAt,
        UpdatedAt:   time.Now().UnixNano(),
        Metadata:    KeyDoc.Metadata,
    }

    // Create the account key (e.g., "account:<address>")
    accKey := []byte(fmt.Sprintf("%s%s", Prefix, KeyDoc.Address))
    
    // Create the DID key (e.g., "did:did:example:123")
    didKey := []byte(DIDPrefix + KeyDoc.DIDAddress)

    // Ensure we're using the accounts database
    if err := ensureAccountsDBSelected(PooledConnection); err != nil {
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
        zap.String(logging.Account, KeyDoc.Address.Hex()),
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

// shared helper: read & unmarshal an Account by ANY key (account:<addr> OR did:<did>)
func loadAccountByKey(PooledConnection *config.PooledConnection, key []byte, logFn string) (*Account, error) {
    ic := PooledConnection.Client

    if err := ensureAccountsDBSelected(PooledConnection); err != nil {
        return nil, fmt.Errorf("failed to select accounts DB: %w", err)
    }

    entry, err := ic.Client.VerifiedGet(ic.Ctx, key) // deref happens server-side for references
    if err != nil {
        if strings.Contains(err.Error(), "key not found") {
            return nil, ErrNotFound
        }
        ic.Logger.Logger.Error("VerifiedGet failed",
            zap.Error(err),
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, logFn),
			zap.String("proxy_function", "DB_OPs.loadAccountByKey"),
        )
        return nil, err
    }

    var acc Account
    if err := json.Unmarshal(entry.Value, &acc); err != nil {
        ic.Logger.Logger.Error("Unmarshal failed",
            zap.Error(err),
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, logFn),
			zap.String("proxy_function", "DB_OPs.loadAccountByKey"),
        )
        return nil, fmt.Errorf("failed to unmarshal account: %w", err)
    }
	PooledConnection.Client.Logger.Logger.Info("Account loaded successfully",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, logFn),
		zap.String("proxy_function", "DB_OPs.loadAccountByKey"),
	)
    return &acc, nil
}


func GetAccountByDID(PooledConnection *config.PooledConnection, did string) (*Account, error) {
    var err error
    if PooledConnection == nil || PooledConnection.Client == nil {
        PooledConnection, err = GetAccountsConnection()
        if err != nil { return nil, fmt.Errorf("failed to get connection from pool: %w", err) }
    }
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

    didKey := []byte(DIDPrefix + did)
	PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "DB_OPs.GetAccountByDID"),
	)
    return loadAccountByKey(PooledConnection, didKey, "DB_OPs.GetAccountByDID")
}

func GetAccount(PooledConnection *config.PooledConnection, address common.Address) (*Account, error) {
    var err error
    if PooledConnection == nil || PooledConnection.Client == nil {
        PooledConnection, err = GetAccountsConnection()
        if err != nil { return nil, err }
    }
    defer func() {
        PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Function, "DB_OPs.GetAccount"),
        )
        PutAccountsConnection(PooledConnection)
    }()

    key := []byte(fmt.Sprintf("%s%s", Prefix, address))
	PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "DB_OPs.GetAccount"),
	)
    return loadAccountByKey(PooledConnection, key, "DB_OPs.GetAccount")
}


// UpdateAccountBalance updates the balance for a Account
func UpdateAccountBalance(PooledConnection *config.PooledConnection, address common.Address, newBalance string) error {
    var err error
    if PooledConnection == nil || PooledConnection.Client == nil {
        PooledConnection, err = GetAccountsConnection()
        if err != nil { return fmt.Errorf("failed to get connection from pool: %w", err) }
    }
    defer func() {
        PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Function, "DB_OPs.UpdateAccountBalance"),
        )
        PutAccountsConnection(PooledConnection)
    }()
	// Ensure we're using the accounts database
	if PooledConnection != nil {
		if err := ensureAccountsDBSelected(PooledConnection); err != nil {
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
    if err := ensureAccountsDBSelected(PooledConnection); err != nil {
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
    keys, err := GetAllKeys(PooledConnection, Prefix)
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
// ListAccountsPaginated retrieves a paginated list of accounts
func ListAccountsPaginated(PooledConnection *config.PooledConnection, limit, offset int, extendedPrefix string) ([]*Account, error) {
    var err error
    if PooledConnection == nil || PooledConnection.Client == nil {
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
            zap.String(logging.Function, "DB_OPs.ListAccountsPaginated"),
        )
        PutAccountsConnection(PooledConnection)
    }()
	ic := PooledConnection.Client
    // Ensure we're using the accounts database
    if err := ensureAccountsDBSelected(PooledConnection); err != nil {
        return nil, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
    }

    // Build the prefix
    prefix := []byte(DIDPrefix)
    if extendedPrefix != "" {
        prefix = []byte(fmt.Sprintf("%s%s", DIDPrefix, extendedPrefix))
    }

    // Scan for keys with pagination
    var accounts []*Account
    batchSize := 1000
    keysScanned := 0
    var lastKey []byte

    for len(accounts) < limit {
        // Get a batch of keys
        scanReq := &schema.ScanRequest{
            Prefix:  prefix,
            Limit:   uint64(batchSize),
            SeekKey: lastKey,
            Desc:    false,
        }

        scanResult, err := ic.Client.Scan(ic.Ctx, scanReq)
        if err != nil {
            PooledConnection.Client.Logger.Logger.Error("Failed to scan for accounts",
                zap.Error(err),
                zap.String(logging.Connection_database, config.AccountsDBName),
                zap.Time(logging.Created_at, time.Now()),
                zap.String(logging.Log_file, LOG_FILE),
                zap.String(logging.Topic, TOPIC),
                zap.String(logging.Loki_url, LOKI_URL),
                zap.String(logging.Function, "DB_OPs.ListAccountsPaginated"),
            )
            return nil, fmt.Errorf("failed to scan for accounts: %w", err)
        }

        if len(scanResult.Entries) == 0 {
            break // No more keys
        }

        // Process the batch
        for _, entry := range scanResult.Entries {
            if keysScanned >= offset {
                // Load the account using our shared helper
                account, err := loadAccountByKey(PooledConnection, entry.Key, "DB_OPs.ListAccountsPaginated")
                if err != nil {
                    PooledConnection.Client.Logger.Logger.Warn("Skipping account due to error",
                        zap.Error(err),
                        zap.String("key", string(entry.Key)),
                        zap.String(logging.Connection_database, config.AccountsDBName),
                        zap.Time(logging.Created_at, time.Now()),
                        zap.String(logging.Log_file, LOG_FILE),
                        zap.String(logging.Topic, TOPIC),
                        zap.String(logging.Loki_url, LOKI_URL),
                        zap.String(logging.Function, "DB_OPs.ListAccountsPaginated"),
                    )
                    continue
                }
                accounts = append(accounts, account)
                if len(accounts) >= limit {
                    break
                }
            }
            keysScanned++
        }

        if len(scanResult.Entries) < batchSize {
            break // No more keys to fetch
        }

        // Prepare for next batch
        lastKey = scanResult.Entries[len(scanResult.Entries)-1].Key
    }

    PooledConnection.Client.Logger.Logger.Info("Successfully listed accounts",
        zap.Int(logging.Count, len(accounts)),
        zap.Int("requested_limit", limit),
        zap.Int("offset", offset),
        zap.String(logging.Connection_database, config.AccountsDBName),
        zap.Time(logging.Created_at, time.Now()),
        zap.String(logging.Log_file, LOG_FILE),
        zap.String(logging.Topic, TOPIC),
        zap.String(logging.Loki_url, LOKI_URL),
        zap.String(logging.Function, "DB_OPs.ListAccountsPaginated"),
    )

    return accounts, nil
}

// CountAccounts returns the total number of Accounts in the database.
// This implementation scans keys without loading them all into memory.
func CountAccounts(PooledConnection *config.PooledConnection) (int, error) {
	// Ensure we're using the accounts database, which also handles reconnections.
	if err := ensureAccountsDBSelected(PooledConnection); err != nil {
		return 0, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
	}
	ic := PooledConnection.Client
	var totalKeys int
	batchSize := 1000 // How many keys to fetch from the DB at a time
	var lastKey []byte

	for {
		var scanResult *schema.Entries
		// Use the existing retry logic from the client for robustness
		err := withRetry(PooledConnection.Client, "ScanAccountsCount", func() error {
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
// GetTransactionsByAccount retrieves all transactions associated with a given account address
// This implementation uses the connection pool and follows the codebase's patterns
func GetTransactionsByAccount(PooledConnection *config.PooledConnection, accountAddr *common.Address) ([]*config.Transaction, error) {
    var err error
    if PooledConnection == nil || PooledConnection.Client == nil {
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
            zap.String(logging.Function, "DB_OPs.GetTransactionsByAccount"),
        )
        PutAccountsConnection(PooledConnection)
    }()

    ic := PooledConnection.Client

    // Get the latest block number
    latestBlockNumber, err := GetLatestBlockNumber(PooledConnection)
    if err != nil {
        ic.Logger.Logger.Error("Failed to get latest block number",
            zap.Error(err),
            zap.String(logging.Connection_database, config.AccountsDBName),
            zap.Time(logging.Created_at, time.Now()),
            zap.String(logging.Log_file, LOG_FILE),
            zap.String(logging.Topic, TOPIC),
            zap.String(logging.Loki_url, LOKI_URL),
            zap.String(logging.Function, "DB_OPs.GetTransactionsByAccount"),
        )
        return nil, fmt.Errorf("failed to get latest block number: %w", err)
    }

    if latestBlockNumber == 0 {
        return []*config.Transaction{}, nil
    }

    var matchingTxs []*config.Transaction
    batchSize := uint64(100) // Process 100 blocks at a time

    for startBlock := uint64(1); startBlock <= latestBlockNumber; startBlock += batchSize {
        endBlock := startBlock + batchSize - 1
        if endBlock > latestBlockNumber {
            endBlock = latestBlockNumber
        }

        // Process current batch of blocks
        for i := startBlock; i <= endBlock; i++ {
            block, err := GetZKBlockByNumber(PooledConnection, i)
            if err != nil {
                ic.Logger.Logger.Warn("Error retrieving block, skipping",
                    zap.Uint64("block_number", i),
                    zap.Error(err),
                    zap.String(logging.Connection_database, config.AccountsDBName),
                    zap.Time(logging.Created_at, time.Now()),
                    zap.String(logging.Log_file, LOG_FILE),
                    zap.String(logging.Topic, TOPIC),
                    zap.String(logging.Loki_url, LOKI_URL),
                    zap.String(logging.Function, "DB_OPs.GetTransactionsByAccount"),
                )
                continue
            }

            // Check each transaction in the current block
            for _, tx := range block.Transactions {
                // Check if the transaction involves the given account
                if isTransactionInvolvingAccount(tx, accountAddr) {
                    // Create a copy of the transaction to avoid referencing the loop variable
                    txCopy := tx
                    matchingTxs = append(matchingTxs, &txCopy)
                }
            }
        }
    }

    ic.Logger.Logger.Info("Successfully retrieved transactions for account",
        zap.String("account", accountAddr.Hex()),
        zap.Int("transaction_count", len(matchingTxs)),
        zap.String(logging.Connection_database, config.AccountsDBName),
        zap.Time(logging.Created_at, time.Now()),
        zap.String(logging.Log_file, LOG_FILE),
        zap.String(logging.Topic, TOPIC),
        zap.String(logging.Loki_url, LOKI_URL),
        zap.String(logging.Function, "DB_OPs.GetTransactionsByAccount"),
    )

    return matchingTxs, nil
}

// isTransactionInvolvingAccount checks if a transaction involves a specific account
func isTransactionInvolvingAccount(tx config.Transaction, accountAddr *common.Address) bool {
	if tx.From == accountAddr || tx.To == accountAddr {
		return true
	}
	return false
}

// GetTransactionHashes retrieves transaction hashes with pagination
func GetTransactionHashes(PooledConnection *config.PooledConnection, offset, limit int) ([]string, int, error) {
	var err error
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountsConnection()
		if err != nil {
			return nil, 0, fmt.Errorf("failed to get connection from pool: %w", err)
		}
	}
	mainDBClient := PooledConnection.Client

	defer func() {
		mainDBClient.Logger.Logger.Info("Client Connection is returned to the Pool",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetTransactionHashes"),
		)
		PutAccountsConnection(PooledConnection)
	}()

	// Get total count first
	allHashes, err := getAllTransactionHashes(PooledConnection)
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
func getAllTransactionHashes(mainDBClient *config.PooledConnection) ([]string, error) {
	// Use the existing GetAllKeys helper for robustness
	keys, err := GetAllKeys(mainDBClient, DEFAULT_PREFIX_TX)
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
func ensureAccountsDBSelected(PooledConnection *config.PooledConnection) error {
	if PooledConnection == nil || PooledConnection.Client == nil || !PooledConnection.Client.IsConnected {
		return fmt.Errorf("client not connected")
	}

	// Try a simple operation to see if the database selection is still valid
	ctx, cancel := context.WithTimeout(PooledConnection.Client.BaseCtx, 5*time.Second)
	defer cancel()

	// Use the stored token
	md := metadata.Pairs("authorization", PooledConnection.Token)
	ctx = metadata.NewOutgoingContext(ctx, md)

	// Try to execute a simple operation to check if we're still connected
	_, err := PooledConnection.Client.Client.CurrentState(ctx)
	if err != nil {
		PooledConnection.Client.Logger.Logger.Warn("Database state check failed, reconnecting...",
			zap.Error(err),
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ensureAccountsDBSelected"),
		)
		return reconnectToAccountsDB(PooledConnection)
	}

	return nil
}

// ensureAccountsDBSelectedForConnection ensures pooled connection is valid for accounts database
func ensureAccountsDBSelectedForConnection(PooledConnection *config.PooledConnection) error {
	if PooledConnection == nil || PooledConnection.Client == nil {
		return fmt.Errorf("connection not available")
	}

	// Try a simple operation to see if the database selection is still valid
	_, err := PooledConnection.Client.Client.CurrentState(PooledConnection.Client.Ctx)
	if err != nil {
		PooledConnection.Client.Logger.Logger.Warn("Database state check failed, reconnecting...",
			zap.Error(err),
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ensureAccountsDBSelectedForConnection"),
		)
		return fmt.Errorf("accounts database connection check failed: %w", err)
	}
	PooledConnection.Client.Logger.Logger.Info("Accounts database connection check passed",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.ensureAccountsDBSelectedForConnection"),
	)
	return nil
}

// reconnectToAccountsDB attempts to reestablish a lost connection to the accounts database
func reconnectToAccountsDB(PooledConnection *config.PooledConnection) error {
    if PooledConnection == nil {
        return fmt.Errorf("invalid client: nil")
    }
	ic := PooledConnection.Client
    // Log the reconnection attempt
    ic.Logger.Logger.Warn("Attempting to reconnect to ImmuDB accounts database",
        zap.String(logging.Connection_database, config.AccountsDBName),
        zap.Time(logging.Created_at, time.Now()),
        zap.String(logging.Log_file, LOG_FILE),
        zap.String(logging.Topic, TOPIC),
        zap.String(logging.Loki_url, LOKI_URL),
        zap.String(logging.Function, "DB_OPs.reconnectToAccountsDB"),
    )

    // Clean up existing connection if any
    if ic.Cancel != nil {
        ic.Cancel()
    }

    if ic.Client != nil {
        if err := ic.Client.Disconnect(); err != nil {
            ic.Logger.Logger.Warn("Error disconnecting old client",
                zap.Error(err),
                zap.String(logging.Connection_database, config.AccountsDBName),
                zap.Time(logging.Created_at, time.Now()),
                zap.String(logging.Log_file, LOG_FILE),
                zap.String(logging.Topic, TOPIC),
                zap.String(logging.Loki_url, LOKI_URL),
                zap.String(logging.Function, "DB_OPs.reconnectToAccountsDB"),
            )
        }
    }

    ic.IsConnected = false

    // Create a new client with configuration
    opts := client.DefaultOptions().
        WithAddress(config.DBAddress).
        WithPort(config.DBPort).
        WithMaxRecvMsgSize(1024 * 1024 * 20) // 20MB message size

    // Create context with timeout for the connection attempt
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    // Create new client
    c, err := client.NewImmuClient(opts)
    if err != nil {
        return fmt.Errorf("failed to create client during reconnect: %w", err)
    }

    // Login to immudb
    lr, err := c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
    if err != nil {
        _ = c.Disconnect()
        return fmt.Errorf("login failed during reconnect: %w", err)
    }

    // Update token and context
	PooledConnection.Token = lr.Token
    md := metadata.Pairs("authorization", lr.Token)
    ctx = metadata.NewOutgoingContext(ctx, md)

    // Select the accounts database
    dbResp, err := c.UseDatabase(ctx, &schema.Database{DatabaseName: config.AccountsDBName})
    if err != nil {
        _ = c.Disconnect()
		PooledConnection.Client.Logger.Logger.Error("Failed to select accounts database during reconnect",
			zap.Error(err),
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.reconnectToAccountsDB"),
		)
        return fmt.Errorf("failed to select accounts database during reconnect: %w", err)
    }

    // Update client state
    PooledConnection.Token = dbResp.Token
    PooledConnection.Client.Client = c
    PooledConnection.Client.Ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", PooledConnection.Token))
    PooledConnection.Client.IsConnected = true

    // Log successful reconnection
    ic.Logger.Logger.Info("Successfully reconnected to ImmuDB accounts database",
        zap.String(logging.Connection_database, config.AccountsDBName),
        zap.Time(logging.Created_at, time.Now()),
        zap.String(logging.Log_file, LOG_FILE),
        zap.String(logging.Topic, TOPIC),
        zap.String(logging.Loki_url, LOKI_URL),
        zap.String(logging.Function, "DB_OPs.reconnectToAccountsDB"),
    )

    return nil
}