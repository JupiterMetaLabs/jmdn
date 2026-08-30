# Seed-Node `GetCommitteeSnapshot` — Handoff Spec

**Status (2026-08-28):** The jmdn client side of this RPC is fully built, tested, and wired live. The seed-node server side is the one missing piece — its generated gRPC handler currently returns `Unimplemented`. This document is everything the seed-node implementer needs; nothing here requires reading jmdn's code first.

**Why this matters:** every remaining step in scaling the validator set beyond today's 7 (pool pinning, `SnapshotOrder`, growing `max_validators`) is blocked on this one endpoint existing. See `docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md` and `docs/VALIDATOR-SCALE-VOTE-AGGREGATION-LLD.md` for the broader picture — this doc is scoped to just the server contract.

---

## 1. What already exists on the jmdn side (reference only — you don't need to touch this)

- `seednode/proto/seednode.pb.go` / `seednode_grpc.pb.go` — the gRPC types and client stub are already generated and correct. Do not regenerate; match these exactly.
- `seednode/committee_snapshot_client.go` — `FetchCommitteeSnapshot`, plus a full caching/eligibility layer (`CommitteeEligibility`, `CommitteeEligibilityAuto`: TOFU authority pinning, TTL cache, fail-closed on every error path). This is live in production (`main.go:1656`, `Sequencer/consensus_statemachine.go:164`).
- `seednode/committee/contracts.go` — jmdn's byte-for-byte mirror of the signing/verification contract. Its own header comment says this must match `seedNodes/pkg/peer/committee_snapshot.go`'s `canonicalCommitteeBytes`/`VerifyCommitteeSnapshot` — if those already exist in the seed-node repo, the crypto side of this may already be built there too, and only the gRPC handler wiring is missing. Worth checking before assuming a full rewrite is needed.

## 2. The RPC contract (wire shapes)

```protobuf
message GetCommitteeSnapshotRequest {
  uint64 epoch = 1;
}

message CommitteeSnapshotEntry {
  string peer_id = 1;
  string bls_pub = 2;   // lowercase hex
}

message CommitteeSnapshot {
  uint64 epoch = 1;
  repeated CommitteeSnapshotEntry entries = 2;  // ALL eligible validators, sorted by peer_id
  string seed = 3;
  string authority_pubkey = 4;   // lowercase hex, the signer's own BLS public key
  string signature = 5;          // hex
}

message GetCommitteeSnapshotResponse {
  CommitteeSnapshot snapshot = 1;
}

service PeerDirectory {
  rpc GetCommitteeSnapshot(GetCommitteeSnapshotRequest) returns (GetCommitteeSnapshotResponse);
}
```

## 3. Signing contract — must match byte-for-byte

The client verifies every snapshot with a BLS signature check (`dela/bls`, BN256 — same library jmdn uses for votes). Get this exactly right or every snapshot will fail client-side verification.

**Canonical bytes to sign** (`CanonicalCommitteeBytes` in `seednode/committee/contracts.go:134`):

```
"jmdt/committee/v1" + "|" + <epoch as decimal string> + "|" + <seed string> + "|" + <entries>

where <entries> = comma-joined "<peer_id>:<bls_pub>" pairs,
      entries sorted by peer_id ascending,
      bls_pub lowercased and trimmed before joining.
```

Example, epoch=42, seed="abc", two entries:
```
jmdt/committee/v1|42|abc|peerA:aabbcc,peerB:ddeeff
```

**Signature:** BLS-sign those exact bytes with the authority's private key. Populate `authority_pubkey` with the lowercase hex of the corresponding public key, and `signature` with the hex signature.

**Client-side verification** (for your own testing — you don't implement this, jmdn already has it): `VerifyCommitteeSnapshot` decodes `authority_pubkey` and `signature` from hex, rebuilds the canonical bytes from the response's own `epoch`/`seed`/`entries`, and BLS-verifies. It also fails closed on: nil snapshot, empty `entries`, or (when the client has a pinned authority key configured) an `authority_pubkey` that doesn't match the pinned key.

## 4. Epoch semantics — the one thing most likely to cause silent failures

There are **two different meanings of "epoch"** in play depending on which client call path is asking, and the server must handle both correctly:

**A. Unpinned / "give me current" reads.** The client sends `epoch = 0` as a sentinel meaning "whatever you consider the current eligible set." The server should return its latest snapshot. The client then independently checks freshness using its own clock (`unix_time / epoch_seconds`, default 3,600s) — it does **not** require your `0` response to literally equal `0`, it just needs the returned snapshot's real `epoch` field to be within ±1 of the client's own time-derived epoch number. So for this path, whatever epoch-numbering convention you use internally (e.g. an hourly clock) is fine, as long as it's roughly time-based and the response's `epoch` field reflects that number honestly.

**B. Pinned / historical reads (the important one).** When `require_pinned_committee` is enabled on jmdn's side, a node re-deriving an old block's committee sends a **specific, non-sentinel epoch value** — and this value is **not** a time-based epoch. It is jmdn's own block-height-derived `SelectionPeriod` (`EpochForHeight(height) = height / committee_epoch_blocks`; with `committee_epoch_blocks = 1`, this is currently just the block height itself). The client requires an **exact match**: if your response's `epoch` field doesn't exactly equal the requested value, the client fails closed and rejects the round entirely — no "close enough."

**This means:** the seed-node server needs some way to resolve "the eligible set as of jmdn selection-epoch N" — which today effectively means "as of block height N" — not just "the eligible set N hours ago." If the seed node currently only tracks committee membership on an internal hourly/time clock, this is a real integration gap to close, not just a formatting detail. Options worth discussing with the jmdn side before implementing:
  - The seed node could track/version its eligible-set changes by block height (requires it to know jmdn's chain height, e.g. via the existing `sequencer_reputation_push`/block-head-push channel jmdn already sends).
  - Alternatively, jmdn's `committee_epoch_blocks` could be coordinated to align with however the seed already versions snapshots — but this needs to be a deliberate agreement, not an assumption either side makes silently.

**Do not guess this — confirm the exact numbering convention with the jmdn side before shipping**, since a mismatch here fails safely (pinned reads reject) but silently in the sense that nothing will look broken until pinning is actually enabled.

## 5. Failure behavior expected of you

The client is fail-closed everywhere, so the server doesn't need to be lenient — return real errors rather than empty/placeholder snapshots:
- Unknown/unresolvable epoch (pinned path) → return a gRPC error, not an empty `CommitteeSnapshot`. The client cannot distinguish "empty because nobody's eligible" from "empty because the server gave up," and both are currently fail-closed anyway, but a real gRPC error is clearer for your own logs and easier for jmdn's operators to diagnose.
- Never sign and return a snapshot with zero entries — the client explicitly rejects `len(snap.Entries) == 0` regardless of signature validity.

## 6. What to test before calling this done

1. A call with `epoch = 0` returns a validly-signed snapshot the jmdn client's `CommitteeEligibilityAuto` accepts and caches.
2. A pinned call for the *exact* current selection-epoch value succeeds.
3. A pinned call for a *past* selection-epoch (one that has since rotated/changed) still returns that historical set correctly, with `epoch` in the response matching exactly what was requested.
4. A pinned call for an epoch you cannot resolve returns a clean gRPC error, not a malformed/empty success response.
5. Round-trip the exact canonical-bytes construction above against a jmdn-side test vector before wiring the real handler — a byte-for-byte mismatch anywhere (wrong join character, wrong sort order, un-trimmed hex) fails signature verification with no useful diagnostic on the client side.
