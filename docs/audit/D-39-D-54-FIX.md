# D-39 + D-54 — VDF fleet-parameter identity (fix record)

**Branch:** `feat/vdf-fleet-identity` (off `v3base`) · **Date:** 2026-09-16
**Findings:** AVC-CONSENSUS-HANDOVER.md D-39 (difficulty `T` has no fleet-agreement
check) + D-54 (the VDF group is bound only implicitly). Per the audit these are one
problem and **must land together** — binding `T` alone leaves two nodes on different
sourced moduli passing `enforceModulusChainPolicy` and then rejecting each other's
proofs with nothing naming the group.

## What was wrong (verified against live code, 2026-09-16)

- `beacon.Pipeline.Accept` (avc `beacon/beacon.go:114/118`) rejected `proof.T !=
  p.difficulty` and let `vdf.Verify(p.group, …)` bind the group **implicitly** by
  re-deriving the challenge. `vdf.Proof` (avc `vdf/vdf.go:248`) carries `Y, Pi, T` —
  no group/modulus — so a receiver had nothing to compare the group against and no
  error named it (D-54).
- `T` was a per-host env var (`JMDN_AVC_VDF_DIFFICULTY_T`); `beacon.Pipeline.Difficulty()`
  had zero jmdn callers, so `T` was never gossiped, persisted, or hashed. A node with a
  divergent `T` rejected every honest proof and published divergent entropy into its own
  sink — silent on the side that was wrong (D-39).

## The fix — one fleet-checked identity = `sha256(domain ‖ group ‖ modulus_digest ‖ T)`

| Phase | Commit | Change |
|---|---|---|
| 1 | `95bf076` | `messaging/vdf_identity.go` — `VDFIdentityDigest`, `Set/LocalVDFIdentity`, `ErrVDFIdentityMismatch`; tests assert each of group/modulus/T moves the digest and the concat is injective |
| 2 | `ab5446a` | `T` becomes a chain parameter: `networkPinPolicy.PinnedDifficultyT` (rsa-2048-testnet-ephemeral → 476510); `InstallAVCBeaconFromEnv` refuses a divergent `T` on a pinned group, then computes + publishes the local identity. `config.ZKBlock.VdfParamsDigest` added and stamped on the boundary block |
| 3 | `37ce305` | `VerifyAndAcceptVDFProof` compares the block's identity against the local one (CHECK 0) and rejects with `ErrVDFIdentityMismatch`, printing both sides — closing D-54's nameless failure and making a divergent-`T`/group node detect itself (D-39) |
| 4 | _this_ | jmdn-side regression tests on the real accept path; this record |

**Rollout is additive:** the identity check fires only when *both* the local node and the
block carry an identity, so Stage-1 nodes and pre-upgrade proposers are unaffected. Once
the fleet runs this binary, every boundary block carries the identity and a
mis-parameterised node is rejected — and sees why — locally.

## Verify (on a linux host with the toolchain; not runnable in the authoring env)

```
cd jmdn
GOWORK=off go build ./...
GOWORK=off go test ./messaging/ -run 'VDFIdentity|VerifyAndAcceptVDFProof_' -v
GOWORK=off go vet ./messaging/ ./Sequencer/ ./Block/ ./config/
```

Two-node parameter-disagreement check (the property, end to end): bring up two nodes with
different `JMDN_AVC_VDF_DIFFICULTY_T` on a *pinned* group → the mis-set node refuses to
start Stage 2 (D-39). With an *unpinned* group and different moduli → the adopter logs
`VdfProof REJECTED … VDF parameter identity … does not match` naming both digests (D-54).

## Register flip (do in the PR into `v3base`)

- **D-39** → `Fixed (feat/vdf-fleet-identity ab5446a+37ce305, messaging/vdf_identity_test.go + entropy_vdf_accept_identity_test.go)`
- **D-54** → `Fixed (same)` — landed together, as required.

## Deliberately NOT done here — the hardening follow-up

`VdfParamsDigest` is **advisory**: it is compared on adoption but **not** folded into the
`ConsensusHash` preimage, so a relay could strip/alter it and cause a *false reject* of one
epoch's proof (non-fatal — the node recovers by local seal; the proof itself stays
`ConsensusHash`-bound). This closes the audit's stated D-39/D-54 concern (honest fleet
**misconfiguration**, detectable + named), not relay tampering.

To make it tamper-proof, fold it into the M2b preimage — the same two-line pattern D-28
recommends, gated by `JMDN_M2B_HASH`, in `Security/consensus_fields_hash.go`:

```go
committee.WriteField(&buf, []byte(block.VdfParamsDigest))
```

That is a **ConsensusHash format change → coordinated fleet restart** (like 8872912), so it
must land with a two-node hash-agreement test and is intentionally left as a separate,
reviewed step rather than shipped untested into the frozen preimage.

## Status of this change

**Untested in the authoring environment** (no Go toolchain / private-module access there).
Phase-1 logic is unit-tested by inspection; the build + full test run above is the gate.
The identity primitive and the accept-path check are pure and self-contained; the risk
surface is the install-path wiring (`beacon_install.go`) and the boundary-block stamp,
both of which need the `go build ./...` above to confirm symbol resolution.
