# Attack Surface Map

## 1. External Network Interfaces

### 1.1 TCP/UDP Ports
- **Libp2p Listen Port**: Configurable or random. Accessible to public internet.
    - *Protocol*: Encrypted transport (Noise), Yamux/Quic.
    - *Attack Vectors*: DoS, handshake exhaustion, Eclipse attacks.
- **Yggdrasil Port**: Specific to overlay integration.
    - *Attack Vectors*: Routing table poisoning.

### 1.2 RPC Services (gRPC/HTTP)
- **gETH Facade (Port 15054 / 8545)**
    - *Exposure*: Localhost by default, but commonly exposed for wallets.
    - *Services*: `GetBlock*`, `SendRawTransaction`.
    - *Risks*: Unauthenticated access to node functions.
- **CLI Management (Port 15053)**
    - *Exposure*: Localhost.
    - *capabilities*: `addpeer`, `removepeer`, `stop`, `broadcast`.
    - *Risks*: If exposed, allows full node takeover.
- **Explorer API (HTTP)**
    - *Exposure*: Configurable.
    - *Risks*: XSS (via rendered block data), unchecked query params.

## 2. Input Vectors

### 2.1 Message Handling (P2P)
- **GossipSub Listener**:
    - `HandleSubscriptionRequest`
    - `ProcessVerificationMessage`
    - *Risk*: Message deserialization bombs (JSON/Protobuf), logic bugs in handlers.
- **Direct Messaging**:
    - `AskForSubscription` flow.

### 2.2 Transaction Submission
- **Path**: `SendRawTransaction` -> `Mempool`.
- *Validation*: 3-layer security check (Signature, Balance, Nonce).
- *Risk*: Replay attacks (if nonce check flawed), malformed RLP data.

## 3. Dependency Surface
- **ImmuDB**: Critical state dependency.
- **Libp2p**: Networking stack.
- **Go-Ethereum**: Crypto and common types.

## 4. High-Risk Components (Phase 1)
- **Sequencer/Communication.go**: Complex state management for subscriptions. `AskForSubscription` involves dynamic channel creation.
- **Security/Security.go**: The gatekeeper for all state changes.
