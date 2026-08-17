# Compiler-ready guide: P1c + EVM-29 + EVM-30/31

You run these where `CGO_ENABLED=1 go build` works (`../ThebeDB` checked out). go-ethereum is
**v1.17.0** (go.mod) — the gas APIs are version-sensitive, so each step includes a `go doc` command
to pin the exact signature before you write the call. Do them in this order; each is its own commit
with its own gate. All of it stays behind `cfg.Contracts.Enabled` (default false) → non-breaking.

---

## EVM-29 — run Shanghai, not London (one field, drop the hack)

**Root cause (verified):** `vm.NewEVM` derives the active fork from
`chainConfig.Rules(blockNumber, isMerge, time)` where **`isMerge = (blockCtx.Random != nil)`**.
Your block context never sets `Random`, so `isMerge=false`; and go-ethereum gates
`IsShanghai = isMerge && ShanghaiTime!=nil && time>=*ShanghaiTime`. So even though
`config.go:36` sets `ShanghaiTime=0`, Shanghai is **off** → London instruction table → no PUSH0.
That is why someone bolted PUSH0 on via `NewVMConfig().ExtraEips=[3855]` (`config.go:44`) — a
symptom patch. `BaseFee/Difficulty=0` are already post-merge-correct.

**Fix — in the deterministic path only (`SmartContract/internal/evm/evm_deterministic.go`):**

1. Add a `Random` to `DetBlockContext` and set it on the `vm.BlockContext`, non-nil and
   deterministic. Use a value all nodes agree on — the block hash is perfect:
   ```go
   type DetBlockContext struct {
       ... // existing fields
       Random common.Hash // PREVRANDAO; set to the block hash (deterministic, fleet-uniform)
   }
   // in both DeployContractWithContext and ExecuteContractWithContext, in the vm.BlockContext literal:
   Random: func() *common.Hash { r := bctx.Random; return &r }(),  // non-nil => isMerge => Shanghai
   ```
   (A pointer to a zero hash also works and still enables Shanghai; the block hash is preferable so
   `PREVRANDAO`/`DIFFICULTY` opcode reads are meaningful and fleet-uniform.)

2. Verify the fork is actually Shanghai, then you can drop the `ExtraEips:[3855]` hack:
   ```
   go doc github.com/ethereum/go-ethereum/params.ChainConfig.Rules
   # write a tiny probe (or a _test.go in package evm):
   #   cfg := NewChainConfig(7000700)
   #   rules := cfg.Rules(big.NewInt(1), true /*isMerge*/, 1 /*time*/)
   #   assert rules.IsShanghai == true && rules.IsMerge == true
   # then run a PUSH0 bytecode through DeployContractWithContext and confirm it executes
   # WITHOUT ExtraEips[3855]; if green, remove 3855 from NewVMConfig (config.go:44).
   ```
   Keep `ExtraEips` empty only after the probe passes — otherwise leave it and note it.

**Gate:** `CGO_ENABLED=1 go test ./SmartContract/internal/evm/ -run Fork` (your new probe) green.

---

## EVM-30 / EVM-31 — charge intrinsic gas + credit refunds

**Root cause:** both `*WithContext` (and the originals) compute `GasUsed = gasLimit - leftOverGas`
straight off `evm.Create`/`evm.Call`. That number omits the **intrinsic gas** (21000 base, + 4/16
per calldata byte, + 32000 for create, + initcode words for Shanghai) that `core.StateTransition`
normally deducts before execution, and it never credits the **SSTORE refund**
(`stateDB.GetRefund()`, capped). So reported gas is systematically too low and spec-divergent.

**Pin the API first (v1.17.0 — it changed with EIP-7702, do not guess):**
```
go doc github.com/ethereum/go-ethereum/core.IntrinsicGas
go doc github.com/ethereum/go-ethereum/params.TxGas          # 21000 base
go doc github.com/ethereum/go-ethereum/core/vm.StateDB.GetRefund
```
In v1.17.0 `IntrinsicGas` looks like:
`IntrinsicGas(data []byte, accessList types.AccessList, authList []types.SetCodeAuthorization, isContractCreation, isHomestead, isEIP2028, isEIP3860 bool) (uint64, error)`
— confirm the exact arg list from `go doc`, then:

**Option A (minimal, in the two `*WithContext` methods):**
```go
// BEFORE calling Create/Call:
intrinsic, err := core.IntrinsicGas(code /*or input*/, nil, nil,
    isCreate /*true for deploy*/, true /*homestead*/, true /*eip2028*/, true /*eip3860, Shanghai*/)
if err != nil { return &ExecutionResult{Error: err}, err }
if gasLimit < intrinsic { return insufficient-intrinsic-gas error }  // spec: reject, don't apply
// give the EVM only the post-intrinsic gas:
execGas := gasLimit - intrinsic
ret, ..., leftOverGas, err := evmInstance.Create(caller, code, execGas, value256)
// refund (Shanghai cap = gasUsedSoFar/5):
used := (gasLimit - leftOverGas)               // includes intrinsic now
refund := state.GetRefund()
if cap := used/5; refund > cap { refund = cap }
used -= refund
result.GasUsed = used
```
**Option B (correct, more work):** route the tx through `core.ApplyMessage` /
`core.NewStateTransition` with a `core.Message` and a `core.GasPool` — this charges intrinsic gas,
runs, credits the refund, and returns `ExecutionResult.UsedGas` for you. Prefer B if you can spare
the refactor; A is a faithful stopgap that closes the divergence.
```
go doc github.com/ethereum/go-ethereum/core.ApplyMessage
go doc github.com/ethereum/go-ethereum/core.Message
```

**Gate — a probe that proves the numbers now match go-ethereum** (the audit's own EVM-30/31 probe
shape): a bare `BALANCE`/transfer tx must report `>= 21000` gas; an SSTORE-clear then re-set must
show the refund credited. `CGO_ENABLED=1 go test ./SmartContract/internal/evm/ -run Gas`.

---

## P1c — the execbridge.ContractExecutor over the EVM

Create **`SmartContract/evmbridge.go`** (package `SmartContract` — it already imports
`internal/evm` and `DB_OPs/contractDB`, so no cycle):

```go
package SmartContract

import (
    "context"
    "math/big"

    "github.com/ethereum/go-ethereum/common"

    contractDB "gossipnode/DB_OPs/contractDB"
    "gossipnode/SmartContract/internal/evm"
    "gossipnode/config"
    "gossipnode/execbridge"
)

type evmExecutor struct{ chainID int }

// NewEVMExecutorBridge is what main.go registers when cfg.Contracts.Enabled.
func NewEVMExecutorBridge(chainID int) execbridge.ContractExecutor { return &evmExecutor{chainID} }

// IsContractTx: deployment (To==nil) or an EVM tx type. Pure + deterministic —
// no state read (a call to a non-contract just runs empty code, still correct).
func (x *evmExecutor) IsContractTx(tx *config.Transaction) bool {
    return tx.To == nil || tx.Type == 2
}

func (x *evmExecutor) ExecuteTx(ctx context.Context, tx *config.Transaction, b execbridge.BlockExecContext) (*execbridge.ExecResult, error) {
    // Per-tx StateDB from the shared singletons (set in server_integration).
    state, err := contractDB.InitializeStateDB()
    if err != nil { return &execbridge.ExecResult{Handled: false}, err }

    det := evm.DetBlockContext{
        BlockNumber: b.BlockNumber,
        Time:        b.Time,
        Coinbase:    b.Coinbase,
        GasLimit:    b.GasLimit,
        Random:      b.BlockHash,   // after the EVM-29 field is added
        // GetHash: supply a deterministic closure over stored blocks in P3; nil is fine for P1.
    }
    ex := evm.NewEVMExecutor(x.chainID)

    var er *evm.ExecutionResult
    res := &execbridge.ExecResult{Handled: true, ContractAddress: common.Address{}}
    if tx.To == nil {
        er, err = ex.DeployContractWithContext(state, det, *tx.From, tx.Data, tx.Value, tx.GasLimit)
        if er != nil { res.ContractAddress = er.ContractAddr }
    } else {
        er, err = ex.ExecuteContractWithContext(state, det, *tx.From, *tx.To, tx.Data, tx.Value, tx.GasLimit)
    }
    if er != nil { res.GasUsed = er.GasUsed; res.Success = er.Error == nil }
    res.Err = err

    // Deterministic commit + digest (P1a). This returns the state root.
    root, cErr := state.CommitToDB(false)
    if cErr != nil { res.Err = cErr; res.Success = false; return res, cErr }
    res.StateRoot = root

    // Absolute balances for the caller to fold through the ONE account path (P2).
    res.BalanceChanges = map[common.Address]*big.Int{}
    for addr, bal := range state.GetBalanceChanges() {  // map[Address]*uint256.Int
        res.BalanceChanges[addr] = bal.ToBig()
    }
    return res, res.Err
}
```

**Register in `main.go`** where the SmartContract server is started (near `StartIntegratedServer`,
`main.go:1584`), gated by the flag:
```go
if cfg.Contracts.Enabled {
    execbridge.SetExecutor(SmartContract.NewEVMExecutorBridge(cfg.Network.ChainID))
    log.Info().Msg("[contracts] EVM executor registered (contracts.enabled=true)")
} else {
    log.Info().Msg("[contracts] execution disabled (default) — contract txs use the transfer path")
}
```
This must run AFTER `StartIntegratedServer` has called `SetSharedStateRepository` /
`SetSharedDIDClient`, so `InitializeStateDB()` resolves the shared singletons.

**Gates:**
```
CGO_ENABLED=1 go build ./SmartContract/ ./execbridge/ .
CGO_ENABLED=1 go vet ./SmartContract/...
CGO_ENABLED=1 go test ./SmartContract/... ./DB_OPs/contractDB/...
```
With the flag OFF (default), `execbridge.Get()` is still the no-op → the apply path is unchanged →
you can merge P1c safely. It only becomes live at P2 (when `processTransaction` calls
`execbridge.Get().IsContractTx/ExecuteTx`), which is the step that needs the 2-node determinism gate.

---

## Order + gates summary
1. EVM-29 (fork) → `-run Fork` green, ExtraEips dropped.
2. EVM-30/31 (gas) → `-run Gas` green (>=21000; refund credited).
3. P1c (bridge + registration) → build/vet/test green, flag OFF = no behavior change.
4. THEN P2 (invoke from apply path) → 2-node determinism gate before flag ON.

If you paste the `CGO_ENABLED=1 go build ./SmartContract/... ./DB_OPs/contractDB/...` output (esp. the
`go doc IntrinsicGas` signature and any errors), I'll turn these sketches into exact diffs.
