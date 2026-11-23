# Certified block commit

A block with a valid committee certificate is stored, or its height waits. The sequencer never skips it and never moves to a new round at that height while the certificate exists. This page covers how that works, what operators see, and how to recover.

## How a certified block is committed

1. When the votes reach quorum, the sequencer saves a **pending record** (the block and its certificate) in its own store, before it tries to store the block.
2. It stores the block. On failure it retries the **same block, same certificate, same period** after 1, 2, 4, 8, 16, 30, 30 and 30 s.
3. If every attempt fails, the height **halts**: alert `Consensus: Certified Block Halted`, the record is kept, and the period is not advanced.
4. A background task retries the pending block every minute until it is stored. A store outage that heals is committed without a restart. A restart also resumes it.
5. Once the block is stored, the record is cleared and the block is broadcast.

While a record exists, the block servers refuse proposals at that height and above (HTTP 409 / gRPC `AlreadyExists`, reason `height_pending_commit`), and the round-ledger clear endpoint refuses that height.

Before each attempt the sequencer checks what is stored at the height. A different block stored there, a block with only some of its transactions applied, or a halted block apply on this node (see [block-apply-halt.md](block-apply-halt.md)) cannot heal on its own; the height stays halted until an operator repairs it.

## What operators see

- **Alert** `Consensus: Certified Block Halted`, labels `block_number`, `block_hash`, `attempts`, `cause`. Repeated at most every 15 minutes per height while the height stays halted.
- **Logs** `Certified block failed to apply; retrying the same block at the same period`, then every minute `Pending certified block is halted; retrying in the background`.
- **Status endpoint**, on the facade port with the admin token:

  ```
  curl -s -H "Authorization: Bearer $ADMIN_TOKEN" http://<sequencer>:8545/consensus/pending-commit
  ```

  returns `{"pending": true, "height": N, "block_hash": "0x...", "created_at": ...}` or `{"pending": false}`.

## Recovery

1. Read the `cause` label and fix it (database, disk, network, seed access; for a halted block apply, resync and clear the apply halt).
2. Wait about a minute. The background retry stores the same block; `GET /consensus/pending-commit` then returns `"pending": false`.

### Last resort: drop the pending record

Only when the block can never be stored on this sequencer, and with engineering sign-off on how the height continues. Once the record is gone, a **different** block can be certified at the height while this block's certificate still exists.

```
curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"height": N, "block_hash": "0x..."}' \
  http://<sequencer>:8545/consensus/pending-commit/drop
```

Height and block hash must both match the record, or the call is refused with 409. The drop waits for a commit attempt in progress (at most one attempt, not a whole retry series); no attempt to store the block starts after it, and the background retry never re-creates the record. The drop is logged with the record identity, and the block's transactions are reported to the orchestrator as not landed.
