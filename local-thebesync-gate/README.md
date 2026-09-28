# ThebeSync catch-up gate

Validates the ThebeSync (FastSync v4) catch-up end-to-end: a lagging/fresh node
`B` brought up via `catchup` must end **byte-identical** to source node `A`
(same tip, same block hashes, same sampled balances).

This is a local, gitignored harness — not committed.

## What it proves

`A` serves over `/fastsync/v4/{head,getblocks}`; `B` fetches, verifies
(body-binding + committee certificate + parent linkage), applies through the
normal `ProcessBlockTransactions` path (with the P2.5 state-fingerprint halt), and
advances its tip. If `B`'s derived state diverged from `A`'s at any block, the
P2.5 halt fires and `B` stops — the gate then fails on the tip mismatch.

## Prerequisites

Two running jmdn nodes with the Ethereum-compatible JSON-RPC facade exposed:

- **A** — source, ahead (the sequencer, or an already-synced node).
- **B** — target, behind A. Either a fresh node (genesis seeded, tip 0) or a node
  intentionally stopped a few blocks back.

Both must be on the same chain (same genesis) and able to dial each other over
libp2p. Contracts should be enabled on both so the P2.5 fingerprint gate is active.

## Run

```bash
NODE_A_RPC=http://localhost:8545 \
NODE_B_RPC=http://localhost:8546 \
A_MULTIADDR=/ip4/127.0.0.1/tcp/15000/p2p/12D3KooW...A \
B_CATCHUP="docker exec jmdn-nodeB /app/jmdn -cmd catchup" \
SAMPLE_ACCOUNTS="0xYourFundedAddr1,0xYourFundedAddr2" \
./catchup_gate.sh
```

- `A_MULTIADDR` — A's full libp2p multiaddr including `/p2p/<peerID>`.
- `B_CATCHUP` — the command that triggers catch-up **on node B**; the script
  appends `A_MULTIADDR`. Use `docker exec <B> jmdn -cmd catchup`, or just
  `jmdn -cmd catchup` if B's CLI targets B's own gRPC.
- `SAMPLE_ACCOUNTS` — optional; balances checked in addition to block hashes.
- `POLL_TIMEOUT` (default 120s), `HASH_SAMPLES` (default 8) are tunable.

Exit `0` = PASS. Non-zero prints the first divergence (stuck tip, hash mismatch,
or balance mismatch).

## Negative check (recommended)

To confirm the gate actually catches divergence: point `B_CATCHUP` at a peer
serving a *perturbed* chain, or corrupt one account on B before catch-up — the
P2.5 halt should stop B and the gate should FAIL on the tip mismatch. A gate that
only ever passes proves nothing.

## Notes / assumptions

- Uses standard `eth_blockNumber`, `eth_getBlockByNumber`, `eth_getBalance`. If
  your facade names differ, adjust the `rpc` calls in `catchup_gate.sh`.
- The old FastsyncV2 Merkle-bisection engine is retired; `catchup` now routes
  through `thebesync.CatchUp`.
