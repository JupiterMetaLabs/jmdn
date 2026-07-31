package FastsyncV2

// Reconciliation delta computation moved.
//
// Balance deltas are no longer computed here (nor shipped as computed
// values). Reconciliation hands BLOCK REFERENCES to the account sync queue
// (see reconcile_local.go); the apply side — DB_OPs.ApplyBlockRecon /
// DB_OPs.ComputeBlockDeltas — recomputes each block's deltas from the locally
// stored block at apply time, filters tx_processed markers under the global
// state-apply lock, and commits balances + markers in one ExecAll.
//
// Rationale: computing balances at enqueue time bakes in the base they were
// read against; by apply time that base can have moved (live execution runs
// in the same process). Recomputing at apply time, serialized with the live
// executor, makes reconciliation writes commutative and exactly-once by
// construction. Fee arithmetic remains config.GasFee / config.EffectiveGasPrice /
// config.SplitFee — the single source of truth shared with live execution
// (see config/gasfee.go); do not fork that logic.
