# Threat Model and Trust Assumptions

## 1. System Overview
The JMDT Node is a Phase-1 Layer 2 blockchain node capable of peer-to-peer networking, reduced-state synchronization (FastSync), and consensus participation (Sequencer/AVC). 

## 2. Trust Boundaries

### 2.1 Network Boundary (Untrusted)
- **Libp2p Swarm**: Any connection incoming from the public internet is treated as untrusted.
- **Yggdrasil Overlay**: End-to-end encrypted paths are trusted for privacy but not for payload correctness.
- **GossipSub Topics**: Messages on public topics (`/jmdt/block/1.0.0`, etc.) are untrusted and must be validated.

### 2.2 Local Interface Boundary (Semi-Trusted)
- **RPC Ports**:
    - CLI (15053): Assumes operator control. No auth apparent in code.
    - gETH (15054): Assumes local or proxied access. 
- **Filesystem**: `data/`, `.immudb_state/`, and `config/` are assumed to be writable only by the node operator.

### 2.3 Execution Boundary (Trusted)
- **ImmuDB**: Data read from the local verified state is trusted.
- **Memory State**: In-memory structs (PeerList, Mempool) are trusted once validated.

## 3. Adversarial Assumptions

- **Hostile Peers**: We assume up to `f` peers in a `3f+1` consensus set may be malicious (Byzantine).
- **Sybil Attacks**: An attacker may spin up unlimited node identities to flood gossip channels.
- **Network Asynchrony**: Messages may be delayed, reordered, or dropped.
- **Malformed Inputs**: RPC and P2P inputs may be fuzzed or oversized.

## 4. Asset Analysis

| Asset | Value | Impact of Compromise |
|-------|-------|----------------------|
| **Private Keys** | Critical | Node identity theft, unauthorized signing. |
| **Consensus Vote** | High | Chain forks, invalid state transitions. |
| **ImmuDB State** | High | Data corruption, loss of history (if no backup). |
| **P2P Bandwidth** | Medium | Denial of Service, inability to sync. |

## 5. Active vs Planned Features (Phase 1)
- **Active**: Libp2p/Yggdrasil connectivity, Basic Sequencer (AskForSubscription), FastSync (Hashmap diff), gETH facade.
- **Planned/Incomplete**: Full BFT robustness (currently relying on simple counting), Advanced slashing conditions.
