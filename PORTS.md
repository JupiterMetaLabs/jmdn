# JMDN Port Reference & Security Guide

This document describes every port used by a JMDN node, its security posture, and the recommended firewall strategy for cloud deployments.

---

## Quick Reference

| # | Service | Port | Protocol | Exposure | Notes |
|---|---|---|---|---|---|
| 1 | Explorer API | `8090` | HTTP | Internal / Trusted IPs | Auth-protected endpoints |
| 2 | Facade RPC | `8545` / `8546` | JSON-RPC / WS | Public (RPC nodes) | Ethereum-compatible |
| 3 | P2P Gossip | `15000` | TCP + UDP | **Public** | Required for node participation |
| 4 | Yggdrasil direct-msg | `15001` | TCP only | Public on bare metal; **not exposed in the Docker image** (see note below) | JMDN's own messaging listener |
| 5 | DID Service | `15052` | gRPC | Public on bare metal (deliberate choice); **not exposed in the Docker image** (see note below) | Identity resolution + unauthenticated registration |
| 6 | CLI Admin | `15053` | gRPC | **Localhost only** | Full admin control — never expose |
| 7 | BlockGen API | `15050` | HTTP | Localhost (internal role only) | Disable on generic nodes; **not exposed in the Docker image** |
| 8 | BlockGRPC | `15055` | gRPC | Internal (Validators) | Disable on passive nodes; **not exposed in the Docker image** |
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
- **Additional endpoints** (when SyncMonitor is enabled): `GET /sync/status`, `POST /sync/reconcile`
- **Recommendation**: Public (`0.0.0.0`) on dedicated RPC nodes. Restrict to specific dApps or wallets if not acting as a public RPC.

---

### 3. P2P Gossip (LibP2P)
- **Port**: `15000` (TCP + UDP)
- **Protocol**: LibP2P
- **Purpose**: Core node communication — GossipSub, block propagation, peer discovery.
- **Recommendation**: **Must be public** (`0.0.0.0/0`). A node that cannot receive inbound P2P traffic cannot participate in the network.

---

### 4. Yggdrasil direct-messaging listener
- **Port**: `15001` (TCP only — `net.Listen("tcp6", ":15001")`, no UDP listener exists on this port)
- **Code Reference**: `messaging/directMSG/directMSG.go`
- **Purpose**: JMDN's own direct node-to-node messaging service, addressed via the peer's Yggdrasil-assigned IPv6 address. This is a JMDN listener, not the Yggdrasil mesh-peering protocol itself.
- **Bare metal**: must be public to use this feature — confirmed the OS `yggdrasil` package's own postinst enables and starts its systemd unit automatically on install, so this works out of the box there.
- **Docker: not exposed, on purpose, for two independent reasons — confirmed live on a running mainnet container:**
  1. **The daemon isn't running.** `docker top` showed no `yggdrasil` process; `ls /sys/class/net` showed only `eth0`/`lo`, no `tun0`/`ygg0`. `setup_dependencies.sh --yggdrasil` only installs the package — nothing in `docker-entrypoint.sh` or anywhere else in this repo starts it. There's no init system in the container to run the OS package's normal auto-start postinst hook, so the feature can't work at all today, regardless of this port.
  2. **Publishing this port via Docker wouldn't actually help even once the daemon is wired up.** Yggdrasil mesh traffic arrives over a TUN device that lives inside the container's own network namespace — it doesn't traverse the Docker bridge/NAT layer at all, so Docker's `-p`/`ports:` port-forwarding has nothing to translate. A published port here would only matter for reaching this listener via the container's *regular* public IP directly (bypassing the mesh entirely), which undercuts the reason to use Yggdrasil addressing in the first place. See `DOCKER.md` §4 sizing/ports section for the fuller writeup and a sketch of what actually wiring this up would take (TUN device + `NET_ADMIN` capability, config generation, process supervision).

---

### 5. DID Service
- **Port**: `15052` (gRPC)
- **Protocol**: gRPC
- **Code Reference**: `DID/DID.go`
- **Endpoints**: `RegisterDID`, `GetDID`, `ListDIDs`
- **`RegisterDID` has no authentication** — the `ServiceDID` security policy is
  `AuthType: None` (`config/settings/security.go`), and
  `pkg/gatekeeper/auth_grpc.go`'s `authenticate()` returns `nil` immediately
  for that case (verified in code, not assumed). Anyone who can reach this
  port and isn't rate-limited can register an arbitrary DID — this is not
  just a read-only "resolver" endpoint despite the name.
- **Bare metal**: public (`0.0.0.0`) on nodes deliberately acting as DID
  resolvers, accepting that tradeoff. Set `ports.did: 0` to disable on
  non-resolver nodes.
- **Docker: not exposed by default** (`docker-compose.yml`'s `ports:` entry
  for `15052` is commented out) — the listener still runs inside the
  container (`ports.did` defaults to `15052`, not `0`), it's simply not
  published to the host/internet. Uncomment only if you're deliberately
  running a public DID-resolver node.

---

### 6. CLI Admin Interface
- **Port**: `15053` (gRPC)
- **Protocol**: gRPC
- **Code Reference**: `CLI/GRPC_Server.go`
- **Endpoints**: `AddPeer`, `StopNode`, `FastSync`
- **Purpose**: Full administrative control of the node.
- **Recommendation**: ⚠️ **Strictly localhost (`127.0.0.1`) only.** Never expose to the network, even internally. Use an SSH tunnel for remote administration.

---

### 7. BlockGen API
- **Port**: `15050` (HTTP)
- **Protocol**: HTTP
- **Code Reference**: `Block/server.go`
- **Endpoints**: `POST /api/process-block` — triggers consensus.
- **Recommendation**: Disabled (`0`) on generic nodes. Localhost only on the nodes that run this role.
- **Docker**: not exposed — not in `docker-compose.yml`'s `ports:` list or the Dockerfile's `EXPOSE`.

---

### 8. BlockGRPC (Validator P2P)
- **Port**: `15055` (gRPC)
- **Purpose**: Validator-to-validator block propagation.
- **Recommendation**: Disabled (`0`) on passive or observer nodes. Internal-only on validator clusters.
- **Docker**: not exposed — same as `15050`.

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
| `allow-yggdrasil` | Ingress | `0.0.0.0/0` | `tcp:15001` | Allow | Bare-metal nodes only — see §4, not applicable to the Docker deployment path today |
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
