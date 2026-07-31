package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"gossipnode/config"
	"gossipnode/config/settings"

	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog/log"
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
	Nonce       uint64         `json:"nonce"`   // Unique deterministic ID for Fastsync ART (migrated from old nonce)
	Address     common.Address `json:"address"` // Derived from PublicKey
	Balance     string         `json:"balance,omitempty"`
	TxNonce     uint64         `json:"tx_nonce"`      // Real Ethereum Nonce
	TxCountSent uint64         `json:"tx_count_sent"` // Tracks actual analytical transactions sent

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

	// Create A CreatedAt and UpdatedAt
	CreatedAt := time.Now().UTC().UnixNano()
	UpdatedAt := time.Now().UTC().UnixNano()

	ARTNonce := GenerateARTNonce()

	// Create the account document
	AccountDoc = &Account{
		Nonce:       ARTNonce,
		DIDAddress:  DIDAddress,
		Address:     Address,
		Balance:     "0",
		TxNonce:     0,
		TxCountSent: 0,
		AccountType: "user",
		CreatedAt:   CreatedAt,
		UpdatedAt:   UpdatedAt,
		Metadata:    metadata,
	}

	// Store the account document
	err = storeAccount(PooledConnection, AccountDoc)
	if err != nil {
		return err
	}

	return nil
}

// StoreAccount stores a Key document in the accounts database and creates a DID reference
func storeAccount(PooledConnection *config.PooledConnection, KeyDoc *Account) error {
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
	if PooledConnection.Client == nil {
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
		Nonce:       KeyDoc.Nonce,
		DIDAddress:  KeyDoc.DIDAddress,
		Address:     KeyDoc.Address,
		Balance:     KeyDoc.Balance,
		TxNonce:     KeyDoc.TxNonce,
		TxCountSent: KeyDoc.TxCountSent,
		AccountType: KeyDoc.AccountType,
		CreatedAt:   KeyDoc.CreatedAt,
		UpdatedAt:   time.Now().UTC().UnixNano(),
		Metadata:    KeyDoc.Metadata,
	}

	// Create the account key (e.g., "account:<address>")
	accKey := []byte(fmt.Sprintf("%s%s", Prefix, KeyDoc.Address))

	_, err = PooledConnection.Client.Client.Get(ctx, accKey)
	if err == nil {
		// Account already exists — treat as an idempotent no-op and keep the
		// stored record authoritative rather than overwriting it here.
		return nil
	}
	if err != ErrNotFound && !strings.Contains(err.Error(), "key not found") && !strings.Contains(err.Error(), "tbtree: key not found") {
		// Get failed for a reason other than "key not found" (e.g. timeout,
		// connection drop). Falling through would overwrite an existing funded
		// record with a fresh zero-balance one, so abort instead.
		return fmt.Errorf("storeAccount: pre-check Get failed: %w", err)
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

	// A brand-new address: key was just written (the not-found pre-check above
	// guarantees this path only runs for a genuinely new account). Notify the
	// maintained account/DID counter.
	fireAccountCreated(1)

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

// normalizeUpdatedAtNanos converts an UpdatedAt value of unknown unit
// (seconds, millis, micros, or nanos since epoch) to nanoseconds so LWW
// comparisons are unit-safe. Needed because the live executor stamps
// UpdatedAt with the block timestamp (Unix seconds) while sync paths stamp
// time.Now().UnixNano() — comparing them raw makes any nano-stamped write
// beat every second-stamped write by 9 orders of magnitude.
func normalizeUpdatedAtNanos(ts int64) int64 {
	switch {
	case ts <= 0:
		return ts
	case ts < 1e11: // seconds (valid until year ~5138)
		return ts * int64(time.Second)
	case ts < 1e14: // milliseconds
		return ts * int64(time.Millisecond)
	case ts < 1e17: // microseconds
		return ts * int64(time.Microsecond)
	default: // already nanoseconds
		return ts
	}
}

// mergeAccountForWrite is the single, PURE decision point for writing an
// account object over stored state. It owns LWW ordering, identity-field
// preservation, monotonic counter guards, and new-account defaults — every
// write path through BatchRestoreAccounts (sync accounts payloads, sparse
// balance updates, restores) goes through this function. Unit-tested in
// merge_account_test.go; keep it free of I/O and logging.
//
// existing == nil means no stored account (new account). Returns the merged
// object and whether it should be written (false = existing state wins LWW).
// isZeroBalanceString reports whether a serialized account balance carries no
// value. Every balance in this system is written via big.Int.String(), so the
// only zero encodings are "" (field unset on a sparse update) and "0".
func isZeroBalanceString(bal string) bool {
	return bal == "" || bal == "0"
}

func mergeAccountForWrite(existing *Account, incoming Account) (Account, bool) {
	if existing == nil {
		// NEW ACCOUNT (no stored object to merge from): fill defaults for
		// identity fields that sparse update entries leave zero-valued.
		// DIDAddress stays empty — hex addresses are not DIDs; the real DID
		// arrives later via the accounts payload or DID propagation.
		if incoming.AccountType == "" {
			incoming.AccountType = "user"
		}
		if incoming.CreatedAt == 0 && incoming.UpdatedAt != 0 {
			incoming.CreatedAt = incoming.UpdatedAt
		}
		return incoming, true
	}

	// LWW on unit-normalized timestamps — stored values may be in seconds
	// (live executor: block timestamp) or nanos (sync paths).
	existingTS := normalizeUpdatedAtNanos(existing.UpdatedAt)
	incomingTS := normalizeUpdatedAtNanos(incoming.UpdatedAt)
	if existingTS > incomingTS {
		return incoming, false
	}
	if existingTS == incomingTS && existing.Balance == incoming.Balance {
		// Same timestamp and balance - no change needed
		return incoming, false
	}

	// FIELD MERGING: Prevent partial updates (e.g. from Reconciliation) from wiping out account metadata
	// 1. Preserve DIDAddress if incoming DID is empty or mistakenly set to the
	// hex address. EqualFold: legacy update entries carried the address in
	// lowercase while Address.Hex() is EIP-55 checksummed — a case-sensitive
	// compare never matched, so the hex-address value could overwrite the real DID.
	if incoming.DIDAddress == "" || strings.EqualFold(incoming.DIDAddress, incoming.Address.Hex()) {
		incoming.DIDAddress = existing.DIDAddress
	}
	// 2. Preserve CreatedAt
	if incoming.CreatedAt == 0 {
		incoming.CreatedAt = existing.CreatedAt
	}
	// 3. Preserve AccountType. Empty = balance update carries no identity;
	// "user" = legacy hardcoded placeholder from old update entries.
	if (incoming.AccountType == "" || incoming.AccountType == "user") && existing.AccountType != "" {
		incoming.AccountType = existing.AccountType
	}
	// 4. Preserve Metadata
	if incoming.Metadata == nil {
		incoming.Metadata = existing.Metadata
	}
	// 5. Preserve ART identity nonce: 0 means the producer had no value
	// (e.g. reconciliation of a receiver-only account). Never zero it.
	if incoming.Nonce == 0 {
		incoming.Nonce = existing.Nonce
	}
	// 6. Monotonic guard on tx counters: the Ethereum nonce and sent-tx
	// count never decrease. A lower incoming value means the producer had
	// partial information (receiver-only recon delta) — keep the existing.
	if incoming.TxNonce < existing.TxNonce {
		incoming.TxNonce = existing.TxNonce
	}
	if incoming.TxCountSent < existing.TxCountSent {
		incoming.TxCountSent = existing.TxCountSent
	}
	// 7. Preserve Balance on a placeholder/sync write. The authoritative balance
	// writers — live execution (ApplyTxAtomic) and reconciliation
	// (ApplyBlockRecon) — commit directly under the state-apply lock and never
	// reach this merge. The writes that DO reach it are account-sync, restore,
	// and DID propagation, which carry Balance "0" as a placeholder that
	// reconciliation is expected to fill. Letting that "0" win LWW would
	// overwrite a real balance with zero — a silent, non-healing divergence.
	// Treat an incoming zero/empty balance as "no balance information" and keep
	// the stored value, exactly like the sparse-field preserves above. A real
	// (nonzero) incoming balance is still applied, so legitimate balance updates
	// that route through this path are unaffected.
	if isZeroBalanceString(incoming.Balance) && !isZeroBalanceString(existing.Balance) {
		incoming.Balance = existing.Balance
	}

	return incoming, true
}

// BatchRestoreAccounts applies a batch of entries into accountsdb.
// For address:<addr> keys it writes KV. For did:<did> it creates a bound reference to the corresponding address key.
func BatchRestoreAccounts(ctx context.Context, PooledConnection *config.PooledConnection, entries []struct {
	Key   string
	Value []byte
}) error {
	if len(entries) == 0 {
		return fmt.Errorf("entries cannot be empty")
	}
	var err error
	var shouldReturnConnection bool

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

	// Deduplicate address entries via hash set: the sender may include the same key
	// multiple times in one page. The LWW check reads the committed DB value (not the
	// in-progress ops slice), so both copies would independently pass and produce a
	// duplicate key in ExecAll. Build a key→entry map keeping the highest UpdatedAt,
	// then flatten back to slice.
	{
		type entry = struct {
			Key   string
			Value []byte
		}
		addrSet := make(map[string]entry, len(addressEntries))
		for _, e := range addressEntries {
			cur, ok := addrSet[e.Key]
			if !ok {
				addrSet[e.Key] = e
				continue
			}
			var curAcc, inAcc Account
			if json.Unmarshal(cur.Value, &curAcc) == nil &&
				json.Unmarshal(e.Value, &inAcc) == nil &&
				normalizeUpdatedAtNanos(inAcc.UpdatedAt) > normalizeUpdatedAtNanos(curAcc.UpdatedAt) {
				addrSet[e.Key] = e
			}
		}
		addressEntries = make([]entry, 0, len(addrSet))
		for _, e := range addrSet {
			addressEntries = append(addressEntries, e)
		}
	}

	// Deduplicate DID entries via hash set: refs are idempotent, last occurrence wins.
	{
		type entry = struct {
			Key   string
			Value []byte
		}
		didSet := make(map[string]entry, len(didEntries))
		for _, e := range didEntries {
			didSet[e.Key] = e
		}
		didEntries = make([]entry, 0, len(didSet))
		for _, e := range didSet {
			didEntries = append(didEntries, e)
		}
	}

	// Pre-fetch all existing account values in one GetAll RPC instead of N individual Gets
	// during the LWW loop. Holding a connection across 3000+ sequential Gets exhausts the
	// pool (max 20) when multiple dispatchWorkers run concurrently.
	existingAccounts := make(map[string]Account, len(addressEntries))
	{
		prefetchSet := make(map[string]struct{}, len(addressEntries)+len(didEntries))
		prefetchKeys := make([][]byte, 0, len(addressEntries)+len(didEntries))
		for _, e := range addressEntries {
			if _, ok := prefetchSet[e.Key]; !ok {
				prefetchSet[e.Key] = struct{}{}
				prefetchKeys = append(prefetchKeys, []byte(e.Key))
			}
		}
		for _, e := range didEntries {
			var acc Account
			if json.Unmarshal(e.Value, &acc) == nil {
				k := fmt.Sprintf("%s%s", Prefix, acc.Address)
				if _, ok := prefetchSet[k]; !ok {
					prefetchSet[k] = struct{}{}
					prefetchKeys = append(prefetchKeys, []byte(k))
				}
			}
		}
		if len(prefetchKeys) > 0 {
			fetchCtx, fetchCancel := context.WithTimeout(ctx, 30*time.Second)
			entriesList, getAllErr := PooledConnection.Client.Client.GetAll(fetchCtx, prefetchKeys)
			fetchCancel()
			// GetAll failure MUST fail the batch. Treating it as "all accounts are
			// new" (the old behaviour) skipped both the LWW check and the identity
			// merge: sparse update entries (empty DIDAddress/AccountType/CreatedAt)
			// would be written raw, clobbering real account objects. Callers retry —
			// the drain worker leaves entries unACKed in the Redis PEL.
			// Note: immudb GetAll silently skips missing keys (database.go: ErrKeyNotFound
			// is tolerated per key), so an all-new-accounts batch returns an empty list
			// with a nil error — this path only fires on real RPC/DB failures.
			if getAllErr != nil {
				return fmt.Errorf("prefetch existing accounts (GetAll %d keys): %w - BatchRestoreAccounts", len(prefetchKeys), getAllErr)
			}
			if entriesList != nil {
				for _, entry := range entriesList.Entries {
					if entry == nil || entry.Value == nil {
						continue
					}
					var acc Account
					if json.Unmarshal(entry.Value, &acc) == nil {
						existingAccounts[string(entry.Key)] = acc
					}
				}
			}
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

	// Count brand-new address: keys written in this batch (existing == nil), to
	// advance the maintained account/DID counter after the batch commits.
	newAccounts := 0

	// Process address: keys first (with LWW logic)
	for _, e := range addressEntries {
		var shouldWrite = true
		var incoming Account
		if err := json.Unmarshal(e.Value, &incoming); err == nil {
			var existing *Account
			if ex, found := existingAccounts[e.Key]; found {
				existing = &ex
			}

			merged, write := mergeAccountForWrite(existing, incoming)
			shouldWrite = write
			if existing == nil && write {
				// New account (no stored object) that will be written.
				newAccounts++
			}
			if !write {
				delete(addressKeysInBatch, e.Key)
			} else {
				if existing != nil && normalizeUpdatedAtNanos(existing.UpdatedAt) < normalizeUpdatedAtNanos(merged.UpdatedAt) {
					loggerCtx, cancel := context.WithCancel(context.Background())
					defer cancel()
					PooledConnection.Client.Logger.Debug(loggerCtx, "Updating account - incoming is newer (LWW)",
						ion.String("key", e.Key),
						ion.Int64("existing_updated_at", existing.UpdatedAt),
						ion.Int64("incoming_updated_at", merged.UpdatedAt),
						ion.String("existing_balance", existing.Balance),
						ion.String("incoming_balance", merged.Balance),
						ion.String("database", config.AccountsDBName),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("log_file", LOG_FILE),
						ion.String("topic", TOPIC),
						ion.String("function", "DB_OPs.BatchRestoreAccounts"))
				}
				// Re-serialize the merged account object to overwrite e.Value
				if mergedVal, err := json.Marshal(merged); err == nil {
					e.Value = mergedVal
				}
			}
		} else {
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

		if !addressKeysInBatch[addrKey] {
			if _, found := existingAccounts[addrKey]; found {
				ops = append(ops, &schema.Op{Operation: &schema.Op_Ref{Ref: &schema.ReferenceRequest{
					Key:           []byte(e.Key),
					ReferencedKey: []byte(addrKey),
					AtTx:          0,
					BoundRef:      true,
				}}})
			}
			// addrKey not in existingAccounts → doesn't exist in DB → skip orphaned ref
		}
		// addressKeysInBatch[addrKey] == true → DID ref already appended in Pass 1
	}

	if len(ops) == 0 {
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

	// Chunk ops to stay within ImmuDB's MaxTxEntries limit (default 1024).
	// Each chunk is its own atomic transaction; LWW semantics make this safe.
	const immudbMaxOpsPerTx = 1000
	for chunkStart := 0; chunkStart < len(ops); chunkStart += immudbMaxOpsPerTx {
		end := chunkStart + immudbMaxOpsPerTx
		if end > len(ops) {
			end = len(ops)
		}
		chunkCtx, chunkCancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = PooledConnection.Client.Client.ExecAll(chunkCtx, &schema.ExecAllRequest{Operations: ops[chunkStart:end]})
		chunkCancel()
		if err != nil {
			loggerCtx2, cancel2 := context.WithCancel(context.Background())
			defer cancel2()
			PooledConnection.Client.Logger.Error(loggerCtx2, "Batch restore ExecAll failed",
				err,
				ion.Int("operations_count", end-chunkStart),
				ion.Int("chunk_start", chunkStart),
				ion.Int("total_ops", len(ops)),
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.BatchRestoreAccounts"))
			return fmt.Errorf("accounts batch restore failed: %w", err)
		}
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

	// All chunks committed: advance the maintained account/DID counter by the
	// number of brand-new accounts in this batch (sync/catchup creation path).
	fireAccountCreated(newAccounts)

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

// UpdateAccount is the central method to write a modified Account object to the database.
// It handles connection pooling, ensures the Accounts database is selected, and performs a SafeCreate.
// The caller is expected to fetch the account via GetAccount, modify it, and pass it here.
func UpdateAccount(PooledConnection *config.PooledConnection, doc *Account) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturnConnection = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("failed to get connection from pool: %w - UpdateAccount", err)
		}
		shouldReturnConnection = true
	}

	if shouldReturnConnection {
		defer func() {
			PutAccountsConnection(PooledConnection)
		}()
	}

	if err := ensureAccountsDBSelected(PooledConnection); err != nil {
		return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
	}

	if doc == nil || doc.Address == (common.Address{}) {
		return fmt.Errorf("invalid account document provided to UpdateAccount")
	}

	key := fmt.Sprintf("%s%s", Prefix, doc.Address)
	if err = SafeCreate(PooledConnection.Client, key, doc); err != nil {
		loggerCtx, logCancel := context.WithCancel(context.Background())
		defer logCancel()
		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to update account",
			err,
			ion.String("account", doc.Address.String()),
			ion.String("database", config.AccountsDBName),
			ion.String("function", "DB_OPs.UpdateAccount"))
		return err
	}
	return nil
}

// UpdateAccountBalance updates only the balance for an account.
// Used widely in test suites (account_immuclient_test.go, security_cache_test.go).
// updatedAt must be set by the caller to block.Timestamp (in nanoseconds) to ensure
// deterministic UpdatedAt values that are identical across all network nodes processing
// the same block. Never pass time.Now() here.
func UpdateAccountBalance(PooledConnection *config.PooledConnection, address common.Address, newBalance string, updatedAt int64) error {
	doc, err := GetAccount(PooledConnection, address)
	if err != nil {
		return err
	}

	doc.Balance = newBalance
	doc.UpdatedAt = updatedAt

	return UpdateAccount(PooledConnection, doc)
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
		scanResult, err := ic.Client.Scan(ReadCtx, scanReq)
		ReadCancel()
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
					PooledConnection.Client.Logger.Warn(loggerCtx, "Skipping account due to unmarshal error",
						ion.String("error", err.Error()),
						ion.String("key", string(entry.Key)),
						ion.String("database", config.AccountsDBName),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("log_file", LOG_FILE),
						ion.String("topic", TOPIC),
						ion.String("function", "DB_OPs.ListAccountsPaginated"))
					cancel()
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

// ListAccountsPaginatedFrom retrieves up to limit accounts starting after seekKey in ascending key order.
// seekKey=nil starts from the first address: entry. Returns the accounts and the scan cursor
// (key of the last accepted account); pass it as seekKey on the next call to continue without rescanning.
//
// Time: O(limit) ImmuDB entries read; Space: O(limit)
// DS: ImmuDB ascending Scan with SeekKey cursor — no offset restart across calls.
func ListAccountsPaginatedFrom(PooledConnection *config.PooledConnection, limit int, seekKey []byte, extendedPrefix string) ([]*Account, []byte, error) {
	var err error
	var shouldReturnConnection = false

	ctx := context.Background()

	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get connection from pool: %w - ListAccountsPaginatedFrom", err)
		}
		shouldReturnConnection = true
	}
	if shouldReturnConnection {
		defer func() {
			PutAccountsConnection(PooledConnection)
		}()
	}

	ic := PooledConnection.Client
	if err := ensureAccountsDBSelected(PooledConnection); err != nil {
		return nil, nil, fmt.Errorf("failed to ensure accounts database is selected: %w - ListAccountsPaginatedFrom", err)
	}

	prefix := []byte(Prefix)
	var accounts []*Account
	var lastKey []byte
	const internalBatch = 1000
	currentSeek := seekKey

	for len(accounts) < limit {
		scanReq := &schema.ScanRequest{
			Prefix:  prefix,
			Limit:   uint64(internalBatch),
			SeekKey: currentSeek,
			Desc:    false,
		}

		scanCtx, scanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		scanResult, scanErr := ic.Client.Scan(scanCtx, scanReq)
		scanCancel()

		if scanErr != nil {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ic.Logger.Error(loggerCtx, "Failed to scan for accounts",
				scanErr,
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.ListAccountsPaginatedFrom"))
			return nil, nil, fmt.Errorf("failed to scan for accounts: %w - ListAccountsPaginatedFrom", scanErr)
		}

		if len(scanResult.Entries) == 0 {
			break
		}

		// ImmuDB Scan is inclusive on SeekKey — skip the first entry if it is the cursor itself.
		startIndex := 0
		if currentSeek != nil && string(scanResult.Entries[0].Key) == string(currentSeek) {
			startIndex = 1
		}

		for i := startIndex; i < len(scanResult.Entries) && len(accounts) < limit; i++ {
			entry := scanResult.Entries[i]

			var acc Account
			if jsonErr := json.Unmarshal(entry.Value, &acc); jsonErr != nil {
				loggerCtx, cancel := context.WithCancel(context.Background())
				ic.Logger.Warn(loggerCtx, "Skipping account due to unmarshal error",
					ion.String("error", jsonErr.Error()),
					ion.String("key", string(entry.Key)),
					ion.String("database", config.AccountsDBName),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.ListAccountsPaginatedFrom"))
				cancel()
				continue
			}

			if extendedPrefix != "" && !strings.HasPrefix(acc.DIDAddress, extendedPrefix) {
				continue
			}

			accounts = append(accounts, &acc)
			lastKey = entry.Key
		}

		if len(accounts) >= limit || len(scanResult.Entries) < internalBatch {
			break
		}

		// Advance cursor to the end of this scan batch.
		currentSeek = scanResult.Entries[len(scanResult.Entries)-1].Key
	}

	return accounts, lastKey, nil
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

// CountAccountsWithTimeout is CountAccounts with a caller-chosen deadline for the
// underlying immudb Count. The one-time explorer-stats seed uses this: it runs
// off the request path and can allow minutes on a large accounts DB instead of
// failing at the default 30s.
func CountAccountsWithTimeout(countTimeout time.Duration) (int, error) {
	return CountBuilder{}.GetAccountsDBCountWithTimeout(Prefix, countTimeout)
}

// GetTransactionsByDID retrieves all transactions associated with a given DID
// This implementation iterates through all blocks to find matching transactions,
// which is more efficient than fetching each transaction individually.
// GetTransactionsByAccount retrieves all transactions associated with a given account address
// This implementation uses the MAIN database connection pool (not accounts) since transactions are stored in main DB
func GetTransactionsByAccount(PooledConnection *config.PooledConnection, accountAddr *common.Address) ([]*config.Transaction, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout.
	// The scan reads every block from 0..latestBlock via batch GetAll calls (~24 batches
	// for 11605 blocks). 120s gives ample headroom even under ImmuDB load.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	latestBlockNumber, err := GetLatestBlockNumber(ctx, PooledConnection)
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
	// Use large batches so GetAll makes ~24 round-trips for 11605 blocks instead
	// of 11605 individual reads. This cuts scan time from minutes to seconds.
	const batchSize = uint64(500)

	for startBlock := uint64(0); startBlock <= latestBlockNumber; startBlock += batchSize {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		endBlock := startBlock + batchSize - 1
		if endBlock > latestBlockNumber {
			endBlock = latestBlockNumber
		}

		blocks, err := GetBlocksRange(PooledConnection, startBlock, endBlock)
		if err != nil {
			loggerCtx, cancel := context.WithCancel(context.Background())
			ic.Logger.Warn(loggerCtx, "Error retrieving block batch, skipping",
				ion.String("error", err.Error()),
				ion.Uint64("start_block", startBlock),
				ion.Uint64("end_block", endBlock),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetTransactionsByAccount"))
			cancel()
			continue
		}

		for _, block := range blocks {
			for _, tx := range block.Transactions {
				if isTransactionInvolvingAccount(tx, accountAddr) {
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

// GetTransactionsByAccountInRange retrieves transactions for an account in [fromBlock, toBlock].
// Pass math.MaxUint64 for toBlock to scan up to the latest block in the DB.
// Identical to GetTransactionsByAccount but scans a bounded block range instead of 0..latest,
// enabling delta-only reconciliation so each sync run replays only new transactions.
func GetTransactionsByAccountInRange(PooledConnection *config.PooledConnection, accountAddr *common.Address, fromBlock, toBlock uint64) ([]*config.Transaction, error) {
	var err error
	var shouldReturnConnection = false

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection from pool: %w - GetTransactionsByAccountInRange", err)
		}
		shouldReturnConnection = true
	}
	if shouldReturnConnection {
		defer PutMainDBConnection(PooledConnection)
	}

	latestBlockNumber, err := GetLatestBlockNumber(ctx, PooledConnection)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest block number: %w", err)
	}

	if toBlock > latestBlockNumber {
		toBlock = latestBlockNumber
	}
	if fromBlock > toBlock {
		// Nothing to scan — no new blocks in range
		return nil, nil
	}

	var matchingTxs []*config.Transaction
	const batchSize = uint64(500)

	for startBlock := fromBlock; startBlock <= toBlock; startBlock += batchSize {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		endBlock := startBlock + batchSize - 1
		if endBlock > toBlock {
			endBlock = toBlock
		}

		blocks, err := GetBlocksRange(PooledConnection, startBlock, endBlock)
		if err != nil {
			PooledConnection.Client.Logger.Warn(ctx, "Error retrieving block batch, skipping",
				ion.String("error", err.Error()),
				ion.Uint64("start_block", startBlock),
				ion.Uint64("end_block", endBlock),
				ion.String("function", "DB_OPs.GetTransactionsByAccountInRange"))
			continue
		}

		for _, block := range blocks {
			for _, tx := range block.Transactions {
				if isTransactionInvolvingAccount(tx, accountAddr) {
					txCopy := tx
					matchingTxs = append(matchingTxs, &txCopy)
				}
			}
		}
	}

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

// GetLatestNonce retrieves the current TxNonce for an account from accountsdb.
// TxNonce is the authoritative Ethereum nonce — maintained by block processing as
// account.TxNonce = tx.Nonce + 1. Reading it here keeps nonce checks consistent
// with Security.go and Processing.go which both read from accountsdb.
//
// The previous implementation scanned transaction history in the main DB which
// was slower and could diverge from the account record (different DB source).
func GetLatestNonce(PooledConnection *config.PooledConnection, fromAddr *common.Address) (uint64, error) {
	if fromAddr == nil {
		return 0, fmt.Errorf("GetLatestNonce: fromAddr is nil")
	}

	account, err := GetAccount(PooledConnection, *fromAddr)
	if err != nil {
		if err == ErrNotFound {
			// Account doesn't exist yet — first transaction will use nonce 0.
			return 0, nil
		}
		return 0, fmt.Errorf("GetLatestNonce: failed to get account %s: %w", fromAddr.Hex(), err)
	}

	return account.TxNonce, nil
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
	latestBlockNumber, err := GetLatestBlockNumber(ctx, PooledConnection)
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
		if ctx.Err() != nil {
			return false, 0, false, ctx.Err()
		}
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
			if ctx.Err() != nil {
				return false, 0, false, ctx.Err()
			}
			i--
			block, err := ReadZKBlockByNumber(ctx, PooledConnection, i)
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

// [AUDIT OK]: Connection lifecycle, determinism via addr bytes, and Immudb writes verified safe across 1 call site in BlockProcessing.
// [AUDIT OK]: Read-modify-write pattern verified safe; GetAccount validates existence; 3 call sites in BlockProcessing.
// [AUDIT OK]: State transition logic (TxCountSent++, Nonce update) and blockTimestamp propagation verified safe; 1 call site in BlockProcessing.
// [AUDIT OK]: Nil checks on account/address, connection pooling handling, and direct storage verified safe; 1 call site in DIDPropagation.
// NormalizePropagatedAccountState resets the volatile ledger fields of an
// account received via DID propagation to their canonical initial values.
// Balance, TxNonce, and TxCountSent are owned by block processing and
// reconciliation, so an identity-propagation event always initializes them to
// zero. This is the single source of truth for that policy, shared by the store
// path (StorePropagatedAccount) and the forward path (HandleDIDStream) so both
// the stored and the re-broadcast copy stay consistent.
//
// Left untouched: the ART identity Nonce (preserved for Fastsync ART routing)
// and CreatedAt/UpdatedAt (timestamp policy is owned by the caller — the store
// path stamps them locally; the forward path keeps them so downstream LWW
// ordering is not affected). Pure and unit-tested.
//
// Returns true if any reset field carried a non-canonical value on input, so
// callers can record it for observability.
func NormalizePropagatedAccountState(acc *Account) bool {
	if acc == nil {
		return false
	}
	adjusted := (acc.Balance != "" && acc.Balance != "0") ||
		acc.TxNonce != 0 ||
		acc.TxCountSent != 0
	acc.Balance = "0"
	acc.TxNonce = 0
	acc.TxCountSent = 0
	return adjusted
}

// StorePropagatedAccount securely stores an account received from the P2P network,
// perfectly preserving its ART Nonce and other properties to ensure Fastsync consensus.
func StorePropagatedAccount(PooledConnection *config.PooledConnection, account *Account) error {
	var err error
	var shouldReturnConnection = false

	if account == nil || account.Address == (common.Address{}) {
		return fmt.Errorf("propagated account is invalid")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("failed to get accounts connection: %w - StorePropagatedAccount", err)
		}
		shouldReturnConnection = true
	}

	if shouldReturnConnection {
		defer PutAccountsConnection(PooledConnection)
	}

	// UPDATE-ONLY unless local creation is explicitly enabled: DID propagation
	// reaches only the peers that happen to receive the message, so letting it
	// CREATE accounts brings the account into existence on SOME nodes with
	// whatever ART nonce the sender minted — the fleet-divergence vector removed
	// by block-carried identities. Metadata/DID updates for accounts this node
	// already holds remain allowed; creation of unknown accounts is refused.
	if !AllowLocalAccountCreate {
		if _, err := GetAccount(PooledConnection, account.Address); err != nil {
			if strings.Contains(err.Error(), "key not found") {
				return fmt.Errorf("refusing to create account %s via DID propagation: %w",
					account.Address.Hex(), ErrLocalAccountCreateDisabled)
			}
			return fmt.Errorf("store propagated account: existence check for %s: %w",
				account.Address.Hex(), err)
		}
	}

	// Initialize volatile ledger fields (balance, tx counters) to their
	// canonical values via the shared policy. A true return means the incoming
	// copy carried non-canonical values; log it for observability.
	if NormalizePropagatedAccountState(account) {
		log.Debug().
			Str("address", account.Address.Hex()).
			Str("did", account.DIDAddress).
			Msg("Normalized propagated account ledger fields before store")
	}
	// Timestamps are stamped locally on receipt (identity-creation event).
	now := time.Now().UTC().UnixNano()
	account.CreatedAt = now
	account.UpdatedAt = now

	return storeAccount(PooledConnection, account)
}

var artNonceCounter uint64

// [AUDIT OK]: Atomic counter and bit shift mathematically proven safe against overflow (51 bits for micro + 12 for counter = 63 bits); 1 call site in CreateAccount.
// GenerateARTNonce generates a locally unique Nonce for Fastsync ART routing.
// This is strictly used when this node originates an account (e.g., manual DID creation).
// Accounts synced from the network MUST preserve the sender's ART Nonce.
func GenerateARTNonce() uint64 {
	ts := uint64(time.Now().UTC().UnixMicro())
	c := atomic.AddUint64(&artNonceCounter, 1)
	return (ts << 12) | (c & 0xFFF)
}
