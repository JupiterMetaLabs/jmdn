# IMMUDB to ThebeDB Migration Map

## Call sites table (file | symbol | replacement)

| File | ImmuDB symbol(s) used | Replacement |
|---|---|---|
| `DB_OPs/immuclient.go` | `Set`, `Get`, `Scan`, `VerifiedSet`, `VerifiedGet`, `History`, `ExecAll`, `UseDatabase`, `schema.ScanRequest`, `schema.HistoryRequest`, `schema.Op`, `schema.ExecAllRequest`, `schema.ImmutableState` | `Set`/`VerifiedSet` -> `thebedb.Append()` + `cassata.SubmitAddress/SubmitBlock/SubmitTransaction/SubmitZKProof/SubmitSnapshot`; `Get`/`VerifiedGet` -> `cassata.GetBlock` / `cassata.GetTransaction` / `cassata.GetAccount` / `cassata.GetSnapshot`; `Scan` -> `cassata.ListBlocks` / `cassata.ListTransactions` / `cassata.ListAccounts`; `History` -> `cassata.ScanKV` (raw KV log) or SQL timeline query; `ExecAll` -> multiple synchronous `thebedb.Append()` calls plus inline SQL projection; `schema.ImmutableState` -> Cassata/Thebe health/status response model |
| `DB_OPs/account_immuclient.go` | `Get`, `Scan`, `ExecAll`, `UseDatabase`, `Login`, `schema.Op_Kv`, `schema.Op_Ref`, `schema.ReferenceRequest`, `schema.ScanRequest`, `schema.ExecAllRequest`, `client.DefaultOptions`, `client.NewImmuClient` | `Get` -> `cassata.GetAccount`; account tx lookups -> `cassata.ListTransactions`; `Scan` -> `cassata.ListAccounts` or `cassata.ListTransactions`; `ExecAll` (KV+Ref atomic batches) -> ordered `thebedb.Append()` events with synchronous Cassata projection; DID reference edges currently modeled by `Op_Ref` should become SQL relation/materialized lookup table; connection/bootstrap calls replaced by Thebe config/bootstrap |
| `FastsyncV2/fastsyncv2.go` | startup and sync orchestration over protocol routers | Keep on V2; update data fetch internals to Cassata/Thebe readers as immudb is removed |
| `DB_OPs/Immudb_AVROfile.go` | `schema.NewImmuServiceClient`, `Login`, `UseDatabase`, `Get`, `schema.KeyRequest` | Replace source extraction with Cassata/Thebe readers (`Get*`, `List*`, or SQL export query) and remove direct gRPC immudb client usage |
| `Scripts/check_nonce_dupes.go` | `immudb.DefaultOptions`, `immudb.NewClient`, `immudb.ImmuClient`, `schema.ScanRequest`, `Scan` | Replace with Cassata query path (nonce/account transaction query in SQL) and optional `cassata.ListAccounts` / `ListTransactions` pagination |
| `DB_OPs/MainDB_Connections.go` | `client.DefaultOptions`, `client.NewImmuClient`, `Login`, `UseDatabase`, `CreateDatabase`, `schema.Database`, `schema.DatabaseSettings` | Remove immudb pool lifecycle and database provisioning; replace with ThebeDB + SQL DSN bootstrap (already present in `thebeprofile`) |
| `DB_OPs/Account_Connections.go` | `client.DefaultOptions`, `client.NewImmuClient`, `Login`, `CreateDatabase`, `schema.DatabaseSettings` | Same as above; remove accounts-specific immudb DB creation and migrate to Cassata account table initialization/migrations |
| `config/ConnectionPool.go` | `client.DefaultOptions`, `client.NewImmuClient`, `Login`, `UseDatabase`, `schema.Database` | Replace `ConnectionPool` from immudb sessions to Thebe/Cassata dependency holder (KV + SQL handles) and remove token/session refresh logic tied to immudb |
| `config/ImmudbConstants.go` | imports `github.com/codenotary/immudb/pkg/client`, `github.com/codenotary/immudb/pkg/api/schema`; types `ImmuClient`, `ImmuTransaction`; constants `DBAddress`, `DBPort`, `DBName`, `AccountsDBName`, `State_Path_Hidden` | Replace with Thebe/Cassata config/constants package: Thebe KV path, SQL DSN, Redis URL/stream, and append transaction abstraction instead of `schema.Op` |
| `CLI/CLI_GRPC.go` | `schema.ImmutableState` | Replace CLI DB stats payload source with Thebe/Cassata status struct (health, append position, projection lag) |
| `CLI/GRPC_Server.go` | `schema.ImmutableState` | Same as above; convert response mapper from immudb state to Thebe/Cassata diagnostics |
| `explorer/BlockOps.go` | `schema.ImmutableState` field type | Replace explorer state type with Thebe/Cassata status model |

## Interface changes needed

| Interface | Where | Current immudb coupling | ThebeDB status | Change needed |
|---|---|---|---|---|
| `PrimaryWriter` | `DB_OPs/dualdb/dualdb.go` | Explicit immudb-centered signatures (`SafeCreate(*config.ImmuClient, ...)`, `BatchCreate` modeled around `ExecAll`) | **Partially** satisfied by current dual-write adapters | Keep business operations, but remove `*config.ImmuClient` and `ExecAll` assumptions; prefer domain-level methods (`AppendEvent`, `SubmitBlock`, `SubmitAccount`, etc.) |
| `ShadowWriter` | `DB_OPs/dualdb/dualdb.go` | Mirrors immudb signatures and semantics | **Partially** satisfied by `shadow_adapter`, still shaped by immudb naming | Same refactor as `PrimaryWriter`; move to backend-neutral domain interface |
| `dbOps` | `DID/DID.go` | Uses `*config.PooledConnection` returned from immudb pools | **Not directly**; methods are domain-level, transport is immudb-specific | Keep method set mostly intact but replace connection parameter type with Cassata/Thebe handle (or hide connection entirely behind repository methods) |
| `MerkleProofInterface` | `DB_OPs/merkletree/merkle.go` | Methods `GetMainDBConnection`/`PutMainDBConnection` are tied to main immudb pool lifecycle | **Not yet** | Replace explicit pool management methods with reader dependency injection or Cassata-backed block fetch service |

## Config keys to remove/replace

### Application config (YAML/env)

| Current key/env | Location | Action |
|---|---|---|
| `database.username` | `jmdn.yaml`, `config/settings/config.go`, `config/settings/loader.go` | Remove for pure Thebe mode, or repurpose to Thebe SQL credentials only if DSN decomposition is introduced |
| `database.password` | `jmdn.yaml`, `config/settings/config.go`, `config/settings/loader.go` | Same as above |
| `JMDN_DATABASE_USERNAME` | implied by Viper env mapping | Remove/ignore after cutover |
| `JMDN_DATABASE_PASSWORD` | implied by Viper env mapping | Remove/ignore after cutover |
| `--immudb-user`, `--immudb-pass` | `Scripts/migrate_immudb_to_thebe.go` | Keep only for one-time migration utility; do not keep in steady-state runtime |

### Hardcoded immudb constants and client settings

| Current key/setting | Location | Action |
|---|---|---|
| `DBAddress` (`localhost`) | `config/ImmudbConstants.go` | Remove; replace with Thebe/Cassata connection config |
| `DBPort` (`3322`) | `config/ImmudbConstants.go` | Remove |
| `DBName` (`defaultdb`) | `config/ImmudbConstants.go` | Remove |
| `AccountsDBName` (`accountsdb`) | `config/ImmudbConstants.go` | Remove |
| `State_Path_Hidden` (`./.immudb_state`) | `config/ImmudbConstants.go` | Remove; no immudb state dir after cutover |
| immudb token/session lifetimes (`TokenMaxLifetime`, token refresh paths) | `config/ConnectionPool.go` | Remove immudb session management; replace with SQL pool settings and Thebe append client settings |

### Ops/service scripts still referencing immudb

| Script/config | Current reference | Action |
|---|---|---|
| `Scripts/install_services.sh` | `immudb` system service units and dependencies | Replace with ThebeDB/Postgres/Redis service dependencies or document external managed services |
| `Scripts/setup_dependencies.sh` | `--immudb`, binary download/install | Remove immudb install path after migration window closes |
| `Scripts/check_nonce_dupes.go` | direct immudb scan tool | Rewrite against Cassata SQL data |

## Tests to migrate

| Test file | Current immudb dependency type | Replace with existing `cassata_integration_test.go` coverage? | Additional work needed |
|---|---|---|---|
| `DB_OPs/Tests/immuclient_test.go` | Real immudb integration via `InitMainDBPool`, `GetMainDBConnection`, verified ops, transactions | **Partially**. CRUD/read/list behavior can be covered by Cassata integration tests already present | Add Thebe repository tests for transaction grouping (`ExecAll` replacement), history semantics, and connection lifecycle equivalents |
| `DB_OPs/Tests/account_immuclient_test.go` | Real immudb integration for account CRUD/scan/batch paths | **Partially**. Account ingest/read/update already covered in Cassata tests | Add tests for DID/address relation behavior (currently `Op_Ref`) and account pagination semantics |
| `DB_OPs/Tests/BulkGetBlock_test.go` | Main DB pool + immudb block reads | **Mostly yes** via Cassata block list/get tests | Add range/pagination edge-case tests matching existing block-range behavior |
| `DB_OPs/Tests/Merkle_test.go` | Main DB pool + block fetch from immudb | **No** | New tests needed for Merkle generation against Cassata-backed block source |
| `DB_OPs/dualdb/dualdb_test.go` | Mocked interfaces but signatures include `*config.ImmuClient` | **N/A** (already mock-based) | Update mocks/signatures once interfaces become backend-neutral |
| `Scripts/check_nonce_dupes.go` (tooling validation) | Direct immudb scan | **No** | Add SQL/Cassata nonce consistency test/tool |

## Estimated risk per area (low/medium/high)

| Area | Risk | Why |
|---|---|---|
| Core write path (`DB_OPs/immuclient.go`, `DB_OPs/account_immuclient.go`) | **High** | Contains most `Set`/`VerifiedSet`/`ExecAll` logic and atomicity assumptions; incorrect mapping can cause ledger divergence |
| Read/query path migration (`Get`/`Scan`/`History` callers) | **Medium** | Semantics change from KV scans to SQL/list APIs; pagination/order differences are likely |
| Interface refactor (`dualdb`, `DID`, `merkletree`) | **Medium** | Signatures leak immudb types; needs coordinated refactor across callers and tests |
| Config/runtime cleanup | **Low-Medium** | Straightforward removal/replacement, but startup regressions possible if stale keys remain referenced |
| Script/service ecosystem | **Medium** | Operational tooling still provisions/runs immudb directly; deployment drift likely if not updated together |
| Test migration and parity | **High** | Existing tests validate immudb-specific behavior; parity gaps can hide regressions unless replaced with Thebe-focused integration/contract tests |

