# CLAUDE.md

Guidance for working in this repository.

## What this is

`jmdn` is a peer-to-peer blockchain node. A node discovers
peers over libp2p, propagates and validates blocks, participates in committee-based
consensus, persists chain and account state in ImmuDB, and exposes Ethereum-compatible
and node-management APIs over gRPC/HTTP.

- Language: Go (see `go.mod` for the toolchain version).
- Build tag: CGO is required (`CGO_ENABLED=1`) because of ImmuDB and go-ethereum dependencies.
- Produced binary: `jmdn` (built from the root `main.go`).

## Build, run, test

```sh
make build        # CGO_ENABLED=1 go build -o jmdn .
make run          # build then ./jmdn
make test         # go test ./...
make lint         # golangci-lint run (config in .golangci.yml)
make fmt          # golangci-lint fmt (gofmt-compatible)
```

Notes:
- Some tests are integration tests and expect a live ImmuDB instance and a reachable
  seed node; unit tests run without them.
- Node configuration is read from `jmdn.yaml` (see `jmdn_default.yaml` for the shipped
  defaults) and can be overridden via environment variables (many flags are prefixed
  `JMDN_`).
- Common runtime flags are defined in `main.go` (for example `-seed`, `-connect`,
  `-seednode`, `-metrics`, `-cli`, `-did`, `-geth`).

## High-level architecture

- **Entry point (`main.go`)** wires up the libp2p host, database pools, gRPC/HTTP
  servers, block synchronization, metrics, and the consensus wiring, then runs until
  shutdown.
- **Networking (`node/`, `Pubsub/`, `messaging/`)**: libp2p host lifecycle, gossip
  pubsub topics, block propagation, and broadcast.
- **Consensus (`Sequencer/`, `AVC/`)**: the sequencer proposes blocks and drives the
  consensus state machine; `AVC/` holds the committee machinery — buddy-node message
  passing, BLS signing/verification, the BFT engine, and VRF-based node selection.
- **Validation (`Security/`, `messaging/`)**: canonical block-hash / transaction-root
  recomputation, transaction value checks, and certificate verification on the receive
  path.
- **State (`DB_OPs/`, `FastsyncV2/`, `crdt/`)**: ImmuDB-backed account and block state,
  crash-safe transaction application markers, an equivocation record store, a sync
  anchor, and state synchronization between peers.
- **Interfaces (`Block/`, `gETH/`, `CLI/`, `DID/`, `CA/`, `Mempool/`)**: block/tx
  submission API, an Ethereum-compatible gRPC surface, a node CLI/gRPC server,
  decentralized identity, certificate/signing helpers, and the mempool client.
- **Cross-cutting (`config/`, `logging/`, `metrics/`, `helper/`, `internal/`,
  `l1finality/`, `shutdown/`, `profiler/`)**: settings and constants (including protocol
  IDs and pubsub topic names), structured logging, Prometheus metrics, reputation and
  sync monitoring, L1 finality, and graceful shutdown.

## Consensus model (overview)

- The sequencer builds ZK blocks and requests votes from a committee of buddy nodes.
  Committee membership is sourced from the seedNode buddy selection and is authenticated
  via signed committee snapshots; an operator `block_buddy` blocklist is subtracted from
  the eligible set.
- Each vote is a BLS signature bound to the specific block: the vote domain includes the
  block hash, the network chain id, and the block height, so a signature is scoped to one
  block, on one chain, at one height. Multiple vote-domain versions are accepted during a
  rollout and are selected by version precedence.
- A block certificate is accepted only when it reaches a Byzantine fault-tolerant `2f+1`
  quorum over the authenticated committee size, counting one vote per eligible peer with
  a snapshot-bound public key. Verification is fail-closed: with no committee source, a
  source error, or an empty eligible set, the node does not accept the certificate.
- On the propagation path the node recomputes the canonical block hash and transaction
  root from the received transactions (rejecting a mismatch before certificate
  verification), records equivocation durably so conflicting blocks at the same height are
  caught across restarts, and enforces parent/height linkage (contiguous linkage is
  required; gaps trigger authenticated catch-up rather than acceptance).
- A separate BFT engine (`AVC/BFT`) exchanges signed PREPARE/COMMIT messages; Byzantine
  tolerance `f` is derived dynamically as `(n-1)/3` from the buddy count.

## Conventions

- Protocol IDs and pubsub topic names are centralized in `config/constants.go`. Changing
  a wire protocol or its version affects network compatibility.
- Committee/validator sizing is governed by `config.MaxMainPeers` and
  `consensus.max_validators` (`config/settings`); these must agree.
- Goroutine/thread names used with the orchestrator live in `config/GRO`.
- Run `gofmt`/`golangci-lint fmt` before committing; CI mirrors the `Makefile` targets.
