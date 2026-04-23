DB\_OPs Function Reference  —  JMDT Node

**DB\_OPs Package**

Function Reference for Junior Developers

JMDT Decentralised Node  —  Database Operations Layer

Version: v1.1.0   |   Branch: fix/uint64-underflow-checknonce


# **Contents**





# **1.  How to Use This Document**
This reference covers every function in the DB\_OPs package — the layer that handles all reading and writing to ImmuDB (the tamper-proof database that stores blocks, transactions, accounts, and DID records). It is written for junior developers who need to understand what the code does before making changes during the database upgrade.

**How to read the function tables:**

- Function — the exact Go function name. Click it in your editor to jump to the source.
- Signature — the parameter types and return types in Go syntax.
- What it does — a plain-English description of the function's job.
- Key parameters — what each important argument means.
- Returns — what you get back on success.
- Errors — what can go wrong and when.

|**Tip:**  If a function name starts with a lowercase letter (e.g. storeAccount) it is unexported — only code inside DB\_OPs can call it. Uppercase names (e.g. GetAccount) are the public API that other packages use.|
| :- |


# **2.  Key Concepts Before You Start**
## **2.1  Two Databases**
DB\_OPs talks to two separate ImmuDB databases:

- Main DB — stores blocks, transactions, receipts, and logs. Think of it as the chain ledger.
- Accounts DB — stores wallet accounts and DID (Decentralised Identity) documents. Think of it as the address book.

Each database has its own connection pool. You must use the right pool for the right database or queries will fail silently.

## **2.2  Connection Pools**
Opening a database connection is slow. To avoid doing it for every query, DB\_OPs keeps a pool of ready-to-use connections.

- Always get a connection before querying: call GetMainDBConnection() or GetAccountsConnections().
- Always return it when done: call PutMainDBConnection() or PutAccountsConnection().
- The convenience helpers GetMainDBConnectionandPutBack() and GetAccountConnectionandPutBack() do both automatically when the context expires — prefer these.

|**Warning:**  If you get a connection and forget to return it, the pool runs dry and the whole node hangs waiting for a free connection.|
| :- |

## **2.3  Verified vs Unverified Reads**
ImmuDB supports two kinds of reads:

- **Get** — fast, no cryptographic proof. Use for hot-path lookups where speed matters.
- **VerifiedGet** — slower, proves the value has not been tampered with using a Merkle proof. Use for anything that could be a security boundary (blocks, receipts).

Functions starting with Safe (e.g. SafeRead, SafeCreate) always use the verified path.

## **2.4  Key Naming Scheme**
All data in ImmuDB is stored as key→value pairs. The key format tells you what kind of data it is:

|**Constant / Error**|**Value**|**Purpose**|
| :- | :- | :- |
|block:{number}|e.g. block:1042|A full ZKBlock stored by block number|
|block:hash:{hash}|e.g. block:hash:0xabc|Maps a block hash to its block number|
|tx:{hash}|e.g. tx:0xdef|A single transaction stored by hash|
|receipt:{hash}|e.g. receipt:0xdef|Transaction receipt stored by tx hash|
|address:{addr}|e.g. address:0x123|A wallet account stored by address|
|did:{did}|e.g. did:jmdt:0x123|A DID document or reference to an address key|
|latest\_block|(literal)|Stores the current highest block number|

## **2.5  Error Sentinel Values**
DB\_OPs defines typed errors you should check for explicitly rather than comparing error strings:

|**Constant / Error**|**Value**|**Purpose**|
| :- | :- | :- |
|ErrEmptyKey|(custom)|You passed an empty string as a key|
|ErrEmptyBatch|(custom)|You passed an empty map/slice to a batch function|
|ErrNilValue|(custom)|You passed a nil value where a value was expected|
|ErrNotFound|(custom)|The key does not exist in the database|
|ErrConnectionLost|(custom)|The database connection dropped|
|ErrPoolClosed|(custom)|The connection pool has been shut down|
|ErrTokenExpired|(custom)|The ImmuDB auth token expired and must be refreshed|
|ErrNoAvailableConn|(custom)|All pool connections are in use — try again shortly|


# **3.  Function Reference by File**
## **3.1  MainDB\_Connections.go — Main Database Pool**
These functions manage the connection pool for the **main database** (blocks, transactions, receipts). You will use GetMainDBConnection and PutMainDBConnection most often.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**InitMainDBPool**|poolConfig \*ConnectionPoolConfig → error|One-time setup call at node startup. Creates the pool and connects to the main database using default credentials.|poolConfig — pool size, timeouts etc.|error if setup fails|Connection refused, bad credentials|
|**InitMainDBPoolWithLoki**|poolConfig, enableLoki bool, username, password string → error|Same as InitMainDBPool but also wires up Loki log shipping if enableLoki is true.|enableLoki — turn Loki on/off; username/password — admin creds|error if setup fails|Auth failures, Loki config errors|
|**GetMainDBConnection**|ctx context.Context → \*PooledConnection, error|Borrows a connection from the pool. You MUST return it with PutMainDBConnection when finished.|ctx — context (reserved for future timeout use)|\*PooledConnection|Pool not yet initialised|
|**PutMainDBConnection**|conn \*PooledConnection|Returns a borrowed connection back to the pool so other callers can use it.|conn — the connection you borrowed|nothing|Logs a warning if conn is nil|
|**GetMainDBConnectionandPutBack**|ctx context.Context → \*PooledConnection, error|Gets a connection AND automatically returns it when ctx is cancelled or times out. Preferred over manual get/put pairs.|ctx — use context.WithTimeout to control when it is returned|\*PooledConnection|Context already cancelled, pool errors|
|**CloseMainDBPool**|→ nothing|Shuts down the entire pool. Call this only on node shutdown.|none|nothing|Logs issues during cleanup|
|**ensureMainDBSelected**|conn \*PooledConnection → error|Internal: verifies the correct database is selected on this connection and refreshes the auth token if needed.|conn — connection to check|error if refresh fails|Token expired, connection lost|
|**connectToMainDB**|username, password string → error|Internal: one-time database creation if the database does not yet exist. Runs at startup before the pool is built.|username/password — admin credentials|error if creation fails|Wrong credentials, database already exists|

## **3.2  Account\_Connections.go — Accounts Database Pool**
Same pool pattern as MainDB\_Connections.go but for the **accounts database** (wallets and DIDs).

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**InitAccountsPool**|→ error|One-time setup at startup. Creates the accounts database if it does not exist, then opens the pool.|none|error if setup fails|Connection refused, database creation errors|
|**GetAccountsConnections**|ctx context.Context → \*PooledConnection, error|Borrows a connection from the accounts pool. Must be returned with PutAccountsConnection.|ctx — context|\*PooledConnection|Pool not initialised|
|**PutAccountsConnection**|conn \*PooledConnection|Returns the connection to the accounts pool.|conn — the connection you borrowed|nothing|Logs warning if nil|
|**GetAccountConnectionandPutBack**|ctx context.Context → \*PooledConnection, error|Gets an accounts connection and auto-returns it when ctx expires. Preferred pattern.|ctx — use context.WithTimeout|\*PooledConnection|Context cancelled, pool errors|
|**EnsureDBConnection**|accountsPool \*PooledConnection → error|Pings the database and retries up to 3 times with a 2-second gap. Useful before long-running operations.|accountsPool — connection to verify|error after 3 failures|All retries fail|
|**ensureAccountsDBExists**|username, password string → error|Internal: creates the accounts database if missing. Runs once at startup.|username/password — admin credentials|error if creation fails|Wrong credentials|

## **3.3  immuclient.go — Core Read / Write Operations**
This is the heart of DB\_OPs. It wraps ImmuDB's low-level client with consistent error handling, retries, and logging. Most other files call functions from here.

### **Basic CRUD**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**Create**|conn \*PooledConnection, key string, value interface{} → error|Stores a key-value pair. Accepts any Go value — structs are JSON-encoded automatically.|key — must not be empty; value — any type, will be encoded|error on failure|ErrEmptyKey, ErrNilValue, connection lost|
|**Read**|conn \*PooledConnection, key string → []byte, error|Retrieves the raw bytes for a key.|key — must not be empty|raw bytes or ErrNotFound|ErrEmptyKey, key not in database|
|**ReadJSON**|key string, dest interface{} → error|Reads a key and unmarshals the JSON value into dest. Pass a pointer as dest.|dest — pointer to struct to fill|error if read or parse fails|Key missing, invalid JSON|
|**Update**|key string, value interface{} → error|Alias for Create. Overwrites an existing key with a new value.|Same as Create|Same as Create|Same as Create|
|**Exists**|conn \*PooledConnection, key string → bool, error|Returns true if the key exists in the database.|key — the key to check|bool (true = found)|Connection errors|
|**BatchCreate**|conn \*PooledConnection, entries map[string]interface{} → error|Writes many key-value pairs in a single atomic database call. Faster than calling Create in a loop.|entries — map of key→value pairs, must not be empty|error if any entry fails|ErrEmptyBatch, ExecAll failures|
|**BatchCreateOrdered**|conn \*PooledConnection, entries []struct{Key,Value} → error|Same as BatchCreate but preserves insertion order (uses a slice instead of a map).|entries — ordered slice of key-value structs|error if any entry fails|ExecAll failures|

### **Verified (Safe) Read / Write**
These use ImmuDB's cryptographic proof system to guarantee data has not been tampered with. Slower but more secure.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**SafeCreate**|ic \*ImmuClient, key string, value interface{} → error|Writes with VerifiedSet — ImmuDB generates a cryptographic proof that the value was stored correctly.|ic — raw client (not pooled conn); key, value — as per Create|error if write fails|VerifiedSet proof rejection|
|**SafeRead**|ic \*ImmuClient, key string → []byte, error|Reads with VerifiedGet — verifies the value against a Merkle proof. Use for blocks and receipts.|ic — raw client; key — key to read|verified bytes|Proof mismatch, key not found|
|**SafeReadJSON**|ic \*ImmuClient, key string, dest interface{} → error|SafeRead + JSON unmarshal in one call.|dest — pointer to struct|error if read or parse fails|Proof failure, invalid JSON|

### **Key Scanning**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetKeys**|conn \*PooledConnection, prefix string, limit int → []string, error|Returns up to limit keys that start with prefix. Fast for small result sets.|prefix — key prefix to match; limit — max results (0 = default)|[]string of matching keys|Scan API failures|
|**GetAllKeys**|conn \*PooledConnection, prefix string → []string, error|Returns ALL keys with prefix, regardless of count. Paginates internally in batches of 1000. Capped at 60min total.|prefix — key prefix|[]string (can be large)|Timeout, reconnection failures|

### **Block Operations**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**StoreZKBlock**|conn \*PooledConnection, block \*ZKBlock → error|Saves a full ZKBlock to the database under block:{number}.|block — the block to save|error on failure|JSON encode failure, Create errors|
|**GetZKBlockByNumber**|conn \*PooledConnection, blockNumber uint64 → \*ZKBlock, error|Loads a block by its number. Uses VerifiedGet for tamper-proof retrieval.|blockNumber — the block height|\*ZKBlock or error|Block does not exist, proof failure|
|**GetZKBlockByHash**|conn \*PooledConnection, blockHash string → \*ZKBlock, error|Loads a block by its hash. Looks up the block number from the hash index first, then fetches the block.|blockHash — 0x-prefixed hash|\*ZKBlock or error|Hash not indexed, block missing|
|**GetLatestBlockNumber**|conn \*PooledConnection → uint64, error|Returns the highest block number currently stored.|none|uint64 block number|latest\_block key missing|
|**GetAllBlocks**|conn \*PooledConnection → []\*ZKBlock, error|Loads every block ever stored. WARNING: on a long-running chain this can be a large dataset.|none|[]\*ZKBlock|GetAllKeys or unmarshal failures|
|**GetBlocksRange**|conn \*PooledConnection, start, end uint64 → []\*ZKBlock, error|Efficiently bulk-fetches a range of blocks using ImmuDB GetAll API (one round trip).|start — first block number; end — last block number (inclusive)|[]\*ZKBlock|start > end, unmarshal failures|

### **Transaction Operations**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetTransactionByHash**|conn \*PooledConnection, txHash string → \*Transaction, error|Fetches one transaction by its hash. Normalises hash to 0x prefix automatically.|txHash — with or without 0x prefix|\*Transaction or error|tx not in database|
|**GetTransactionsBatch**|conn \*PooledConnection, hashes []string → []\*Transaction, error|Fetches multiple transactions by their hashes. Calls GetTransactionByHash in a loop.|hashes — list of tx hashes|[]\*Transaction|Any single hash missing causes error|
|**GetTransactionBlock**|conn \*PooledConnection, txHash string → \*ZKBlock, error|Finds which block contains a given transaction. Scans all tx keys to locate the correct block.|txHash — the tx to locate|\*ZKBlock containing the tx|tx hash not found|
|**GetTransactionsByAccount**|conn \*PooledConnection, addr \*common.Address → []\*Transaction, error|Returns all transactions where the address is sender or recipient. Full scan — slow on large chains.|addr — the wallet address to search for|[]\*Transaction|Scan or lookup failures|
|**CountTransactions**|conn \*PooledConnection → int, error|Counts total transactions in the database using ImmuDB's native Count API.|none|int count|Count API failures|
|**CountTransactionsByAccount**|addr \*common.Address → int64, error|Counts how many transactions an address has made. Loads all txs then counts — not efficient for busy addresses.|addr — the address to count for|int64 count|GetTransactionsByAccount failures|

### **Database Utilities**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetMerkleRoot**|conn \*PooledConnection → []byte, error|Returns the current Merkle root hash of the database — a fingerprint of all stored data.|none|[]byte root hash|CurrentState API failure|
|**GetDatabaseState**|ic \*ImmuClient → \*ImmutableState, error|Returns the full immutable state object (root hash + transaction ID).|ic — raw client|\*ImmutableState|Connection or state API failure|
|**GetHistory**|ic \*ImmuClient, key string, limit int → []\*schema.Entry, error|Retrieves all past versions of a key's value (ImmuDB is append-only — old values are kept).|limit — max versions to return|[]\*schema.Entry|History API failure|
|**IsHealthy**|ic \*ImmuClient → bool|Returns true if the database is reachable and accepting requests.|none|bool|none (returns false on error)|
|**Ping**|ic \*ImmuClient → error|Sends a heartbeat ping to the database.|none|error if unreachable|Network errors|
|**Close**|ic \*ImmuClient → error|Closes the client connection cleanly.|none|error on failure|Disconnect errors|

### **Block Hashing**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**NewBlockHasher**|→ \*BlockHasher|Creates a reusable hasher for block fingerprinting.|none|\*BlockHasher instance|none|
|**HashBlock**|h \*BlockHasher, nonce, sender string, timestamp int64 → string|Hashes a block's key fields into a deterministic hex string.|nonce, sender — block fields; timestamp|hex hash string|none|

### **Transactions (Atomic Writes)**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**Transaction**|ic \*ImmuClient, fn func(tx \*ImmuTransaction) error → error|Runs fn inside an ImmuDB transaction. If fn returns an error the transaction is rolled back.|fn — your write logic wrapped in a function|error if fn fails or commit fails|Connection errors, fn panics|
|**Set**|tx \*ImmuTransaction, key string, value interface{} → error|Queues a write inside an active transaction. Only takes effect when the transaction commits.|tx — from Transaction(); key, value|error if encoding fails|ErrEmptyKey, ErrNilValue|

### **Internal Helpers**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**withRetry**|ic \*ImmuClient, operation string, fn func() error → error|Runs fn up to retryLimit times. Retries only on connection errors, not on data errors.|operation — name for logging; fn — the call to retry|error after retries exhausted|Connection errors trigger retry|
|**toBytes**|value interface{} → []byte, error|Converts any value to bytes. Strings and []byte pass through; everything else is JSON-encoded.|value — any type|[]byte|ErrNilValue, JSON encode failure|
|**isConnectionError**|err error → bool|Returns true if err is a gRPC connectivity error (codes 1, 4, 14).|err — any error|bool|none|
|**isNotFoundError**|err error → bool|Returns true if err means the key was not in the database.|err — any error|bool|none|
|**reconnect**|ic \*ImmuClient, FUNCTION string → error|Marks a connection as disconnected. Does NOT reconnect — reconnection happens via the pool.|FUNCTION — name for log message|always returns an error|(intentional — signals pool to replace conn)|

## **3.4  immuclient\_helper.go — Block Transaction Helper**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetTransactionsOfBlock**|conn \*PooledConnection, blockNumber uint64 → []\*Transaction, error|Loads a block by number and returns just its transaction list. Shortcut for GetZKBlockByNumber + block.Transactions.|blockNumber — which block to load|[]\*Transaction|Block not found, connection errors|

## **3.5  account\_immuclient.go — Account & Nonce Management**
Everything to do with wallet accounts, DID identities, and transaction nonces lives here. This file talks to the **accounts database**.

### **Account Set (Helper Type)**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**NewAccountsSet**|→ \*AccountsSet|Creates an empty set you can collect addresses into, then pass to GetMultipleAccounts for a single bulk lookup.|none|\*AccountsSet|none|
|**(AccountsSet).Add**|address common.Address|Adds a wallet address to the set.|address — the address|nothing|none|

### **Account Creation & Storage**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**CreateAccount**|conn \*PooledConnection, DIDAddress string, Addr common.Address, metadata map[string]interface{} → error|Creates a new wallet account with a linked DID. Fails if the account already exists (prevents overwriting).|DIDAddress — the DID URI; Addr — wallet address; metadata — extra fields|error on failure|Empty DID, address already exists, storage failures|
|**storeAccount**|conn \*PooledConnection, KeyDoc \*Account → error|Internal: writes the Account struct to the database and creates a DID→address reference. Refuses to overwrite existing records to prevent balance attacks.|KeyDoc — populated Account struct|error on failure|Nil doc, empty fields, transaction failures|
|**BatchCreateAccountsOrdered**|conn \*PooledConnection, entries []struct{Key string; Account \*Account} → error|Creates many accounts atomically. All succeed or all fail together.|entries — ordered list of key+account pairs|error on failure|Marshalling, ExecAll failures|
|**BatchRestoreAccounts**|conn \*PooledConnection, entries []struct{Key string; Value []byte} → error|Restores accounts from a backup. Uses Last-Write-Wins (compares UpdatedAt timestamps) to resolve conflicts safely.|entries — raw key+bytes pairs from backup|error on failure|Unmarshal, timestamp comparison, ExecAll failures|

### **Account Retrieval**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetAccount**|conn \*PooledConnection, addr common.Address → \*Account, error|Loads a wallet account by its Ethereum-style address.|addr — the wallet address|\*Account or error|Account not found, connection errors|
|**GetAccountByDID**|conn \*PooledConnection, did string → \*Account, error|Loads an account via its DID key. Follows the DID→address reference automatically.|did — the DID URI string|\*Account or error|DID not found, reference broken|
|**ListAllAccounts**|conn \*PooledConnection, limit int → []\*Account, error|Returns all accounts up to limit (0 = no limit). Scans the address: prefix. Avoid calling without a limit on large chains.|limit — max results (0 = unlimited)|[]\*Account|Scan or load failures|
|**ListAccountsPaginated**|conn \*PooledConnection, limit, offset int, extPrefix string → []\*Account, error|Paginates account listings. offset skips the first N results; limit controls page size.|offset — skip N accounts; limit — page size; extPrefix — narrow search|[]\*Account|Scan or load failures|
|**GetMultipleAccounts**|conn \*PooledConnection, accounts \*AccountsSet → map[string]\*Account, error|Loads many accounts in one database round trip. Much faster than calling GetAccount in a loop.|accounts — set built with NewAccountsSet|map[addr hex → \*Account]|GetAll failures, unmarshal errors|
|**CountAccounts**|conn \*PooledConnection → int, error|Returns total number of accounts using ImmuDB's native Count API.|none|int count|Count API failure|

### **Account Mutation**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**UpdateAccountBalance**|conn \*PooledConnection, addr common.Address, newBalance string → error|Reads the account, sets the new balance, updates the timestamp, and writes it back using SafeCreate (verified write).|addr — account to update; newBalance — new balance as string|error on failure|Account not found, SafeCreate failure|

### **Nonce Management**
A **nonce** is a sequential counter attached to each transaction from an address. It prevents replay attacks. Every new transaction must have a nonce exactly one higher than the last.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**PutNonceofAccount**|→ uint64, error|Generates a unique nonce from the current timestamp and an atomic counter. Used during account creation.|none|uint64 nonce|none (always succeeds)|
|**CheckNonceDuplicate**|conn \*PooledConnection, fromAddr \*common.Address, nonce uint64 → bool, error|Returns true if this nonce has already been used by the address. Prevents replaying old transactions.|fromAddr — sender address; nonce — nonce to check|bool (true = duplicate)|GetTransactionsByAccount failures|
|**GetLatestNonce**|conn \*PooledConnection, fromAddr \*common.Address → uint64, error|Returns the highest nonce seen for an address. The next valid nonce is latestNonce + 1.|fromAddr — sender address|uint64 latest nonce|GetTransactionsByAccount failures|
|**CheckNonceAndGetLatest**|conn \*PooledConnection, fromAddr \*common.Address, submittedNonce uint64 → bool, uint64, bool, error|Combines CheckNonceDuplicate and GetLatestNonce in one pass. Returns (isDuplicate, latestNonce, foundAnyTx, error). More efficient than calling both separately.|submittedNonce — the nonce on the incoming tx|bool, uint64, bool, error|Block scan failures|

### **Transaction Queries for Accounts**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetTransactionsByAccount**|conn \*PooledConnection, addr \*common.Address → []\*Transaction, error|Returns all transactions involving the address (sent or received). Scans the full chain — slow on busy addresses.|addr — the address|[]\*Transaction|Scan failures|
|**GetTransactionsByAccountPaginated**|conn \*PooledConnection, addr \*common.Address, offset, limit int → []\*Transaction, int, error|Paginated version of the above. Returns a page of results and the total count.|offset — skip N; limit — page size|[]\*Transaction, totalCount|GetTransactionsByAccount failures|
|**GetTransactionsPaginated**|conn \*PooledConnection, offset, limit int → []\*Transaction, int, error|Paginated retrieval of ALL transactions (not filtered to one address).|offset — skip N; limit — page size|[]\*Transaction, totalCount|GetAllKeys or GetTransactionByHash failures|
|**GetTransactionHashes**|conn \*PooledConnection, offset, limit int → []string, int, error|Returns just the hashes (not full transaction data) in paginated form.|offset — skip N; limit — page size|[]string hashes, totalCount|GetAllKeys failures|
|**isTransactionInvolvingAccount**|tx Transaction, addr \*common.Address → bool|Internal: checks whether a tx's From or To field matches the address.|tx — transaction to check; addr — address|bool|none|

### **Internal Connection Helpers**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**ensureAccountsDBSelected**|conn \*PooledConnection → error|Ensures the accounts database is selected and the auth token is fresh.|conn — connection to verify|error if refresh fails|Token expired, UseDatabase failure|
|**reconnectToAccountsDB**|conn \*PooledConnection → error|Attempts to re-establish a dropped accounts database connection.|conn — the broken connection|error if reconnect fails|Connection failures|
|**loadAccountByKey**|conn \*PooledConnection, key []byte, logFn string → \*Account, error|Internal: low-level account loader. Follows DID references automatically. Used by GetAccount and GetAccountByDID.|key — raw database key; logFn — caller name for logs|\*Account or error|Get failure, unmarshal errors|

## **3.6  Accounts\_helper.go — Convenience Wrappers & Record Counting**
Thin helpers that make common patterns shorter to write.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetImmuClient**|→ \*PooledConnection, error|Shortcut for GetMainDBConnection(). Gets a main DB connection.|none|\*PooledConnection|Pool errors|
|**CloseImmuClient**|conn \*PooledConnection → error|Shortcut for PutMainDBConnection(). Returns a main DB connection.|conn — connection to return|error|none|
|**GetAccountsImmuClient**|→ \*PooledConnection, error|Shortcut for GetAccountsConnections(). Gets an accounts DB connection.|none|\*PooledConnection|Pool errors|
|**CloseAccountsImmuClient**|conn \*PooledConnection → error|Shortcut for PutAccountsConnection(). Returns an accounts DB connection.|conn — connection to return|error|none|
|**GetCountofRecords**|conn \*PooledConnection, ConnType int, prefix string → int, error|Counts records with prefix in either database. ConnType 0 = Main DB, ConnType 1 = Accounts DB.|ConnType — 0 or 1; prefix — key prefix|int count|Invalid ConnType, Count API failure|
|**CountBuilder.Build**|→ \*CountBuilder, error|Factory that creates a CountBuilder — a helper object to count records in both databases without managing connections.|none|\*CountBuilder|none|
|**CountBuilder.GetMainDBCount**|prefix string → int, error|Counts records in the main database matching prefix.|prefix|int count|GetCountofRecords failures|
|**CountBuilder.GetAccountsDBCount**|prefix string → int, error|Counts records in the accounts database matching prefix.|prefix|int count|GetCountofRecords failures|

## **3.7  BlockLogs.go — Ethereum-Compatible Event Logs**
These functions retrieve transaction event logs — the eth\_getLogs equivalent used by the gETH RPC layer.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetLogs**|conn \*PooledConnection, filterQuery FilterQuery → []Log, error|Fetches logs from a block range that match address and topic filters. The FilterQuery specifies FromBlock, ToBlock, Addresses, and Topics.|filterQuery — see FilterQuery struct for fields|[]Log|Connection, invalid range, filter errors|
|**GetLogsFromBlock**|conn \*PooledConnection, block \*ZKBlock, filterQuery FilterQuery → []Log, error|Applies the filter to logs from a single already-loaded block. Called internally by GetLogs.|block — the block to filter; filterQuery|[]Log from that block|Receipt retrieval or conversion errors|

## **3.8  BulkGetBlock.go — Efficient Block Range Retrieval**
Use these when you need to load many blocks at once — for example during chain sync or explorer queries. They are much faster than calling GetZKBlockByNumber in a loop.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetBlocksRange**|conn \*PooledConnection, start, end uint64 → []\*ZKBlock, error|Loads all blocks from start to end inclusive in a single ImmuDB GetAll call (one network round trip).|start — first block; end — last block|[]\*ZKBlock|start > end, GetAll failure, unmarshal errors|
|**NewBlockIterator**|conn \*PooledConnection, start, end uint64, batchSize int → \*BlockIterator|Creates an iterator to page through a large block range in batches. Use when the range is too large to load at once.|batchSize — blocks per page (default 1000 if <= 0)|\*BlockIterator|none (factory)|
|**(BlockIterator).Next**|→ []\*ZKBlock, error|Returns the next batch of blocks. Returns nil (and no error) when all blocks have been returned.|none (call repeatedly until nil)|[]\*ZKBlock batch|GetBlocksRange failures|

|**Tip:**  Prefer GetBlocksRange for fixed ranges. Use BlockIterator when you don't know how many blocks you'll need or when you want to process them in streaming fashion without loading everything into memory at once.|
| :- |

## **3.9  BulkGetAccounts.go — Bulk Account Lookup**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetMultipleAccounts**|conn \*PooledConnection, accounts \*AccountsSet → map[string]\*Account, error|Loads many accounts in a single ImmuDB GetAll call. Build the set with NewAccountsSet, add addresses with Add, then call this.|accounts — set of addresses to load|map[address hex → \*Account]|GetAll failure, unmarshal errors|

## **3.10  Facade\_Receipts.go — Transaction Receipts**
A transaction receipt is the result record that Ethereum-compatible clients expect after a transaction is processed. It contains status, gas used, logs, and a bloom filter.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**GetReceiptByHash**|conn \*PooledConnection, hash string → \*Receipt, error|Loads the receipt for a transaction by its hash. Returns nil (no error) if the transaction is still being processed (tx\_processing = -1).|hash — with or without 0x prefix|\*Receipt or nil|tx not found, processing check failures|
|**generateReceiptFromTransaction**|conn \*PooledConnection, tx \*Transaction, block \*ZKBlock, txIndex uint64 → \*Receipt|Internal: builds a Receipt from raw transaction and block data. Calculates cumulative gas used and constructs the logs bloom filter.|txIndex — position of tx within the block|\*Receipt|none (always returns a receipt)|
|**GetReceiptsofBlock**|conn \*PooledConnection, blockNumber uint64 → []\*Receipt, error|Returns receipts for every transaction in a block.|blockNumber — the block to get receipts for|[]\*Receipt|Transaction retrieval or generation failures|
|**MakeReceiptRoot**|conn \*PooledConnection, receipts []\*Receipt → []byte, error|Computes the Merkle root hash over a slice of receipts. Used when constructing block headers.|receipts — all receipts for a block|[]byte Merkle root|Root computation failures|

## **3.11  HashMapValidator.go — HashMap Key Validation**
Used during fast-sync to verify that a peer's HashMap (state snapshot) contains keys that actually exist in your database.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**ValidateHashMapKeys**|hashMap \*HashMap, dbClient \*PooledConnection, dbType string → \*HashMap, error|Checks every key in hashMap exists in the database. Processes in batches of 100. Returns a new HashMap with only valid keys.|hashMap — the map to validate; dbType — label for logs|\*HashMap (validated keys only)|Client nil, check failures (logged as warnings)|
|**ValidateHashMapKeysIncremental**|hashMap \*HashMap, dbClient \*PooledConnection, dbType string → \*HashMap, error|Same as ValidateHashMapKeys but uses larger batches of 1000 to reduce memory pressure on big maps.|Same as above|\*HashMap (validated keys only)|Same as above|

## **3.12  Immudb\_AVROfile.go — AVRO Backup Export**
Exports database contents to an Apache AVRO file — a compact, compressed binary format suitable for long-term storage and data transfer.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**BackupFromHashMap**|cfg Config, MAP \*hashmap.HashMap → error|Connects to ImmuDB, reads every key in MAP, and writes the values to an AVRO OCF file with Snappy compression. Logs statistics on how many block keys vs other keys were exported.|cfg.outputPath — where to write the file; MAP — the set of keys to export|error on failure|Connection, Get, or AVRO write failures|

## **3.13  logger.go — Structured Logging**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**logger**|NamedLogger string → \*ion.Ion|Returns the shared structured logger instance for DB\_OPs. All functions use this to write logs in a consistent JSON format.|NamedLogger — a label shown in log output|\*ion.Ion logger|Returns nil if logger not initialised|

## **3.14  merkletree/merkle.go — Merkle Tree Builder**
Builds and reconstructs Merkle trees over block ranges. Used by the AVC consensus layer to prove chain integrity.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**NewMerkleProof**|→ MerkleProofInterface|Factory that creates a new MerkleProof instance.|none|MerkleProofInterface|none|
|**(MerkleProof).GetMainDBConnection**|→ \*MerkleProof|Acquires a main DB connection and stores it in the MerkleProof for use during tree generation.|none|\*MerkleProof with connection|Logs error if connection fails|
|**(MerkleProof).PutMainDBConnection**|→ nothing|Returns the connection stored by GetMainDBConnection back to the pool.|none|nothing|none|
|**(MerkleProof).GenerateMerkleTree**|startBlock, endBlock int64 → \*MerkleTreeSnapshot, error|Builds a Merkle tree over blocks startBlock..endBlock. Pass endBlock=-1 to use the latest block. Fills gaps with empty hashes.|startBlock — first block; endBlock — last (-1=latest)|\*MerkleTreeSnapshot|Invalid range, block retrieval, or builder failures|
|**(MerkleProof).ReconstructTree**|snap \*MerkleTreeSnapshot → \*Builder, error|Recreates a Merkle Builder from a previously saved snapshot. Used to verify historical proofs without re-scanning the chain.|snap — the saved snapshot|\*merkletree.Builder|FromSnapshot failures|

## **3.15  sqlops/sqlops.go — Local SQLite Database**
A small SQLite database stored on the local filesystem (not ImmuDB). It is used for fast local lookups of peer connections, node metadata, and Merkle hashes that do not need to be on-chain.

### **Setup**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**NewUnifiedDB**|→ \*UnifiedDB, error|Opens (or creates) the SQLite file and creates the three tables — peers, key-value, and merkle — if they do not yet exist.|none|\*UnifiedDB|Directory creation failure, SQLite open failure, schema failure|
|**initializeSchema**|db \*sql.DB → error|Internal: runs CREATE TABLE IF NOT EXISTS for all three tables.|db|error|SQL execution failure|
|**(UnifiedDB).Close**|→ error|Closes the SQLite connection cleanly. Call this on shutdown.|none|error|Close failure|

### **Peer Management**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**(UnifiedDB).AddPeer**|peerID, publicAddr string, connections int, capabilities []string → error|Inserts or updates a peer entry. capabilities is stored as a comma-separated string.|peerID — libp2p peer ID; publicAddr — host:port; capabilities — list of supported protocols|error|SQL exec failure|
|**(UnifiedDB).GetPeer**|peerID string → string, int, []string, int64, error|Retrieves a peer's public address, connection count, capabilities list, and lastSeen timestamp.|peerID — peer to look up|addr, conns, caps, lastSeen|Query or scan failure|
|**(UnifiedDB).DeletePeer**|peerID string → error|Removes a peer from the database. Returns an error if the peer was not found.|peerID — peer to remove|error|SQL exec failure, rows-affected = 0|
|**(UnifiedDB).GetPeers**|maxConnections, limit int → []string, error|Returns up to limit peer addresses where connection count < maxConnections, ordered by most recently seen.|maxConnections — filter; limit — max results|[]string addrs|Query or scan failure|
|**(UnifiedDB).GetAllPeers**|→ []string, error|Returns addresses of every known peer.|none|[]string addrs|Query or scan failure|
|**(UnifiedDB).UpdatePeerConnections**|peerID string, connections int → error|Updates the connection count and lastSeen time for a peer.|peerID; connections — new connection count|error|SQL exec failure, peer not found|
|**(UnifiedDB).GetConnectedPeers**|→ []PeerInfo, error|Returns all connected peers as PeerInfo structs.|none|[]PeerInfo|Schema query, scan failure|
|**(UnifiedDB).GetConnectedPeersAsMap**|→ []map[string]interface{}, error|Same as GetConnectedPeers but returns maps (easier for JSON serialisation).|none|[]map|Query or scan failure|
|**(UnifiedDB).CountConnectedPeers**|→ int, error|Counts rows in the connected\_peers table.|none|int|Query failure|

### **Key-Value Store**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**(UnifiedDB).StoreKeyValue**|key, value string → error|Inserts or updates a generic string key-value pair with a timestamp.|key, value|error|SQL exec failure|
|**(UnifiedDB).GetKeyValue**|key string → string, error|Retrieves a value by key.|key|string value|SQL scan failure, key not found|
|**(UnifiedDB).DeleteKeyValue**|key string → error|Deletes a key-value pair. Errors if not found.|key|error|SQL exec failure, key not found|
|**(UnifiedDB).GetAllKeyValues**|→ map[string]string, error|Returns all key-value pairs as a map.|none|map|Query or scan failure|

### **Merkle Hash Store**

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**(UnifiedDB).StoreMerkleHash**|key, valueHash string → error|Stores a hash for a key to be included in Merkle tree computation.|key; hash — computed hash|error|SQL exec failure|
|**(UnifiedDB).GetAllMerkleHashes**|→ map[string]string, error|Returns all stored hashes as a map for Merkle tree building.|none|map[key→hash]|Query or scan failure|

## **3.16  common/GRO.go — Goroutine Manager Initialisation**
GRO (Goroutine Orchestrator) tracks background goroutines so the node can shut them down cleanly. This file provides the DB\_OPs initialisation hook.

|**Function**|**Signature**|**What it does**|**Key parameters**|**Returns**|**Errors**|
| :- | :- | :- | :- | :- | :- |
|**InitializeGRO**|Local string → LocalGoroutineManagerInterface, error|Reads the GRO config and creates a LocalGoroutineManager identified by Local. Called once per subsystem that needs managed goroutines.|Local — unique name for this manager instance|LocalGoroutineManagerInterface|GRO config failure, NewLocalManager failure|


# **4.  Common Patterns for the DB Upgrade**
## **4.1  Safe Connection Pattern**
Always use the auto-return helpers so you can never forget to release a connection:

|<p>ctx, cancel := context.WithTimeout(context.Background(), 5\*time.Second)</p><p>defer cancel()</p><p></p><p>conn, err := GetMainDBConnectionandPutBack(ctx)  // auto-returns on ctx expiry</p><p>if err != nil { return err }</p><p></p><p>block, err := GetZKBlockByNumber(conn, blockNumber)</p>|
| :- |

## **4.2  Batch Over Loop**
When loading multiple blocks or accounts, always prefer the batch functions over a loop — they use one database round trip instead of N:

|**// Slow: N round trips**|**// Fast: 1 round trip**|
| :- | :- |
|<p>for \_, num := range blockNums {</p><p>`  `b, \_ := GetZKBlockByNumber(conn, num)</p><p>}</p>|blocks, \_ := GetBlocksRange(conn, start, end)|

## **4.3  Checking If a Key Exists**
Use Exists() before writing if you must not overwrite (e.g. creating accounts). Do not use Read + check for ErrNotFound — that does two round trips.

## **4.4  Use Transactions for Multi-Key Writes**
If you are writing more than one key that must all succeed or all fail, wrap them in Transaction(). A partial write without a transaction can leave the database in an inconsistent state.

|**Note for the DB upgrade:**  When replacing ImmuDB with a new backend, every function in this document needs a new implementation. The function signatures (names, parameters, return types) should stay identical — that way none of the callers in Block, gETH, Sequencer, or explorer need to change. Only the body of each function changes to talk to the new database.|
| :- |


# **5.  Quick Lookup: What Function Do I Need?**

|**I want to…**|**Use this function**|
| :- | :- |
|Read a block by its number|GetZKBlockByNumber(conn, blockNumber)|
|Read a block by its hash|GetZKBlockByHash(conn, blockHash)|
|Find out the current chain height|GetLatestBlockNumber(conn)|
|Load a range of blocks efficiently|GetBlocksRange(conn, start, end)|
|Stream through all blocks without using much memory|NewBlockIterator + .Next()|
|Save a new block|StoreZKBlock(conn, block)|
|Look up a transaction by hash|GetTransactionByHash(conn, txHash)|
|Get all transactions for an address|GetTransactionsByAccount(conn, addr)|
|Get paginated transactions for an address|GetTransactionsByAccountPaginated(conn, addr, offset, limit)|
|Count all transactions|CountTransactions(conn)|
|Get a transaction receipt|GetReceiptByHash(conn, hash)|
|Get all receipts for a block|GetReceiptsofBlock(conn, blockNumber)|
|Fetch event logs with a filter|GetLogs(conn, filterQuery)|
|Create a wallet account|CreateAccount(conn, did, addr, metadata)|
|Load an account by address|GetAccount(conn, addr)|
|Load an account by DID|GetAccountByDID(conn, did)|
|Load many accounts in one call|NewAccountsSet + Add + GetMultipleAccounts|
|Update an account's balance|UpdateAccountBalance(conn, addr, newBalance)|
|List all accounts (paginated)|ListAccountsPaginated(conn, limit, offset, prefix)|
|Count all accounts|CountAccounts(conn)|
|Check if a nonce is a duplicate|CheckNonceAndGetLatest(conn, addr, nonce)|
|Get the latest nonce for an address|GetLatestNonce(conn, addr)|
|Count records with a key prefix|GetCountofRecords(conn, connType, prefix)|
|Check if a key exists|Exists(conn, key)|
|Write one key-value pair|Create(conn, key, value)|
|Write many key-value pairs atomically|BatchCreate(conn, entries)|
|Read with cryptographic proof|SafeRead(ic, key)|
|Get the database Merkle root|GetMerkleRoot(conn)|
|Build a Merkle tree over blocks|NewMerkleProof + GenerateMerkleTree(start, end)|
|Export DB contents to AVRO file|BackupFromHashMap(cfg, hashMap)|
|Manage peer connections locally|NewUnifiedDB + AddPeer / GetPeers|

Page   of     |   Confidential — Internal Use Only
