# Technical Findings

## 1. Critical Severity Issues

### 1.1 Unbounded Read in PubSub (DoS Vector)
- **Component**: `Pubsub/Pubsub.go`
- **Function**: `readMessage`
- **Description**: The `readMessage` function reads from the network stream byte-by-byte until a delimiter is found, without enforcing a maximum message size limit.
- **Impact**: An attacker can send a continuous stream of bytes without a delimiter, causing the node to buffer data until it runs out of memory (OOM), crashing the node.
- **Location**: `Pubsub/Pubsub.go` lines 188-206.

### 1.2 Block Propagation Amplification
- **Component**: `messaging/blockPropagation.go`
- **Function**: `HandleBlockStream`
- **Description**: The node forwards received ZK blocks to peers *before* validating them or their BLS signatures.
- **Impact**: Malicious nodes can flood the network with invalid blocks. Innocent nodes will participate in the attack by forwarding these invalid blocks, consuming bandwidth and processing power across the entire network.
- **Location**: `messaging/blockPropagation.go` lines 254-268 ("STEP 1: FORWARD BLOCK FIRST").

### 1.3 Missing Consensus Result Implementation
- **Component**: `messaging/blockPropagation.go`
- **Function**: `handleConsensusResult`
- **Description**: The function responsible for processing the final result of the consensus voting process contains only a `TODO` comment and no logic.
- **Impact**: Even if consensus is reached, the node may fail to act on it (e.g., committing the block to the chain), effectively halting the chain's progress.
- **Location**: `messaging/blockPropagation.go` lines 797-800.

## 2. High Severity Issues

### 2.1 Hardcoded Database Credentials
- **Component**: `config/ImmudbConstants.go`
- **Description**: The default credentials for ImmuDB (`immudb`/`immudb`) are hardcoded in the source code.
- **Impact**: If a node operator fails to override these defaults (and the instructions to do so are not enforced), an attacker with local or network access to the database port can take full control of the node's state.
- **Location**: `config/ImmudbConstants.go` lines 28-31.

## 3. Medium Severity Issues

### 3.1 Unrestricted gRPC Listening
- **Component**: `gETH/Server.go`, `CLI/GRPC_Server.go`
- **Description**: gRPC servers listen on `0.0.0.0` (all interfaces) by default.
- **Impact**: Exposes administrative and block interfaces to the public network if the host firewall is not configured correctly.
- **Remediation**: Bind to `127.0.0.1` by default or allow configuration of the bind interface.

### 3.2 Unbounded Message Cache Growth
- **Component**: `Pubsub/Pubsub.go`
- **Description**: `gps.MessageCache` (a map) grows indefinitely as messages are received.
- **Impact**: Long-running nodes will eventually exhaust memory.
- **Remediation**: Implement a bounded cache (LRU) or TTL-based cleanup.

## 4. Low Severity / Code Quality

### 4.1 Global State Usage
- **Description**: Extensive use of global variables (`MainAM`, `MainLM`, `fastSyncer`) in `main.go`.
- **Impact**: Makes testing difficult and introduces potential race conditions during startup/shutdown.

### 4.2 Inconsistent Error Handling
- **Description**: Some functions log fatal errors (exiting the process) while others return errors to the caller.
