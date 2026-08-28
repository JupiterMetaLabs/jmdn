# Remaining Work — Stage 7 and seedNodes §4

Two independent items outstanding after A4 (jmdn side complete). Neither is
a "how fast can it be coded" problem — both are blocked on things outside
this repo's own code: an operational fact (Stage 7) and a different repo's
owner (seedNodes §4).

---

## 1. Stage 7 — delete the old vote write/read path

Source: `docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md` §10. Marked **not
revertible — do last**.

### 1.1 Gate check — do this before anything else, and it isn't a coding task

- [ ] Confirm `JMDN_VOTE_CRDT_V2` has been **on** across the fleet.
- [ ] Confirm it's been on long enough to count as a real soak, not just
      "turned on recently and nothing exploded yet." The LLD doesn't put a
      number on this — decide what's actually enough runway for your
      deployment (a full epoch cycle? a specific number of days of quiet
      operation? your call, not a default to inherit from this doc).
- [ ] Confirm no rollback to flag-off has happened during that window that
      would reset the soak clock.
- [ ] Get sign-off from whoever owns production ops that the answer to all
      three above is yes — this is a deployment-history fact, not something
      verifiable by reading code.

**If any box above is unchecked, stop here.** Nothing below this line
should be started until this section is fully checked.

### 1.2 Design gap — Stage 7 has no file-level plan yet, unlike Stages 1-6

Every other stage in the LLD has a dedicated section with exact fields,
call sites, and code. Stage 7 exists only as one row in the §10 summary
table ("delete old write + old reader"). Before any deletion can be
planned, let alone executed, this needs to be worked out and written down:

- [ ] Enumerate the exact "old write path" — which function(s) currently
      write votes outside the CRDT path, in which file(s).
- [ ] Enumerate the exact "old reader" — the pre-Stage-4
      `ProcessVotesFromCRDT` legacy body (`processVotesFromCRDT_legacy` in
      `AVC/BuddyNodes/MessagePassing/Structs/Utils.go`) and anything else
      that still calls it.
- [ ] Re-derive what the four call sites from §6.3 (Stage 4) fall back to
      once the legacy branch is gone — confirm nothing outside the
      CRDT-v2 path still depends on the old return shape.
- [ ] Check for anything reading the dual-write flag
      (`Vote.VoteCRDTDualWrite` / `voteCRDTV2Enabled`) that assumes the
      legacy path might still be live — those checks become dead branches
      once Stage 7 lands and should be removed too, not left as unreachable
      code.
- [ ] Write this up as its own LLD section (mirroring Stages 1-6's format)
      before writing any deletion code — this is planning work, achievable
      without touching the fleet, and doesn't depend on the soak gate.

### 1.3 Execution (only after 1.1 and 1.2 are both done)

- [ ] Delete the old write path.
- [ ] Delete the old reader (`processVotesFromCRDT_legacy` and the
      dispatcher branch that calls it).
- [ ] Remove the now-dead flag-check branches.
- [ ] `go build ./... && go vet ./... && go test ./...` — no toolchain
      available in this session to do this step; whoever executes Stage 7
      needs to run it themselves.
- [ ] Confirm this is understood as a one-way door before merging — there's
      no flag to flip back once the legacy code is gone.

---

## 2. seedNodes side — §4 sequencer-auth enforcement

Source: `docs/A4-COMPLETION-LLD.md` §4, draft at
`seedNodes/_A4_SEED_AUTH_DRAFT/` (`sequencer_auth.go` + `README.md`) —
verified accurate against the real `origin/JMNS` code, never compiled
(no Go toolchain available on either this session's device or this
session's environment for the seedNodes repo).

| Piece | Status | What's actually needed |
|---|---|---|
| Receive RPC (`UpdatePeerWeights`, `ListBuddy`) | ⚠️ Exists | Nothing — already implemented in `cmd/jmns-service/main.go` on `JMNS`. |
| Actually update weight | ⚠️ Exists | Nothing — `GormJMNSService.UpdatePeerWeights` already writes to the DB. |
| Authenticate caller | ❌ Not implemented/live | Land `sequencer_auth.go`'s `VerifySequencerRequest` and call it as the first line of both handlers — see the draft's `README.md` for the exact two call sites and line numbers. |
| Verify sequencer (not just self-consistency) | ❌ Not implemented/live | Same function — it checks the claimed peer ID against a **pinned** `TrustedSequencerPeerID` from config, not just that the signature is internally consistent. This is the specific trap flagged in `A4-COMPLETION-LLD.md` §4.3; don't let a simpler implementation skip the pin check. |
| Key-type compatibility | ❓ Must test | `resolveSequencerPubKey`'s primary path (`libp2pPeer.ExtractPublicKey`) only works if the sequencer's libp2p identity key is a small/inlinable type (Ed25519, typically). **Test this in isolation, first**, against the real `SEQUENCER_PEER_ID` value, before wiring anything else. If it fails, set `SEQUENCER_PUBKEY_HEX` explicitly — the fallback path is already written, just needs the operator to export that value once. |
| Compile/test integration | ❌ Not done | `go build ./... && go vet ./... && go test ./...` in seedNodes on `JMNS`, after landing the file and the two call-site edits. Not achievable from this session — no toolchain for this repo either. |
| Merge into seedNodes | ❌ Not done | PR + review by whoever owns that repo. This is the actual "done" line for §4 — a draft folder sitting outside the tracked tree does not count, however accurate it's been verified to be. |

### 2.1 Order these five matter in

- [ ] Test key extraction in isolation (the ❓ row) — this determines
      whether `SEQUENCER_PUBKEY_HEX` config is required or optional, which
      changes what operators need to be told to provide.
- [ ] Land `sequencer_auth.go` at `pkg/peer/sequencer_auth.go` on `JMNS`.
- [ ] Add the `PinnedSequencerPubKeyHex` config field (`README.md` has the
      exact diff).
- [ ] Wire `VerifySequencerRequest` into both handlers (`README.md` has the
      exact diff, including the method-name gotcha: `UpdatePeerWeights`
      must check `"PushReputation"`, not `"UpdatePeerWeights"` — that's the
      string jmdn's client actually signs).
- [ ] Build, vet, test, review, merge.

Until this lands, `UpdatePeerWeights` stays open to any caller — jmdn's
side is correct and complete, but the enforcement guarantee A4 exists to
provide isn't real until this row is checked off.
