package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gossipnode/config"
	"gossipnode/config/settings"

	"sync/atomic"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc/metadata"
)

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

type AccountsSet struct {
	Accounts map[string]*Account
}

func NewAccountsSet() *AccountsSet {
	return &AccountsSet{
		Accounts: make(map[string]*Account),
	}
}

func (s *AccountsSet) Add(address common.Address) {
	s.Accounts[address.Hex()] = nil
}

// Get the Nonce of a account - NTF
var counter uint64

func PutNonceofAccount() (uint64, error) {
	ts := uint64(time.Now().UTC().UnixNano())
	c := atomic.AddUint64(&counter, 1)
	return ts<<16 | (c & 0xFFFF), nil // embed counter in low bits
}

// Create Account from DID and Address and Store using StoreAccount
func CreateAccount(PooledConnection *config.PooledConnection, DIDAddress string, Address common.Address, metadata map[string]interface{}) error {
	var err error
	var AccountDoc *Account
	var shouldReturnConnection = false

	if DIDAddress == "" || Address == (common.Address{}) {
		return fmt.Errorf("DIDAddress and Address cannot be empty")
	}

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check if we need to get a connection
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("failed to get accounts connection: %w - CreateAccount", err)
		}
		shouldReturnConnection = true // We acquired the connection, so we should return it

		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.CreateAccount"))
	}

	// Only return the connection if we acquired it ourselves
	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.CreateAccount"))
			PutAccountsConnection(PooledConnection)
		}()
	}

	// Create a Nonce First
	Nonce, err := PutNonceofAccount()
	if err != nil {
		return err
	}

	// Create A CreatedAt and UpdatedAt
	CreatedAt := time.Now().UTC().UnixNano()
	UpdatedAt := time.Now().UTC().UnixNano()

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
	// Debugging
	// fmt.Println("AccountDoc: ", AccountDoc)
	// Store the account document
	err = StoreAccount(PooledConnection, AccountDoc)
	if err != nil {
		return err
	}

	return nil
}

// StoreAccount stores a Key document in the accounts database and creates a DID reference
func StoreAccount(PooledConnection *config.PooledConnection, KeyDoc *Account) error {

	// DEFINE NEW GLOBAL REPO USAGE:
	// If GlobalRepo is initialized (meaning the new DB architecture is activated),
	// route this request through the coordinator interfaces instead of standard ImmuDB.
	if repo, ok := GlobalRepo.(interface {
		StoreAccount(context.Context, *Account) error
	}); ok {
		// Create a generous context for the distributed transaction
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return repo.StoreAccount(ctx, KeyDoc)
	}

	// ==========================================
	// LEGACY IMMUDB OPERATION FALLBACK
	// ==========================================

	var err error
	var AccountDoc *Account
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()

	if KeyDoc == nil {
		return fmt.Errorf("key document cannot be nil")
	}

	if KeyDoc.DIDAddress == "" || KeyDoc.Address == (common.Address{}) {
		return fmt.Errorf("DIDAddress and Address cannot be empty")
	}

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("failed to get accounts connection: %w - StoreAccount", err)
		}
		shouldReturnConnection = true // We acquired the connection, so we should return it

		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.StoreAccount"))
	}

	// Use the Client pointer directly instead of dereferencing it
	ic := PooledConnection.Client

	// Return the connection to the pool when done
	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.StoreAccount"))
			PutAccountsConnection(PooledConnection)
		}()
	}

	// Create the account document
	AccountDoc = &Account{
		DIDAddress:  KeyDoc.DIDAddress,
		Address:     KeyDoc.Address,
		Balance:     KeyDoc.Balance,
		Nonce:       KeyDoc.Nonce,
		AccountType: KeyDoc.AccountType,
		CreatedAt:   KeyDoc.CreatedAt,
		UpdatedAt:   time.Now().UTC().UnixNano(),
		Metadata:    KeyDoc.Metadata,
	}

	// Create the account key (e.g., "account:<address>")
	accKey := []byte(fmt.Sprintf("%s%s", Prefix, KeyDoc.Address))

	_, err = PooledConnection.Client.Client.Get(ctx, accKey)
	if err == nil {
		// Account already exists - do not overwrite
		// This prevents "Fake Balance Attack" where a peer propagates a high balance
		// returning nil treats it as "success" (idempotent)
		return nil
	}

	// Create the DID key (e.g., "did:did:example:123")
	didKey := []byte(DIDPrefix + KeyDoc.DIDAddress)

	// Ensure we're using the accounts database
	if err := ensureAccountsDBSelected(PooledConnection); err != nil {
		return fmt.Errorf("failed to ensure accounts database is selected: %w - StoreAccount", err)
	}

	// Marshal the account document
	var val []byte
	val, err = json.Marshal(AccountDoc)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Error(loggerCtx, "Failed to marshal account document",
			err,
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.StoreAccount"))
		return fmt.Errorf("failed to marshal account document: %w", err)
	}

	// Create atomic operations:
	// 1. Store the account document
	// 2. Create a reference from DID to account
	ops := []*schema.Op{
		{Operation: &schema.Op_Kv{Kv: &schema.KeyValue{Key: accKey, Value: val}}},
		{Operation: &schema.Op_Ref{Ref: &schema.ReferenceRequest{
			Key:           didKey,
			ReferencedKey: accKey,
			AtTx:          0,
			BoundRef:      true,
		}}},
	}

	// Execute all operations atomically
	status, err := ic.Client.ExecAll(ctx, &schema.ExecAllRequest{Operations: ops})
	// Debugging
	// fmt.Println("Executed ExecAll function and Status: ", status.String())
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Error(loggerCtx, "Failed to store account and create DID reference",
			err,
			ion.String("status", status.String()),
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.StoreAccount"))
		return fmt.Errorf("failed to store account and create DID reference: %w", err)
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Debug(loggerCtx, "Successfully stored account and created DID reference",
		ion.String("status", status.String()),
		ion.String("account", KeyDoc.Address.Hex()),
		ion.String("did", KeyDoc.DIDAddress),
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.StoreAccount"))

	return nil
}

// BatchCreateAccountsOrdered stores multiple key-value pairs in accountsdb preserving order
func BatchCreateAccountsOrdered(PooledConnection *config.PooledConnection, entries []struct {
	Key   string
	Value []byte
}) error {
	if len(entries) == 0 {
		return fmt.Errorf("entries cannot be empty")
	}

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var err error
	var shouldReturnConnection bool
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("failed to get accounts connection: %w - BatchCreateAccountsOrdered", err)
		}
		shouldReturnConnection = true
	}
	if shouldReturnConnection {
		defer PutAccountsConnection(PooledConnection)
	}
	if err := ensureAccountsDBSelected(PooledConnection); err != nil {
		return fmt.Errorf("failed to select accounts database: %w - BatchCreateAccountsOrdered", err)
	}
	ops := make([]*schema.Op, 0, len(entries))
	for _, e := range entries {
		if e.Key == "" || e.Value == nil {
			return fmt.Errorf("invalid entry (empty key or nil value)")
		}
		ops = append(ops, &schema.Op{Operation: &schema.Op_Kv{Kv: &schema.KeyValue{Key: []byte(e.Key), Value: e.Value}}})
	}
	_, err = PooledConnection.Client.Client.ExecAll(ctx, &schema.ExecAllRequest{Operations: ops})
	if err != nil {
		return fmt.Errorf("accounts batch operation failed: %w - BatchCreateAccountsOrdered", err)
	}
	return nil
}

// BatchRestoreAccounts applies a batch of entries into accountsdb.
// For address:<addr> keys it writes KV. For did:<did> it creates a bound reference to the corresponding address key.
func BatchRestoreAccounts(PooledConnection *config.PooledConnection, entries []struct {
	Key   string
	Value []byte
}) error {
	if len(entries) == 0 {
		return fmt.Errorf("entries cannot be empty")
	}
	var err error
	var shouldReturnConnection bool

	// Define Function wide context for timeout
	ctx := context.Background()

	// End the context.Background()
	defer ctx.Done()

	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("failed to get accounts connection: %w - BatchRestoreAccounts", err)
		}
		shouldReturnConnection = true
	}
	if shouldReturnConnection {
		defer PutAccountsConnection(PooledConnection)
	}
	if err := ensureAccountsDBSelected(PooledConnection); err != nil {
		return fmt.Errorf("failed to select accounts database: %w - BatchRestoreAccounts", err)
	}

	// Separate address: and did: keys to ensure proper ordering
	var addressEntries []struct {
		Key   string
		Value []byte
	}
	var didEntries []struct {
		Key   string
		Value []byte
	}

	for _, e := range entries {
		if e.Key == "" || e.Value == nil {
			return fmt.Errorf("invalid entry (empty key or nil value)")
		}
		if strings.HasPrefix(e.Key, Prefix) {
			addressEntries = append(addressEntries, e)
		} else if strings.HasPrefix(e.Key, DIDPrefix) {
			didEntries = append(didEntries, e)
		}
	}

	// Build a map of address keys being written in this batch for quick lookup
	addressKeysInBatch := make(map[string]bool)
	for _, e := range addressEntries {
		addressKeysInBatch[e.Key] = true
	}

	// Build a map of DID entries grouped by their address key
	didEntriesByAddress := make(map[string][]struct {
		Key   string
		Value []byte
	})
	for _, e := range didEntries {
		var acc Account
		if err := json.Unmarshal(e.Value, &acc); err == nil {
			addrKey := fmt.Sprintf("%s%s", Prefix, acc.Address)
			didEntriesByAddress[addrKey] = append(didEntriesByAddress[addrKey], e)
		}
	}

	ops := make([]*schema.Op, 0, len(entries))

	// Process address: keys first (with LWW logic)
	for _, e := range addressEntries {
		var shouldWrite = true
		var incoming Account
		if err := json.Unmarshal(e.Value, &incoming); err == nil {
			// Try read existing account
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			entry, getErr := PooledConnection.Client.Client.Get(ctx, []byte(e.Key))
			cancel()
			if getErr == nil && entry != nil && len(entry.Value) > 0 {
				var existing Account
				if jsonErr := json.Unmarshal(entry.Value, &existing); jsonErr == nil {
					// If existing is newer, skip writing to preserve newer balance
					if existing.UpdatedAt > incoming.UpdatedAt {
						// Remove from batch map since we're not writing it
						delete(addressKeysInBatch, e.Key)
						shouldWrite = false
					} else if existing.UpdatedAt == incoming.UpdatedAt {
						// If timestamps are equal, only update if incoming has different balance
						// This handles race conditions where sync happens during local update
						if existing.Balance == incoming.Balance {
							// Same timestamp and balance - skip to avoid unnecessary write
							delete(addressKeysInBatch, e.Key)
							shouldWrite = false
						}
						// Same timestamp but different balance - write it (takes newer data)
					}
					// incoming.UpdatedAt > existing.UpdatedAt - we write the newer data
					if shouldWrite && existing.UpdatedAt < incoming.UpdatedAt {
						loggerCtx, cancel := context.WithCancel(context.Background())
						defer cancel()
						PooledConnection.Client.Logger.Debug(loggerCtx, "Updating account - incoming is newer (LWW)",
							ion.String("key", e.Key),
							ion.Int64("existing_updated_at", existing.UpdatedAt),
							ion.Int64("incoming_updated_at", incoming.UpdatedAt),
							ion.String("existing_balance", existing.Balance),
							ion.String("incoming_balance", incoming.Balance),
							ion.String("database", config.AccountsDBName),
							ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
							ion.String("log_file", LOG_FILE),
							ion.String("topic", TOPIC),
							ion.String("function", "DB_OPs.BatchRestoreAccounts"))
					}
				}
				// If existing unmarshal fails, proceed with write (shouldWrite = true)
			}
		} else {
			// Account doesn't exist yet - we'll create it
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Creating new account during sync",
				ion.String("key", e.Key),
				ion.Int64("incoming_updated_at", incoming.UpdatedAt),
				ion.String("incoming_balance", incoming.Balance),
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.BatchRestoreAccounts"))
		}

		if shouldWrite {
			// Write the address: key with incoming data (which is newer or equal)
			ops = append(ops, &schema.Op{Operation: &schema.Op_Kv{Kv: &schema.KeyValue{Key: []byte(e.Key), Value: e.Value}}})

			// Create all did: references that point to this address key in the same transaction
			if didRefs, hasRefs := didEntriesByAddress[e.Key]; hasRefs {
				for _, didEntry := range didRefs {
					didKey := []byte(didEntry.Key)
					ops = append(ops, &schema.Op{Operation: &schema.Op_Ref{Ref: &schema.ReferenceRequest{
						Key:           didKey,
						ReferencedKey: []byte(e.Key),
						AtTx:          0,
						BoundRef:      true,
					}}})
				}
			}
		}
	}

	// Process remaining did: entries that point to address keys not in this batch
	for _, e := range didEntries {
		var acc Account
		if err := json.Unmarshal(e.Value, &acc); err != nil {
			continue
		}
		addrKey := fmt.Sprintf("%s%s", Prefix, acc.Address)

		// If address key was in batch but skipped, or not in batch at all
		if !addressKeysInBatch[addrKey] {
			// Check if address key exists in database
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, getErr := PooledConnection.Client.Client.Get(ctx, []byte(addrKey))
			cancel()
			if getErr == nil {
				// Address key exists in DB - create reference
				didKey := []byte(e.Key)
				ops = append(ops, &schema.Op{Operation: &schema.Op_Ref{Ref: &schema.ReferenceRequest{
					Key:           didKey,
					ReferencedKey: []byte(addrKey),
					AtTx:          0,
					BoundRef:      true,
				}}})
			}
			// If getErr != nil, address key doesn't exist - skip creating orphaned reference
		}
		// If addressKeysInBatch[addrKey] is true, we already processed it above
	}

	// Process did: keys after address: keys are updated
	for _, e := range didEntries {
		// For DID keys, create a reference to the address key
		var acc Account
		if err := json.Unmarshal(e.Value, &acc); err != nil {
			// If payload is not an Account, skip creating ref to avoid corrupt data
			continue
		}
		addrKey := fmt.Sprintf("%s%s", Prefix, acc.Address)

		// Check if address key is being written in this batch OR already exists in DB
		// This ensures references are only created for valid address keys
		shouldCreateRef := false
		if addressKeysInBatch[addrKey] {
			// Address key is being written in this batch - safe to create reference
			shouldCreateRef = true
		} else {
			// Check if address key exists in database
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, getErr := PooledConnection.Client.Client.Get(ctx, []byte(addrKey))
			cancel()
			if getErr == nil {
				// Address key exists in database - safe to create reference
				shouldCreateRef = true
			}
		}

		if !shouldCreateRef {
			// Address key doesn't exist - skip creating reference
			// This can happen if address: key was skipped due to LWW or was never synced
			continue
		}

		didKey := []byte(e.Key)
		ops = append(ops, &schema.Op{Operation: &schema.Op_Ref{Ref: &schema.ReferenceRequest{
			Key:           didKey,
			ReferencedKey: []byte(addrKey),
			AtTx:          0,
			BoundRef:      true,
		}}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if len(ops) == 0 {
		// Nothing to apply (e.g., all entries skipped by LWW) -> treat as success
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "No operations to apply in batch restore (all skipped by LWW)",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.BatchRestoreAccounts"))
		return nil
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Executing batch restore",
		ion.Int("total_operations", len(ops)),
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.BatchRestoreAccounts"))

	_, err = PooledConnection.Client.Client.ExecAll(ctx, &schema.ExecAllRequest{Operations: ops})
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Error(loggerCtx, "Batch restore ExecAll failed",
			err,
			ion.Int("operations_count", len(ops)),
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.BatchRestoreAccounts"))
		return fmt.Errorf("accounts batch restore failed: %w", err)
	}

	loggerCtx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	PooledConnection.Client.Logger.Debug(loggerCtx2, "Batch restore completed successfully",
		ion.Int("operations_applied", len(ops)),
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.BatchRestoreAccounts"))
	return nil
}

// shared helper: read & unmarshal an Account by ANY key (account:<addr> OR did:<did>)
func loadAccountByKey(PooledConnection *config.PooledConnection, key []byte, logFn string) (*Account, error) {
	var err error
	ic := PooledConnection.Client
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get connection from pool: %w", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", logFn))
	}

	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", logFn))
			PutAccountsConnection(PooledConnection)
		}()
	}

	if err := ensureAccountsDBSelected(PooledConnection); err != nil {
		return nil, fmt.Errorf("failed to select accounts DB: %w", err)
	}

	entry, err := ic.Client.Get(ctx, key) // Get follows references automatically
	if err != nil {
		if strings.Contains(err.Error(), "key not found") {
			return nil, ErrNotFound
		}
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Error(loggerCtx, "VerifiedGet failed",
			err,
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", logFn),
			ion.String("proxy_function", "DB_OPs.loadAccountByKey"))
		return nil, err
	}

	var acc Account
	if err := json.Unmarshal(entry.Value, &acc); err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Error(loggerCtx, "Unmarshal failed",
			err,
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", logFn),
			ion.String("proxy_function", "DB_OPs.loadAccountByKey"))
		return nil, fmt.Errorf("failed to unmarshal account: %w", err)
	}
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Account loaded successfully",
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", logFn),
		ion.String("proxy_function", "DB_OPs.loadAccountByKey"))
	return &acc, nil
}

func GetAccountByDID(PooledConnection *config.PooledConnection, did string) (*Account, error) {

	// DEFINE NEW GLOBAL REPO USAGE:
	if repo, ok := GlobalRepo.(interface {
		GetAccountByDID(context.Context, string) (*Account, error)
	}); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		acc, err := repo.GetAccountByDID(ctx, did)
		if err == nil && acc != nil {
			return acc, nil
		}
		// If custom repo fails, we fall through to ImmuDB as a safety mechanism during migration
	}

	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get connection from pool: %w - GetAccountByDID", err)
		}
		shouldReturnConnection = true
	}

	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetAccountByDID"))
			PutAccountsConnection(PooledConnection)
		}()
	}

	didKey := []byte(DIDPrefix + did)
	return loadAccountByKey(PooledConnection, didKey, "DB_OPs.GetAccountByDID")
}

func GetAccount(PooledConnection *config.PooledConnection, address common.Address) (*Account, error) {

	// DEFINE NEW GLOBAL REPO USAGE:
	if repo, ok := GlobalRepo.(interface {
		GetAccount(context.Context, common.Address) (*Account, error)
	}); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		acc, err := repo.GetAccount(ctx, address)
		if err == nil && acc != nil {
			return acc, nil
		}
		// If custom repo fails, we fall through to ImmuDB as a safety mechanism during migration
	}

	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get connection from pool: %w - GetAccount", err)
		}
		shouldReturnConnection = true
	}

	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetAccount"))
			PutAccountsConnection(PooledConnection)
		}()
	}

	key := []byte(fmt.Sprintf("%s%s", Prefix, address))
	return loadAccountByKey(PooledConnection, key, "DB_OPs.GetAccount")
}

// UpdateAccountBalance updates the balance for a Account
func UpdateAccountBalance(PooledConnection *config.PooledConnection, address common.Address, newBalance string) error {

	// DEFINE NEW GLOBAL REPO USAGE:
	if repo, ok := GlobalRepo.(interface {
		UpdateAccountBalance(context.Context, common.Address, string) error
	}); ok {
		// Use generous timeout for distributed tx across DBs
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return repo.UpdateAccountBalance(ctx, address, newBalance)
	}

	// ==========================================
	// LEGACY IMMUDB OPERATION FALLBACK
	// ==========================================

	fmt.Printf("=== DEBUG: UpdateAccountBalance called for address %s with balance %s ===\n", address.Hex(), newBalance)

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturnConnection = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		fmt.Println("DEBUG: PooledConnection is nil, getting new connection from pool")
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			fmt.Printf("DEBUG: Failed to get connection from pool: %v\n", err)
			return fmt.Errorf("failed to get connection from pool: %w - UpdateAccountBalance", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.UpdateAccountBalance"))
	} else {
		fmt.Println("DEBUG: Using provided PooledConnection")
	}

	if shouldReturnConnection {
		defer func() {
			fmt.Println("DEBUG: Returning connection to pool")
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.UpdateAccountBalance"))
			PutAccountsConnection(PooledConnection)
		}()
	}

	// Ensure we're using the accounts database
	if PooledConnection != nil {
		fmt.Println("DEBUG: Ensuring accounts database is selected")
		if err := ensureAccountsDBSelected(PooledConnection); err != nil {
			fmt.Printf("DEBUG: Failed to ensure accounts database is selected: %v\n", err)
			return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
		}
		fmt.Println("DEBUG: Accounts database selection confirmed")
	}

	fmt.Printf("DEBUG: Getting account for address %s\n", address.Hex())
	doc, err := GetAccount(PooledConnection, address)
	if err != nil {
		fmt.Printf("DEBUG: Failed to get account: %v\n", err)
		return err
	}
	fmt.Printf("DEBUG: Retrieved account - Current balance: %s, UpdatedAt: %d\n", doc.Balance, doc.UpdatedAt)

	doc.Balance = newBalance
	doc.UpdatedAt = time.Now().UTC().UnixNano()
	fmt.Printf("DEBUG: Updated account document - New balance: %s, New UpdatedAt: %d\n", doc.Balance, doc.UpdatedAt)

	// Safe Write to the DB with the same key
	key := fmt.Sprintf("%s%s", Prefix, address)
	fmt.Printf("DEBUG: Writing to database with key: %s\n", key)
	err = SafeCreate(PooledConnection.Client, key, doc)
	if err != nil {
		fmt.Printf("DEBUG: SafeCreate failed: %v\n", err)
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to update DID balance",
			err,
			ion.String("account", address.String()),
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.UpdateAccountBalance"))
		return err
	}
	fmt.Println("DEBUG: SafeCreate completed successfully")

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Successfully updated Account balance",
		ion.String("account", address.String()),
		ion.String("new_balance", newBalance),
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.UpdateAccountBalance"))
	fmt.Printf("=== DEBUG: UpdateAccountBalance completed successfully for address %s ===\n", address.Hex())
	return nil
}

// ListAllAccounts retrieves all Accounts with a limit
func ListAllAccounts(PooledConnection *config.PooledConnection, limit int) ([]*Account, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if PooledConnection == nil || PooledConnection.Client == nil {
		// Get a connection from the pool
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get connection from pool: %w - ListAllAccounts", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ListAllAccounts"))
	}

	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.ListAllAccounts"))
			PutAccountsConnection(PooledConnection)
		}()
	}

	// Ensure we're using the accounts database
	if err := ensureAccountsDBSelected(PooledConnection); err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to ensure accounts database is selected",
			err,
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ListAllAccounts"))
		return nil, fmt.Errorf("failed to ensure accounts database is selected: %w - ListAllAccounts", err)
	}

	// Get all keys with "account:" prefix
	keys, err := GetAllKeys(PooledConnection, Prefix)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to get Account keys",
			err,
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ListAllAccounts"))
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
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Error(loggerCtx, "Failed to get Account document",
				err,
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.ListAllAccounts"))
			continue
		}
		docs = append(docs, Doc)
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Successfully retrieved accounts",
		ion.Int("count", len(docs)),
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ListAllAccounts"))

	return docs, nil
}

// ListDIDsPaginated retrieves a paginated list of DIDs.
// It first fetches all keys (which is fast) and then retrieves full documents only for the requested page.
// This implementation efficiently scans keys without loading all of them into memory.
// ListAccountsPaginated retrieves a paginated list of accounts
func ListAccountsPaginated(PooledConnection *config.PooledConnection, limit, offset int, extendedPrefix string) ([]*Account, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx := context.Background()
	// End the context.Background()
	defer ctx.Done()

	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get connection from pool: %w - ListAccountsPaginated", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ListAccountsPaginated"))
	}
	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.ListAccountsPaginated"))
			PutAccountsConnection(PooledConnection)
		}()
	}
	ic := PooledConnection.Client
	// Ensure we're using the accounts database
	if err := ensureAccountsDBSelected(PooledConnection); err != nil {
		return nil, fmt.Errorf("failed to ensure accounts database is selected: %w - ListAccountsPaginated", err)
	}

	// Scan for address: keys instead of did: keys
	// This is more reliable because:
	// 1. address: keys are regular KV pairs, always scannable by ImmuDB Scan
	// 2. did: references might not appear in Scan results
	// 3. Every account has an address: key, so we'll get all accounts
	// 4. This works for both locally created and synced accounts
	prefix := []byte(Prefix) // Use "address:" prefix instead of "did:"

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
			Desc:    true, // latest accounts first
		}
		ReadCtx, ReadCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ReadCancel()
		scanResult, err := ic.Client.Scan(ReadCtx, scanReq)
		if err != nil {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Error(loggerCtx, "Failed to scan for accounts",
				err,
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.ListAccountsPaginated"))
			return nil, fmt.Errorf("failed to scan for accounts: %w - ListAccountsPaginated", err)
		}

		if len(scanResult.Entries) == 0 {
			break // No more keys
		}

		// Check for infinite loop detection (SeekKey is inclusive)
		// If the first key matches the seek key, we need to skip it
		startIndex := 0
		if len(scanResult.Entries) > 0 && lastKey != nil && string(scanResult.Entries[0].Key) == string(lastKey) {
			startIndex = 1
		}

		// Process the batch
		for i := startIndex; i < len(scanResult.Entries); i++ {
			entry := scanResult.Entries[i]
			// keysScanned is tracked globally across batches
			if keysScanned >= offset {
				// Load the account directly from address: key value
				// This works for both synced and locally created accounts
				var acc Account
				if err := json.Unmarshal(entry.Value, &acc); err != nil {
					loggerCtx, cancel := context.WithCancel(context.Background())
					defer cancel()
					PooledConnection.Client.Logger.Warn(loggerCtx, "Skipping account due to unmarshal error",
						ion.String("error", err.Error()),
						ion.String("key", string(entry.Key)),
						ion.String("database", config.AccountsDBName),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("log_file", LOG_FILE),
						ion.String("topic", TOPIC),
						ion.String("function", "DB_OPs.ListAccountsPaginated"))
					continue
				}

				// Filter by network prefix if specified (e.g., "did:jmdt:mainnet:")
				if extendedPrefix != "" && !strings.HasPrefix(acc.DIDAddress, extendedPrefix) {
					keysScanned++
					continue
				}

				accounts = append(accounts, &acc)
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

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Successfully listed accounts",
		ion.Int("count", len(accounts)),
		ion.Int("requested_limit", limit),
		ion.Int("offset", offset),
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ListAccountsPaginated"))

	return accounts, nil
}

// CountAccounts returns the total number of Accounts in the database.
// This implementation scans keys without loading them all into memory.
func CountAccounts(PooledConnection *config.PooledConnection) (int, error) {
	count, err := CountBuilder{}.GetAccountsDBCount(Prefix)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetTransactionsByDID retrieves all transactions associated with a given DID
// This implementation iterates through all blocks to find matching transactions,
// which is more efficient than fetching each transaction individually.
// GetTransactionsByAccount retrieves all transactions associated with a given account address
// This implementation uses the MAIN database connection pool (not accounts) since transactions are stored in main DB
func GetTransactionsByAccount(PooledConnection *config.PooledConnection, accountAddr *common.Address) ([]*config.Transaction, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		// Use MAIN database connection since transactions are stored in main DB
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection from pool: %w - GetTransactionsByAccount", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionsByAccount"))
	}
	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetTransactionsByAccount"))
			PutMainDBConnection(PooledConnection)
		}()
	}

	ic := PooledConnection.Client

	// Get the latest block number
	latestBlockNumber, err := GetLatestBlockNumber(PooledConnection)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Error(loggerCtx, "Failed to get latest block number",
			err,
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionsByAccount"))
		return nil, fmt.Errorf("failed to get latest block number: %w", err)
	}

	var matchingTxs []*config.Transaction
	batchSize := uint64(100) // Process 100 blocks at a time

	// Start from block 0 (genesis block) to include all blocks
	for startBlock := uint64(0); startBlock <= latestBlockNumber; startBlock += batchSize {
		endBlock := startBlock + batchSize - 1
		if endBlock > latestBlockNumber {
			endBlock = latestBlockNumber
		}

		// Process current batch of blocks
		for i := startBlock; i <= endBlock; i++ {
			block, err := GetZKBlockByNumber(PooledConnection, i)
			if err != nil {
				loggerCtx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ic.Logger.Warn(loggerCtx, "Error retrieving block, skipping",
					ion.String("error", err.Error()),
					ion.Uint64("block_number", i),
					ion.String("database", config.AccountsDBName),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.GetTransactionsByAccount"))
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

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Debug(loggerCtx, "Successfully retrieved transactions for account",
		ion.String("account", accountAddr.Hex()),
		ion.Int("transaction_count", len(matchingTxs)),
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetTransactionsByAccount"))

	return matchingTxs, nil
}

// isTransactionInvolvingAccount checks if a transaction involves a specific account
func isTransactionInvolvingAccount(tx config.Transaction, accountAddr *common.Address) bool {
	// Compare address values, not pointers
	if tx.From != nil && *tx.From == *accountAddr {
		return true
	}
	if tx.To != nil && *tx.To == *accountAddr {
		return true
	}
	return false
}

// CheckNonceDuplicate checks if a transaction with the same (from, nonce) already exists
// Returns true if a duplicate is found, false otherwise
// This function checks confirmed transactions in blocks
func CheckNonceDuplicate(PooledConnection *config.PooledConnection, fromAddr *common.Address, nonce uint64) (bool, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx := context.Background()
	defer ctx.Done()

	if PooledConnection == nil || PooledConnection.Client == nil {
		// Use MAIN database connection since transactions are stored in main DB
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return false, fmt.Errorf("failed to get main DB connection from pool: %w - CheckNonceDuplicate", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.CheckNonceDuplicate"))
	}
	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.CheckNonceDuplicate"))
			PutMainDBConnection(PooledConnection)
		}()
	}

	ic := PooledConnection.Client

	// Get all transactions for the from address
	transactions, err := GetTransactionsByAccount(PooledConnection, fromAddr)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Error(loggerCtx, "Failed to get transactions for nonce check",
			err,
			ion.String("from_address", fromAddr.Hex()),
			ion.Uint64("nonce", nonce),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.CheckNonceDuplicate"))
		return false, fmt.Errorf("failed to get transactions for nonce check: %w", err)
	}

	// Check if any transaction has the same nonce and from address
	for _, tx := range transactions {
		if tx.From != nil && *tx.From == *fromAddr && tx.Nonce == nonce {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ic.Logger.Warn(loggerCtx, "Duplicate nonce found",
				ion.String("from_address", fromAddr.Hex()),
				ion.Uint64("nonce", nonce),
				ion.String("existing_tx_hash", tx.Hash.Hex()),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.CheckNonceDuplicate"))
			return true, nil
		}
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Debug(loggerCtx, "No duplicate nonce found",
		ion.String("from_address", fromAddr.Hex()),
		ion.Uint64("nonce", nonce),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.CheckNonceDuplicate"))

	return false, nil
}

// GetLatestNonce retrieves the latest (highest) nonce for a given account address
// Returns the latest nonce and an error if any
// If no transactions exist for the account, returns 0 (indicating first transaction)
func GetLatestNonce(PooledConnection *config.PooledConnection, fromAddr *common.Address) (uint64, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		// Use MAIN database connection since transactions are stored in main DB
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return 0, fmt.Errorf("failed to get main DB connection from pool: %w - GetLatestNonce", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetLatestNonce"))
	}
	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetLatestNonce"))
			PutMainDBConnection(PooledConnection)
		}()
	}

	ic := PooledConnection.Client

	// Get all transactions for the from address
	transactions, err := GetTransactionsByAccount(PooledConnection, fromAddr)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Error(loggerCtx, "Failed to get transactions for latest nonce check",
			err,
			ion.String("from_address", fromAddr.Hex()),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetLatestNonce"))
		return 0, fmt.Errorf("failed to get transactions for latest nonce check: %w", err)
	}

	// If no transactions exist, return 0 (first transaction will have nonce 0 or 1)
	if len(transactions) == 0 {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Debug(loggerCtx, "No transactions found for account, returning 0 as latest nonce",
			ion.String("from_address", fromAddr.Hex()),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetLatestNonce"))
		return 0, nil
	}

	// Find the maximum nonce among transactions from this address
	var latestNonce uint64 = 0
	for _, tx := range transactions {
		if tx.From != nil && *tx.From == *fromAddr {
			if tx.Nonce > latestNonce {
				latestNonce = tx.Nonce
			}
		}
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Debug(loggerCtx, "Successfully retrieved latest nonce for account",
		ion.String("from_address", fromAddr.Hex()),
		ion.Uint64("latest_nonce", latestNonce),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetLatestNonce"))

	return latestNonce, nil
}

// GetTransactionsByAccountPaginated retrieves paginated transactions for a given account address
// This implementation scans blocks in reverse order (latest first) and stops early once it has
// collected enough transactions for the requested page, making it much faster than GetTransactionsByAccount
// for accounts with many transactions.
// Returns: transactions for the requested page, total count (if available), and error
func GetTransactionsByAccountPaginated(PooledConnection *config.PooledConnection, accountAddr *common.Address, offset, limit int) ([]*config.Transaction, int, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		// Use MAIN database connection since transactions are stored in main DB
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to get main DB connection from pool: %w - GetTransactionsByAccountPaginated", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionsByAccountPaginated"))
	}
	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetTransactionsByAccountPaginated"))
			PutMainDBConnection(PooledConnection)
		}()
	}

	ic := PooledConnection.Client

	// Get the latest block number
	latestBlockNumber, err := GetLatestBlockNumber(PooledConnection)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Error(loggerCtx, "Failed to get latest block number",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionsByAccountPaginated"))
		return nil, 0, fmt.Errorf("failed to get latest block number: %w", err)
	}

	// Calculate how many transactions we need to collect
	// We need: offset + limit transactions total
	transactionsNeeded := offset + limit

	var allMatchingTxs []*config.Transaction
	batchSize := uint64(100)         // Process 100 blocks at a time
	maxBlocksToScan := uint64(10000) // Safety limit
	blocksScanned := uint64(0)

	// Start from latest block and go backwards (reverse order)
	// This ensures we get the most recent transactions first
	for currentBlock := latestBlockNumber; currentBlock > 0 && len(allMatchingTxs) < transactionsNeeded && blocksScanned < maxBlocksToScan; {
		// Determine the batch range (going backwards)
		var startBlock uint64
		if currentBlock >= batchSize {
			startBlock = currentBlock - batchSize + 1
		} else {
			startBlock = 0
		}

		// Process current batch of blocks (in reverse order)
		for i := currentBlock; i >= startBlock && len(allMatchingTxs) < transactionsNeeded; i-- {
			block, err := GetZKBlockByNumber(PooledConnection, i)
			if err != nil {
				loggerCtx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ic.Logger.Warn(loggerCtx, "Error retrieving block, skipping",
					ion.String("error", err.Error()),
					ion.Uint64("block_number", i),
					ion.String("database", config.DBName),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.GetTransactionsByAccountPaginated"))
				continue
			}

			// Check each transaction in the current block (in reverse order within block)
			// Process transactions in reverse to maintain chronological order (newest first)
			for j := len(block.Transactions) - 1; j >= 0 && len(allMatchingTxs) < transactionsNeeded; j-- {
				tx := block.Transactions[j]
				// Check if the transaction involves the given account
				if isTransactionInvolvingAccount(tx, accountAddr) {
					// Create a copy of the transaction to avoid referencing the loop variable
					txCopy := tx
					allMatchingTxs = append(allMatchingTxs, &txCopy)
				}
			}

			blocksScanned++
		}

		// Move to next batch (going backwards)
		if currentBlock >= batchSize {
			currentBlock = currentBlock - batchSize
		} else {
			break
		}
	}

	// If we don't have enough transactions, we've reached the end
	total := len(allMatchingTxs)

	// Extract only the transactions for the requested page
	var paginatedTxs []*config.Transaction
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		paginatedTxs = allMatchingTxs[offset:end]
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Debug(loggerCtx, "Successfully retrieved paginated transactions for account",
		ion.String("account", accountAddr.Hex()),
		ion.Int("returned_count", len(paginatedTxs)),
		ion.Int("total_found", total),
		ion.Int("offset", offset),
		ion.Int("limit", limit),
		ion.Uint64("blocks_scanned", blocksScanned),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetTransactionsByAccountPaginated"))

	return paginatedTxs, total, nil
}

// GetTransactionHashes retrieves transaction hashes with pagination (DEPRECATED - use GetTransactionsPaginated)
// This function is kept for backward compatibility but loads all hashes into memory
func GetTransactionHashes(PooledConnection *config.PooledConnection, offset, limit int) ([]string, int, error) {
	// Use the new database-level pagination function
	transactions, total, err := GetTransactionsPaginated(PooledConnection, offset, limit)
	if err != nil {
		return nil, 0, err
	}

	// Extract hashes from transactions
	hashes := make([]string, len(transactions))
	for i, tx := range transactions {
		hashes[i] = tx.Hash.Hex() // Convert common.Hash to hex string
	}

	return hashes, total, nil
}

// GetTransactionsPaginated retrieves transactions with database-level pagination
// This uses ImmuDB Scan with SeekKey to paginate at the database level, avoiding loading all transactions into memory
func GetTransactionsPaginated(PooledConnection *config.PooledConnection, offset, limit int) ([]*config.Transaction, int, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Transactions are stored in MAIN database, not accounts DB
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to get main DB connection from pool: %w - GetTransactionsPaginated", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionsPaginated"))
	}
	ic := PooledConnection.Client

	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ic.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetTransactionsPaginated"))
			PutMainDBConnection(PooledConnection)
		}()
	}

	// Get total count efficiently (without loading all transactions)
	// Use the existing CountTransactions function from immuclient.go
	total, err := CountTransactions(PooledConnection)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count transactions: %w", err)
	}

	// If offset is beyond total, return empty result
	if offset >= total {
		return []*config.Transaction{}, total, nil
	}

	// Scan for transactions with database-level pagination
	prefix := []byte(DEFAULT_PREFIX_TX) // "tx:"
	var transactions []*config.Transaction
	batchSize := 1000 // Scan in batches
	keysScanned := 0
	var lastKey []byte

	for len(transactions) < limit {
		// Get a batch of keys from database
		scanReq := &schema.ScanRequest{
			Prefix:  prefix,
			Limit:   uint64(batchSize),
			SeekKey: lastKey,
			Desc:    true, // latest transactions first
		}

		scanCtx, scanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		scanResult, err := ic.Client.Scan(scanCtx, scanReq)
		scanCancel()

		if err != nil {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ic.Logger.Error(loggerCtx, "Failed to scan for transactions",
				err,
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetTransactionsPaginated"))
			return nil, 0, fmt.Errorf("failed to scan for transactions: %w", err)
		}

		if len(scanResult.Entries) == 0 {
			break // No more keys
		}

		// Check for infinite loop detection (SeekKey is inclusive)
		// If the first key matches the seek key, we need to skip it
		startIndex := 0
		if len(scanResult.Entries) > 0 && lastKey != nil && string(scanResult.Entries[0].Key) == string(lastKey) {
			startIndex = 1
		}

		// Process the batch
		for i := startIndex; i < len(scanResult.Entries); i++ {
			entry := scanResult.Entries[i]
			if keysScanned >= offset {
				// Extract transaction hash from key (format: "tx:<hash>")
				keyStr := string(entry.Key)
				if len(keyStr) > len(DEFAULT_PREFIX_TX) {
					txHash := keyStr[len(DEFAULT_PREFIX_TX):]

					// Fetch the full transaction
					tx, err := GetTransactionByHash(PooledConnection, txHash)
					if err != nil {
						loggerCtx, cancel := context.WithCancel(context.Background())
						defer cancel()
						ic.Logger.Warn(loggerCtx, "Skipping transaction due to fetch error",
							ion.String("error", err.Error()),
							ion.String("txHash", txHash),
							ion.String("database", config.DBName),
							ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
							ion.String("log_file", LOG_FILE),
							ion.String("topic", TOPIC),
							ion.String("function", "DB_OPs.GetTransactionsPaginated"))
						keysScanned++
						continue
					}

					transactions = append(transactions, tx)
					if len(transactions) >= limit {
						break
					}
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

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Debug(loggerCtx, "Successfully retrieved paginated transactions",
		ion.Int("count", len(transactions)),
		ion.Int("requested_limit", limit),
		ion.Int("offset", offset),
		ion.Int("total", total),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetTransactionsPaginated"))

	return transactions, total, nil
}

// ensureAccountsDBSelected makes sure we're using the accounts database
// This helps prevent the "please select a database first" error and ensures we're using the correct database
func ensureAccountsDBSelected(PooledConnection *config.PooledConnection) error {
	if PooledConnection == nil || PooledConnection.Client == nil {
		return fmt.Errorf("client not connected")
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use the stored token
	md := metadata.Pairs("authorization", PooledConnection.Token)
	ctx = metadata.NewOutgoingContext(ctx, md)

	// Always ensure we're using the accounts database by calling UseDatabase
	// This is necessary because connections from the pool might be connected to defaultdb
	dbResp, err := PooledConnection.Client.Client.UseDatabase(ctx, &schema.Database{DatabaseName: config.AccountsDBName})
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Warn(loggerCtx, "Failed to select accounts database, reconnecting...",
			ion.String("error", err.Error()),
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureAccountsDBSelected"))
		return reconnectToAccountsDB(PooledConnection)
	}

	// Update the token if it changed
	if dbResp.Token != "" {
		PooledConnection.Token = dbResp.Token
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Successfully ensured accounts database is selected",
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ensureAccountsDBSelected"))

	return nil
}

// reconnectToAccountsDB attempts to reestablish a lost connection to the accounts database
func reconnectToAccountsDB(PooledConnection *config.PooledConnection) error {
	if PooledConnection == nil {
		return fmt.Errorf("invalid client: nil")
	}
	ic := PooledConnection.Client
	// Log the reconnection attempt
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Warn(loggerCtx, "Attempting to reconnect to ImmuDB accounts database",
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.reconnectToAccountsDB"))

	// Clean up existing connection if any
	if ic.Cancel != nil {
		ic.Cancel()
	}

	if ic.Client != nil {
		if err := ic.Client.Disconnect(); err != nil {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ic.Logger.Warn(loggerCtx, "Error disconnecting old client",
				ion.String("error", err.Error()),
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.reconnectToAccountsDB"))
		}
	}

	ic.IsConnected = false

	// Create a new client with configuration
	opts := client.DefaultOptions().
		WithAddress(config.DBAddress).
		WithPort(config.DBPort).
		WithMaxRecvMsgSize(1024 * 1024 * 200) // 20MB message size

	// Create context with timeout for the connection attempt
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create new client
	c, err := client.NewImmuClient(opts)
	if err != nil {
		return fmt.Errorf("failed to create client during reconnect: %w", err)
	}

	// Login to immudb
	lr, err := c.Login(ctx, []byte(settings.Get().Database.Username), []byte(settings.Get().Database.Password))
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
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to select accounts database during reconnect",
			err,
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.reconnectToAccountsDB"))
		return fmt.Errorf("failed to select accounts database during reconnect: %w", err)
	}

	// Update client state
	PooledConnection.Token = dbResp.Token
	PooledConnection.Client.Client = c
	PooledConnection.Client.Ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", PooledConnection.Token))
	PooledConnection.Client.IsConnected = true

	// Log successful reconnection
	loggerCtx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ic.Logger.Debug(loggerCtx2, "Successfully reconnected to ImmuDB accounts database",
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.reconnectToAccountsDB"))

	return nil
}

// CheckNonceAndGetLatest is an optimized function that combines nonce duplicate check
// and latest nonce retrieval in a single reverse scan of blocks.
// This is much faster than calling CheckNonceDuplicate and GetLatestNonce separately
// because it:
// 1. Scans blocks in reverse order (latest to oldest)
// 2. Stops early once it finds the latest nonce and checks for duplicates
// 3. Only checks transactions from the sender address
// Returns: (hasDuplicate, latestNonce, hasAnyTransactions, error)
// hasAnyTransactions indicates if any transactions were found (needed to distinguish
// between "no transactions" (nonce 0 valid) vs "latest transaction has nonce 0" (next should be 1))
func CheckNonceAndGetLatest(PooledConnection *config.PooledConnection, fromAddr *common.Address, submittedNonce uint64) (bool, uint64, bool, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		// Use MAIN database connection since transactions are stored in main DB
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return false, 0, false, fmt.Errorf("failed to get main DB connection from pool: %w - CheckNonceAndGetLatest", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.CheckNonceAndGetLatest"))
	}
	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.CheckNonceAndGetLatest"))
			PutMainDBConnection(PooledConnection)
		}()
	}

	ic := PooledConnection.Client

	// Get the latest block number
	latestBlockNumber, err := GetLatestBlockNumber(PooledConnection)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Error(loggerCtx, "Failed to get latest block number",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.CheckNonceAndGetLatest"))
		return false, 0, false, fmt.Errorf("failed to get latest block number: %w", err)
	}

	var latestNonce uint64 = 0
	foundLatestNonce := false
	hasDuplicate := false

	// Scan blocks in reverse order (latest to oldest) for early termination
	// Process in batches for efficiency
	batchSize := uint64(100)
	maxBlocksToScan := uint64(1000) // Limit scan to recent blocks for performance
	blocksScanned := uint64(0)

	// Start from latest block and go backwards
	for currentBlock := latestBlockNumber; currentBlock > 0 && blocksScanned < maxBlocksToScan; {
		// Determine the batch range (going backwards)
		var startBlock uint64
		if currentBlock >= batchSize {
			startBlock = currentBlock - batchSize + 1
		} else {
			startBlock = 0
		}

		// Process current batch of blocks (in reverse order).
		// Loop is written as a top-decrement to avoid uint64 underflow: if startBlock
		// is 0 and the condition were checked as "i >= startBlock" after decrement,
		// i would wrap to uint64 max on the iteration where i==0, causing an infinite
		// loop that attempts to fetch non-existent blocks near ^uint64(0).
		for i := currentBlock + 1; i > startBlock; {
			i--
			block, err := GetZKBlockByNumber(PooledConnection, i)
			if err != nil {
				loggerCtx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ic.Logger.Warn(loggerCtx, "Error retrieving block, skipping",
					ion.String("error", err.Error()),
					ion.Uint64("block_number", i),
					ion.String("database", config.DBName),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.CheckNonceAndGetLatest"))
				continue
			}

			// Check each transaction in the current block
			// Process in reverse order within block to find latest nonce faster
			for j := len(block.Transactions) - 1; j >= 0; j-- {
				tx := block.Transactions[j]

				// Only check transactions from the sender address
				if tx.From == nil || *tx.From != *fromAddr {
					continue
				}

				// Check for duplicate nonce
				if tx.Nonce == submittedNonce {
					hasDuplicate = true
					loggerCtx, cancel := context.WithCancel(context.Background())
					defer cancel()
					ic.Logger.Warn(loggerCtx, "Duplicate nonce found",
						ion.String("from_address", fromAddr.Hex()),
						ion.Uint64("nonce", submittedNonce),
						ion.String("existing_tx_hash", tx.Hash.Hex()),
						ion.Uint64("block_number", i),
						ion.String("database", config.DBName),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("log_file", LOG_FILE),
						ion.String("topic", TOPIC),
						ion.String("function", "DB_OPs.CheckNonceAndGetLatest"))
				}

				// Update latest nonce if we found a higher one
				if tx.Nonce > latestNonce {
					latestNonce = tx.Nonce
					foundLatestNonce = true
				}
			}

			blocksScanned++

			// Early termination: if we found the latest nonce and checked for duplicates,
			// and we've scanned enough blocks, we can stop
			// However, we still need to check for duplicates in all blocks, so we continue
			// but we can optimize by stopping if latestNonce is much higher than submittedNonce
			if foundLatestNonce && latestNonce > submittedNonce+100 {
				// If latest nonce is way ahead, we've likely found all relevant transactions
				// This is a heuristic optimization
				break
			}
		}

		// Move to next batch (going backwards)
		if currentBlock >= batchSize {
			currentBlock = currentBlock - batchSize
		} else {
			break
		}

		// Early exit if we found duplicate and latest nonce
		if hasDuplicate && foundLatestNonce {
			break
		}
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Debug(loggerCtx, "Nonce check completed",
		ion.String("from_address", fromAddr.Hex()),
		ion.Uint64("submitted_nonce", submittedNonce),
		ion.Uint64("latest_nonce", latestNonce),
		ion.Bool("has_duplicate", hasDuplicate),
		ion.Bool("has_any_transactions", foundLatestNonce),
		ion.Uint64("blocks_scanned", blocksScanned),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.CheckNonceAndGetLatest"))

	return hasDuplicate, latestNonce, foundLatestNonce, nil
}
