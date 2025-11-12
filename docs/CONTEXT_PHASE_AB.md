# INFO: Phase A & Phase B Implementation Blueprint

Last updated: 2025-11-11  
Applies to: Public L2 node (`/Users/naman/JM/repos/jmdn-extra`)

---

## Phase A – Entry & Bootstrap Context Propagation

### A.1 Baseline Observations
- `main.go` creates an interrupt channel and calls `context.WithCancel(context.Background())` (`main.go:570-584`). Cancellation only fires when the goroutine calls `cancel()`, and `os.Exit(1)` terminates immediately without allowing deferred clean-up.
- `node.NewNode()` (see `node/node.go:114-205`) has no context parameter. Downstream operations (libp2p host setup, handler registration) cannot honour shutdown signals.
- `initPubSub()` (`main.go:500-515`) and other bootstrap helpers take no context. Any failure or cancellation depends on implicit timeouts.

### A.2 Implementation Steps

#### Step A1 – Replace manual signal goroutine with `signal.NotifyContext`

```518:575:main.go
func main() {
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    cfg := mustLoadConfig()

    bootCtx, cancelBoot := context.WithTimeout(ctx, cfg.Timeouts.Bootstrap)
    defer cancelBoot()

    node, err := node.NewNodeWithContext(bootCtx, cfg.Node)
    if err != nil {
        log.Fatal().Err(err).Msg("failed to initialize node")
    }
    defer node.Host.Close()

    // ...
    <-ctx.Done()
    log.Info().Msg("shutdown signal received; waiting for subsystems to drain")
}
```

**Key changes:**
- Root context is bound to OS interrupts (`SIGINT`, `SIGTERM`); no manual `os.Exit`.
- Bootstrap work executes under `bootCtx` with a bounded timeout.
- The process blocks on `<-ctx.Done()` to allow deferred clean-up.

#### Step A2 – Introduce `node.NewNodeWithContext`

```114:205:node/node.go
// NewNodeWithContext creates and starts a libp2p node under the supplied context.
func NewNodeWithContext(ctx context.Context, opts config.NodeOptions) (*config.Node, error) {
    privKey, peerID, err := loadOrCreatePrivateKey()
    if err != nil {
        return nil, fmt.Errorf("failed to load/create Peer ID: %w", err)
    }

    host, err := libp2p.New(
        libp2p.Identity(privKey),
        libp2p.ListenAddrStrings(opts.ListenAddrs...),
        libp2p.ResourceManager(newResourceManager(ctx)),
        // ...
    )
    if err != nil {
        return nil, fmt.Errorf("failed to start libp2p: %w", err)
    }

    go func() {
        <-ctx.Done()
        shutdownCtx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
        defer cancel()
        _ = host.Close() // release streams; rely on shutdownCtx for logging if needed
    }()

    // remaining initialization unchanged
}
```

**Usage impacts:**
- Callers pass the root or phase-specific context.
- Resource manager (optional) can be derived from context for future cancellation hooks.

#### Step A3 – Propagate context into bootstrap helpers

```499:516:main.go
func initPubSub(ctx context.Context, n *config.Node) (*Pubsub.StructGossipPubSub, error) {
    // ...
    gossipPubSub, err := Pubsub.NewGossipPubSubWithContext(ctx, n.Host, pubSubProtocol)
    // ...
}
```

Ensure `initFastSync`, `initSeedNode`, etc. accept context parameters and use `context.WithTimeout(ctx, cfg.Timeouts.FastSync)` where appropriate.

#### Step A4 – CLI command propagation

```24:46:CLI/client.go
func runClientCommand(ctx context.Context, args []string) error {
    dialCtx, cancel := context.WithTimeout(ctx, timeouts.CLI.Dial)
    defer cancel()
    conn, err := grpc.DialContext(dialCtx, target, dialOptions...)
    // ...
}
```

### A.3 Roll-out Checklist
- Update all call sites: `main.go`, `CLI/CLI.go`, tests, and any scripts invoking `node.NewNode()`.
- Validate that each `context.WithTimeout` derives from the propagated parent.
- Remove obsolete signal goroutines and `os.Exit` calls.

---

## Phase B – Context-Aware Database Access

### B.1 Baseline Observations
- `DB_OPs/immuclient.go` functions frequently start with `context.WithTimeout(context.Background(), X)` (example: `Create`, `GetByTxID`, `SyncZkBlocks`). Callers cannot cancel these operations.
- `config/ConnectionPool.go:createConnection` sets `BaseCtx: context.Background()` for pooled clients, making contextual configuration impossible.
- `Block/Server.go`, `gETH/Facade/Service.go`, and CLI command handlers call DB helpers without owning the context or timeout configuration.

### B.2 Implementation Steps

#### Step B1 – Centralize request timeout helper

```320:379:config/ConnectionPool.go
type RequestTimeoutProvider interface {
    RequestTimeout() time.Duration
}

func (cp *ConnectionPool) WithRequestTimeout(parent context.Context) (context.Context, context.CancelFunc) {
    timeout := cp.Config.ConnectionTimeout
    if timeout <= 0 {
        timeout = DefaultDBRequestTimeout // e.g., 8 * time.Second
    }
    return context.WithTimeout(parent, timeout)
}
```

- Store a pointer to the pool in `ImmuClient` (e.g., `Pool *ConnectionPool`) so DB helpers can reuse it.
- Update `ImmuClient.BaseCtx` to the parent context provided during login, not `context.Background()`.

#### Step B2 – Make ImmuDB helper methods context-aware

```226:273:DB_OPs/immuclient.go
func Create(ctx context.Context, pooled *config.PooledConnection, key string, value interface{}) error {
    if ctx == nil {
        return fmt.Errorf("nil context")
    }
    reqCtx, cancel := pooled.Client.Pool.WithRequestTimeout(ctx)
    defer cancel()

    // existing validation unchanged
    if pooled == nil {
        var err error
        pooled, err = GetMainDBConnectionandPutBack(reqCtx)
        // ...
    }

    if err := ensureMainDBSelected(pooled); err != nil {
        return err
    }
    _, err := pooled.Client.DB.Set(reqCtx, &schema.SetRequest{
        KVs: []*schema.KeyValue{{
            Key:   []byte(key),
            Value: encoded,
        }},
    })
    return err
}
```

- Repeat for getters (`Read`, `GetTx`, `Iterate` etc.) ensuring the first parameter is `ctx context.Context`.
- For functions that currently create internal contexts (e.g., `withRetry`), accept `ctx` as parameter and derive child contexts inside the retry loop: `attemptCtx, cancel := context.WithTimeout(ctx, backoff(i))`.

#### Step B3 – Update call sites with real contexts

Example in `Block/Server.go`:

```357:430:Block/Server.go
func processZKBlock(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    storeCtx, cancel := context.WithTimeout(ctx, timeouts.API.ProcessZKBlock)
    defer cancel()

    if err := DB_OPs.Create(storeCtx, nil, blockKey, blockData); err != nil {
        // handle error
    }
}
```

Example in `gETH/Facade/Service.go`:

```104:138:gETH/Facade/Service/Service.go
func (s *Service) BlockByNumber(ctx context.Context, num *big.Int) (*types.Block, error) {
    txCtx, cancel := s.dbPool.WithRequestTimeout(ctx)
    defer cancel()
    rec, err := s.dbClient.GetBlockByNumber(txCtx, num)
    // ...
}
```

#### Step B4 – Tests & tooling
- Update `DB_OPs/Tests/*` to provide explicit contexts: `ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)`.
- Introduce helper `testContext(t *testing.T) (context.Context, context.CancelFunc)` to standardize tests.

### B.3 Roll-out Checklist
- Run gofmt/goimports after signature changes.
- Ensure all `GetMainDBConnectionandPutBack` callers pass the inherited context.
- Monitor pool metrics (`config.DBPoolMetrics`) to confirm connection churn decreases during shutdown or cancellation scenarios.

---

## Validation & Follow-up
- After applying Phase A & B, run integration tests exercising graceful shutdown (Ctrl+C) and DB-heavy workloads.
- Capture metrics before/after (CPU, open FDs, ImmuDB connection count) to validate improvements.
- Proceed with Phase C/D/E only after confirming baseline stability with these primary changes.

