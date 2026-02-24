# Remediation Plan

## 1. Critical Fixes (Immediate Action Required)

### 1.1 Fix Unbounded PubSub Read
**Action**: Modify `Pubsub/Pubsub.go` to enforce a hard limit on message size.
**Proposed Code**:
```go
const MaxMessageSize = 10 * 1024 * 1024 // 10 MB

func readMessage(s network.Stream) ([]byte, error) {
    // ...
    // Use io.LimitReader or check accumulated size
    if len(message) > MaxMessageSize {
        return nil, fmt.Errorf("message size exceeds limit")
    }
    // ...
}
```

### 1.2 Secure Block Propagation
**Action**: Change the order of operations in `messaging/blockPropagation.go`.
**Steps**:
1.  **Validate**: Perform basic structural and signature validation (BLS) *first*.
2.  **Forward**: Only forward valid blocks.
3.  **Process**: Execute transactions and update state.

### 1.3 Implement Consensus Finalization
**Action**: Implement the logic in `handleConsensusResult` to commit approved blocks.
**Steps**:
1.  Check if `status` is approved.
2.  Call `DB_OPs.StoreZKBlock`.
3.  Update the canonical chain head.

## 2. High Priority Security Hardening

### 2.1 Externalize Secrets
**Action**: Remove hardcoded credentials from `config/ImmudbConstants.go`.
**Steps**:
1.  Load credentials *only* from environment variables or a secure configuration file.
2.  Fail startup if secrets are using known unsafe defaults in production mode.

## 3. Medium Priority Improvements

### 3.1 Bind to Localhost by Default
**Action**: Update `StartGRPCServer` to use `127.0.0.1` unless overridden by a flag.

### 3.2 Bounded Caches
**Action**: Replace `map[string]bool` message caches with an LRU cache implementation (e.g., hashicorp/golang-lru) with a fixed size (e.g., 10,000 items).
