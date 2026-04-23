# JMDN Port Reference & Security Guide

This document describes every port used by a JMDN node, its security posture, and the recommended firewall strategy for cloud deployments.

---

## Quick Reference

| # | Service | Port | Protocol | Exposure | Notes |
|---|---|---|---|---|---|
| 1 | Explorer API | `8090` | HTTP | Internal / Trusted IPs | Auth-protected endpoints |
| 2 | Facade RPC | `8545` / `8546` | JSON-RPC / WS | Public (RPC nodes) | Ethereum-compatible |
| 3 | P2P Gossip | `15000` | TCP + UDP | **Public** | Required for node participation |
| 4 | Yggdrasil | `15001` | TCP + UDP | **Public** | Mesh overlay network |
| 5 | DID Service | `15052` | gRPC | Public (DID resolvers) | Identity resolution |
| 6 | CLI Admin | `15053` | gRPC | **Localhost only** | Full admin control — never expose |
| 7 | BlockGen API | `15050` | HTTP | Localhost (Sequencers) | Disable on generic nodes |
| 8 | BlockGRPC | `15055` | gRPC | Internal (Validators) | Disable on passive nodes |
| 9 | Metrics | `8081` | HTTP | Localhost / Internal | Prometheus `/metrics` |
| 10 | Profiler | `6060` | HTTP | **Disabled in production** | pprof — information leak risk |

---

## Port Details

### 1. Explorer API
- **Port**: `8090` (configurable)
- **Protocol**: HTTP (Gin)
- **Code Reference**: `explorer/api.go`
- **Endpoints**:
  - `GET /api/v1/node/version` — **Public**. Health check, returns node version.
  - `POST /api/auth/token` — **Public**. Issues JWT (requires API key).
  - `GET /api/block/:id` — **Protected**. Block details (requires JWT).
  - `GET /api/transactions/...` — **Protected**. Transaction history (requires JWT).
- **Recommendation**: Bind to `0.0.0.0`. Restrict access to internal network or specific monitoring IPs via firewall. Sensitive endpoints are authenticated, but attack surface should still be minimised.

---

### 2. Facade (JSON-RPC)
- **Port**: `8545` (HTTP), `8546` (WebSocket)
- **Protocol**: JSON-RPC 2.0 (Ethereum-compatible)
- **Code Reference**: `gETH/Facade/rpc/handlers.go`
- **Endpoints**: `eth_blockNumber`, `eth_getBalance`, `eth_chainId`, `eth_sendRawTransaction`, `eth_call`
- **Note**: `eth_call` is often disabled for compliance.
- **Recommendation**: Public (`0.0.0.0`) on dedicated RPC nodes. Restrict to specific dApps or wallets if not acting as a public RPC.

---

### 3. P2P Gossip (LibP2P)
- **Port**: `15000` (TCP + UDP)
- **Protocol**: LibP2P
- **Purpose**: Core node communication — GossipSub, block propagation, peer discovery.
- **Recommendation**: **Must be public** (`0.0.0.0/0`). A node that cannot receive inbound P2P traffic cannot participate in the network.

---

### 4. Yggdrasil (Mesh Network)
- **Port**: `15001` (TCP + UDP)
- **Protocol**: Yggdrasil IPv6 overlay
- **Purpose**: Direct mesh messaging and peer discovery over an encrypted overlay network.
- **Recommendation**: **Must be public**. Required for Yggdrasil-based messaging between nodes.

---

### 5. DID Service
- **Port**: `15052` (gRPC)
- **Protocol**: gRPC
- **Code Reference**: `DID/DID.go`
- **Endpoints**: `RegisterDID`, `GetDID`, `ListDIDs`
- **Recommendation**: Public (`0.0.0.0`) on nodes acting as DID resolvers. Can be disabled (`0`) on non-resolver nodes.

---

### 6. CLI Admin Interface
- **Port**: `15053` (gRPC)
- **Protocol**: gRPC
- **Code Reference**: `CLI/GRPC_Server.go`
- **Endpoints**: `AddPeer`, `StopNode`, `FastSync`
- **Purpose**: Full administrative control of the node.
- **Recommendation**: ⚠️ **Strictly localhost (`127.0.0.1`) only.** Never expose to the network, even internally. Use an SSH tunnel for remote administration.

---

### 7. BlockGen API (Sequencer)
- **Port**: `15050` (HTTP)
- **Protocol**: HTTP
- **Code Reference**: `Block/server.go`
- **Endpoints**: `POST /api/process-block` — triggers consensus.
- **Recommendation**: Disabled (`0`) on generic nodes. Localhost only on Sequencer nodes.

---

### 8. BlockGRPC (Validator P2P)
- **Port**: `15055` (gRPC)
- **Purpose**: Validator-to-validator block propagation.
- **Recommendation**: Disabled (`0`) on passive or observer nodes. Internal-only on validator clusters.

---

### 9. Metrics (Prometheus)
- **Port**: `8081`
- **Protocol**: HTTP (`/metrics`)
- **Purpose**: Observability — scraped by Prometheus.
- **Recommendation**: Bind to localhost (`127.0.0.1`) or internal network only. Never expose to the public internet.

---

### 10. Profiler (pprof)
- **Port**: `6060`
- **Protocol**: HTTP (`/debug/pprof`)
- **Purpose**: CPU and heap profiling for debugging.
- **Recommendation**: **Disable in production.** Exposes memory and goroutine state. Significant information leak and performance impact if left open.

---

## Firewall & Cloud Security

### General Strategy (GCP / AWS / Azure)

1. **Bind services to `0.0.0.0`** — services listen on all interfaces.
2. **Control access at the firewall layer** — cloud firewall rules determine what reaches the node.
3. **Never rely on application-level binding alone** for sensitive ports like CLI (`15053`).

### Recommended Firewall Rules

| Rule | Direction | Source | Port(s) | Action | Applies To |
|---|---|---|---|---|---|
| `allow-p2p` | Ingress | `0.0.0.0/0` | `tcp:15000`, `udp:15000` | Allow | All nodes |
| `allow-yggdrasil` | Ingress | `0.0.0.0/0` | `tcp:15001`, `udp:15001` | Allow | All nodes |
| `allow-rpc-public` | Ingress | `0.0.0.0/0` | `tcp:8545`, `tcp:8546` | Allow | RPC nodes only |
| `allow-internal-all` | Ingress | `10.0.0.0/8` (VPC) | `tcp:0–65535` | Allow | All nodes |
| `allow-monitoring` | Ingress | `<monitoring-IP>/32` | `tcp:8090`, `tcp:8081` | Allow | All nodes |
| `block-sensitive` | Ingress | `0.0.0.0/0` | `tcp:15053`, `tcp:15050` | **Deny** | All nodes |

### Internal vs External Traffic

- **Same VPC**: Always use **internal IPs** (`10.x.x.x`) for inter-node and monitoring traffic. This avoids egress charges and keeps traffic behind the firewall.
- **External access to protected ports** (e.g., `8090`): Whitelist the specific source IP in the firewall. Do not open to `0.0.0.0/0`.
- **CLI port `15053`**: Reach it remotely only via SSH tunnel — **never open this port in any firewall rule**.

---

*Back to [GETTING_STARTED.md](./GETTING_STARTED.md) · [README.md](./README.md)*
