# ADR-001: Contract Address Propagation — Sequencer-Initiated, Post-Consensus

**Status:** Proposed  
**Date:** 2026-04-14  
**Deciders:** SmartContract / BlockProcessing / Messaging owners

---

## Context

A contract deployment transaction enters the system like any other transaction: client → mempool → sequencer picks it up, builds a block, runs BFT consensus. Only after consensus succeeds does the block get processed. The contract address is only derivable *after* the EVM executes the constructor bytecode — so it cannot be known at broadcast time.

The two execution paths through `ProcessBlockTransactions` are:

| Who | Call site | `commitToDB` |
|-----|-----------|-------------|
| **Sequencer** | `broadcast.go → BroadcastBlockToEveryNodeWithExtraData → ProcessBlockLocally` | `true` |
| **All other nodes** | `blockPropagation.go → HandleBlockStream → ProcessBlockTransactions` | `true` |

Both paths run EVM execution, so all nodes compute the contract address and store bytecode in PebbleDB. The problem is the **contract registry** (used by `GetContractCode`) and the **ABI** only exist on the deploying node unless explicitly propagated.

### What actually needs propagating

| Data | Available where? |
|------|-----------------|
| Contract address | Every node derives it from EVM (deterministic) |
| Bytecode | Every node stores it in PebbleDB via EVM execution |
| Deployer, TxHash, BlockNumber | Every node can read from the block |
| **ABI** | **Only the deploying node's registry** (submitted at `DeployContract` time) |
| **Contract registry entry** | **Only the deploying node**, unless propagated |

Without propagation, `GetContractCode` returns empty on non-deploying nodes even though `CallContract` works fine (bytecode is present). The registry/ABI layer is the gap.

---

## Decision

Modify `ProcessBlockTransactions` to return the list of contracts deployed in a block. In `BroadcastBlockToEveryNodeWithExtraData` (sequencer-only path), after `ProcessBlockLocally` completes successfully, gossip a `ContractMessage` for each deployed contract. Receiving nodes write the metadata to their local contract registry. The pattern mirrors `DIDPropagation.go` exactly.

---

## Call Flow

```
Client
  └─► DeployContract gRPC (handlers.go)
        └─► tx → mempool → block
              └─► BFT consensus (sequencer)
                    └─► ConsensusResult{BlockAccepted: true}
                          └─► BroadcastBlockToEveryNodeWithExtraData (broadcast.go)
                                ├─► send block to peers (with BLS proofs)
                                └─► ProcessBlockLocally (sequencer only)
                                      ├─► ProcessBlockTransactions → [ContractDeploymentInfo, ...]
                                      └─► go PropagateContractDeployments  ◄── NEW (sequencer only)
                                            └─► ContractMessage gossip per deployed contract
                                                  └─► HandleContractStream on each peer
                                                        └─► SmartContract.RegisterContractFromGossip
                                                              └─► local registry + ABI stored
```

Other nodes process the block via `HandleBlockStream → ProcessBlockTransactions` — their EVM writes bytecode to PebbleDB (so `CallContract` works), and `HandleContractStream` fills in the registry/ABI layer on top. No double-propagation because only the sequencer path calls `PropagateContractDeployments`.

---

## Implementation Plan

### Step 1 — Return deployment results from `ProcessBlockTransactions`

**`messaging/BlockProcessing/Processing.go`**

Add a new result type and change `ProcessBlockTransactions` + `processTransaction` to collect and return deployed contract info:

```go
// ContractDeploymentInfo is returned per deployed contract in a processed block.
type ContractDeploymentInfo struct {
    ContractAddress common.Address
    Deployer        common.Address
    TxHash          common.Hash
    BlockNumber     uint64
    GasUsed         uint64
}

// ProcessBlockTransactions processes all transactions in a block.
// Returns a slice of ContractDeploymentInfo for every successfully deployed contract,
// alongside any processing error.
func ProcessBlockTransactions(
    block *config.ZKBlock,
    accountsClient *config.PooledConnection,
    commitToDB bool,
) ([]ContractDeploymentInfo, error)
```

Inside `processTransaction`, when `ProcessContractDeployment` succeeds and `commitToDB=true`, append to a `deployments` slice and return it up the call chain.

The public API wrapper `ProcessSingleTransaction` in `public_api.go` (used by buddy nodes for verification) does not need to change.

---

### Step 2 — Thread results through `ProcessBlockLocally`

**`messaging/broadcast.go`**

```go
// ProcessBlockLocally processes a consensus-verified block on the sequencer.
// Returns any contracts deployed in the block so the caller can propagate them.
func ProcessBlockLocally(
    block *config.ZKBlock,
    blsResults []BLS_Signer.BLSresponse,
) ([]BlockProcessing.ContractDeploymentInfo, error)
```

---

### Step 3 — Propagate from the sequencer call site

**`messaging/broadcast.go` — inside `BroadcastBlockToEveryNodeWithExtraData`**

```go
if result {
    if len(bls) > 0 {
        deployments, err := ProcessBlockLocally(block, bls)
        if err != nil {
            return err
        }
        // Sequencer-only: gossip each deployed contract to all peers.
        // Fire-and-forget — a propagation failure must not roll back the block.
        if len(deployments) > 0 {
            go PropagateContractDeployments(h, block, deployments)
        }
        return nil
    }
    log.Warn()...
}
```

All other `ProcessBlockTransactions` call sites (e.g. `HandleBlockStream`) receive the new return value and simply discard the deployments slice.

---

### Step 4 — Create `messaging/ContractPropagation.go`

Mirrors `DIDPropagation.go` structurally. Key types and functions:

```go
// ContractMessage is the gossip payload for a confirmed contract deployment.
type ContractMessage struct {
    ID              string         `json:"id"`
    Sender          string         `json:"sender"`
    Timestamp       int64          `json:"timestamp"`
    Type            string         `json:"type"`         // "contract_deployed"
    Hops            int            `json:"hops"`
    ContractAddress common.Address `json:"contract_address"`
    Deployer        common.Address `json:"deployer"`
    TxHash          common.Hash    `json:"tx_hash"`
    BlockNumber     uint64         `json:"block_number"`
    GasUsed         uint64         `json:"gas_used"`
    // ABI is populated if the sequencer's registry has it (optional — receivers
    // must handle an empty string gracefully).
    ABI             string         `json:"abi,omitempty"`
}

// PropagateContractDeployments builds a ContractMessage for each deployment
// and gossips it to all connected peers.  Called only on the sequencer.
func PropagateContractDeployments(
    h host.Host,
    block *config.ZKBlock,
    deployments []BlockProcessing.ContractDeploymentInfo,
) { ... }

// HandleContractStream is the libp2p stream handler registered on all nodes.
func HandleContractStream(stream network.Stream) { ... }

// InitContractPropagation initialises the Bloom filter and GRO pools.
func InitContractPropagation() error { ... }
```

The ABI field is populated at propagation time by querying the sequencer's local contract registry. If the registry has it (because this node processed the original `DeployContract` gRPC call), it is included. If not, the field is empty — receivers still get the address and metadata.

**Receive path in `HandleContractStream`:**
1. Deduplication via Bloom filter (same as DID pattern)
2. Call `SmartContract.RegisterContractFromGossip(msg)` to write to local registry
3. Hop-limited forward to other peers (respects `config.MaxContractHops`)

---

### Step 5 — Expose registry write from `SmartContract` package

**`SmartContract/processor.go`**

```go
// RegisterContractFromGossip stores contract metadata received via gossip into
// the local registry and PebbleDB.  Idempotent — safe to call if the contract
// already exists locally (EVM execution during block processing may have already
// stored the bytecode; this call fills in the registry/ABI layer on top).
func RegisterContractFromGossip(
    ctx context.Context,
    addr    common.Address,
    deployer common.Address,
    txHash  common.Hash,
    blockNumber uint64,
    abi    string,
) error { ... }
```

This requires `SmartContract` to hold a `sharedRegistry` reference, wired in `server_integration.go` similarly to the existing `sharedKVStore` singleton.

---

### Step 6 — Add constants

**`config/constants.go`**
```go
ContractPropagationProtocol protocol.ID = "/gossipnode/contract/1.0.0"
MaxContractHops             int         = 7
```

**`config/GRO/constants.go`**
```go
ContractStoreThread             = "thread:contract:store"
ContractPropagationThread       = "thread:contract:propagation"
ContractForwardThread           = "thread:contract:forward"
ContractPropagationStreamThread = "thread:contract:propagation:stream"
ContractPropagationLocal        = "local:contract:propagation"
ContractForwardWG               = "wg:contract:forward"
```

---

### Step 7 — Register stream handler in `main.go`

Next to the DID handler (line ~998):

```go
n.Host.SetStreamHandler(config.ContractPropagationProtocol, messaging.HandleContractStream)

if err := messaging.InitContractPropagation(); err != nil {
    log.Error().Err(err).Msg("Failed to initialize contract propagation")
}
```

---

## Consequences

**What becomes easier:**
- `GetContractCode` returns the ABI on any node, not just the deploying one
- Late-joining nodes that missed the block catch up when the first `ContractMessage` reaches them
- The pattern is identical to DID propagation — the team already knows how to maintain it
- No new dependencies required

**What becomes harder:**
- `ProcessBlockTransactions` now returns a richer type — all call sites must be updated (non-sequencer paths simply discard the slice)
- ABI is best-effort in the gossip message; callers must handle the empty-ABI case

**What to revisit (Phase 2):**
- If the sequencer goes offline before propagation completes, peers won't receive the `ContractMessage` until a re-sync or pull-on-demand mechanism is added. A pull-on-demand path — where a node that finds no registry entry for a known contract address queries its peers directly — would make the system resilient to mid-propagation failures and is a natural follow-up.
- Consider compressing the ABI payload (gzip + base64) once registry usage grows, since ABI JSON strings can be several KB for complex contracts.

---

## Action Items

- [ ] Add `ContractDeploymentInfo` struct to `messaging/BlockProcessing/Processing.go`; change `ProcessBlockTransactions` + `processTransaction` return type
- [ ] Update `broadcast.go`'s `ProcessBlockLocally` to return `[]ContractDeploymentInfo`; propagate deployments after successful local processing
- [ ] Update `HandleBlockStream` and any other `ProcessBlockTransactions` call sites to handle the new return value (discard the slice)
- [ ] Create `messaging/ContractPropagation.go` — `ContractMessage`, `PropagateContractDeployments`, `HandleContractStream`, `InitContractPropagation`
- [ ] Add `sharedRegistry` singleton + `RegisterContractFromGossip` to `SmartContract/processor.go`; wire up in `server_integration.go`
- [ ] Add `ContractPropagationProtocol` + `MaxContractHops` to `config/constants.go`
- [ ] Add GRO thread/WG constants to `config/GRO/constants.go`
- [ ] Register `HandleContractStream` + call `InitContractPropagation` in `main.go`
- [ ] Smoke test: deploy via Node A (sequencer), verify `GetContractCode` returns ABI on Node B without re-deployment
- [ ] **[Phase 2]** Design and implement pull-on-demand fallback for nodes that miss the propagation window (sequencer offline, partition, etc.)
