# L1-finality spoofing — unauthenticated ingress (Ibnu76 report, 2026-09-25)

**Branch:** `fix/l1-finality-auth` (off `origin/fix/evm-rpc-parity-v3base` @ `816ba8f`).
**Reporter:** Ibnu76 (external bug bounty). **Severity:** High (recommended) — see below.
**Verified against current source** (report cited a7c1630; the defect is unchanged and live on today's tree).

## Finding (all claims independently reproduced from source)

A node accepts L1-finality data (the Ethereum commit-rollup tx hash + L1 block number) over ingress paths
that do not authenticate the publisher:

- **Gossip** (`Service/subscriptionService.go:432`, `PubSubConnector/subscriptionService.go:210`): the only
  guard was a self-echo check, then `l1finality.ApplyCommit/ApplyRange`. No sender-is-sequencer check. The
  topic is `IsPublic:true` (line 560) and **no** GossipSub topic validator is registered anywhere in source
  (`RegisterTopicValidator`/`WithValidator` appear only in docs).
- **HTTP** (`Block/Server.go:1079` `receiveL1Commit`, `:1184` range): bind → `Validate()` → `ApplyCommit`,
  behind rate-limit + TLS middleware only, no handler auth (compare `/api/submit-raw-tx`, which runs
  `Security.AllChecks`).
- **Validation** (`l1finality/l1finality.go:33,49`) is field-presence only; `MaxRangeSpan=10_000`, so one
  range message poisons up to 10k blocks.
- **Fingerprint blind spot** (`internal/merkle/hash.go`): the SyncMonitor Merkle root hashes
  `fastsync_types.ZKBlock` content, which has **no** L1 fields, so a poisoned finality flag never changes
  the root a node reports to the seednode → divergence detection / self-heal never fire → it persists.
- **Exposure** (`gETH/Facade/rpc/handlers.go:114-148,938`): served to clients via
  `eth_getBlockByNumber(wantL1Commit=true)` / `LatestL1CommitBlock` as L1-finality proof.

**Honest bound (reporter's, confirmed by me):** the L1 flag gates nothing in this node's own
consensus/balance/reorg — no `Sequencer/`, `Security/`, or `messaging/BlockProcessing/` code reads the L1
fields; only the DB layer (storage/hydration) and the RPC facade do. Impact is unauthenticated persistent
state modification + false finality over RPC. Fund loss requires an external consumer (bridge/exchange)
that trusts the flag — which is the flag's raison d'être on an L2, hence the **High** recommendation.

## What shipped (this branch) — gossip authentication

Reuses the identity the vote path already trusts: under gossipsub's default **StrictSign**,
`msg.Sender` is the cryptographically authenticated originator (libp2p `GetFrom`), **not** the
self-declared `msg.Data.Sender` payload field — established by the D-26(a) fix
(`subscriptionService.go:227-243`) and set at `SubscriberHelper.go`/`SubscriptionManager.go`. So requiring
`msg.Sender` to equal the fleet's pinned sequencer is a sound, wire-format-free authentication.

- **`internal/l1auth`** (new, pure): `Enforced(pinnedID)` and `IsSequencer(authenticatedSender, pinnedID)`
  (peer.Decode compare, fail-closed). Unit-tested in-sandbox: `go test ./internal/l1auth/` → **ok**
  (sequencer accepted, attacker rejected, empty/garbage fail-closed).
- **`l1finality.AuthorizeGossipSender(sender)`** — single shared chokepoint (avoids the three-call-site
  drift the package exists to prevent). Reads `config.Consensus.SequencerPinnedPeerID`.
- **Both gossip receivers** (`Service` + `PubSubConnector`, `handleL1Commit` + `handleL1CommitRange`) now
  drop a commit whose authenticated sender is not the pinned sequencer; when no pin is configured they
  apply with a **loud WARN** (non-bricking pre-rollout).

**No new config or posture gate needed:** `SequencerPinnedPeerID` already exists (the CON-03 vote-requester
source of truth) and is **already required by `ValidateProductionConsensusPosture`**
(`messaging/production_posture.go:88`) — so in production the pin is non-empty and L1 gossip auth is
enforced automatically. The empty-pin WARN branch cannot ship to mainnet (the block-858 finding-4 lesson,
already enforced by that gate).

## Severity after this fix

The **primary reported vector — "any mesh peer can forge finality" — is closed** (a forged gossip commit
from a non-sequencer is dropped fail-closed). Residual is narrower (see Phase 2): reaching the sequencer's
HTTP endpoint. Net: High → materially reduced; the remaining risk requires network access to the
sequencer's API port.

## Phase 2 (designed, NOT shipped — each needs a decision)

1. **HTTP `/api/l1-commit(-range)` orchestrator authentication (confused-deputy).** The sequencer applies
   the HTTP body locally *and re-broadcasts it under its own authenticated identity*, so an attacker who can
   reach the sequencer's API port launders a forgery into an authenticated gossip commit — undermining the
   gossip fix from that vantage. Not auto-fixed here because the correct control depends on the deployment:
   **where does the orchestrator run relative to the sequencer, and what credential can it present?**
   Options: bind these routes to loopback (if co-located), require a shared bearer token / mTLS on just
   these routes, or have the orchestrator sign requests with a pinned key. Guessing wrong would silently
   break L1 ingestion (a finding-4-style trap), so this is left for the operator decision. **Recommended
   default:** loopback-bind if co-located; else a required token on the two routes, made mandatory in
   production posture.
2. **Fold L1 fields into the SyncMonitor fingerprint** (the "can't hide" half). Requires adding `L1TxHash`/
   `L1BlockNumber` to the hashed type (`JMDN-FastSync` `ZKBlock`) and changing the Merkle-root preimage —
   a **coordinated fleet-wide cutover** (like the state-fingerprint/consensus-hash preimages the block-858
   work deliberately did not touch). Lower priority now that ingress is authenticated: an attacker can no
   longer inject; this only matters against a compromised sequencer, for which detection is the goal.

## Test status (HONEST)

- **In-sandbox green:** `go test ./internal/l1auth/` (Go 1.26). All touched files `gofmt`-clean.
- **Untested in-sandbox:** `l1finality` + the two subscription handlers import the **private**
  `ThebeDB`/`avc` modules (no git creds in the sandbox). Validate on a build host:

```sh
CGO_ENABLED=1 GOWORK=off go build ./...
CGO_ENABLED=1 GOWORK=off go vet ./l1finality/... ./AVC/BuddyNodes/MessagePassing/Service/...
CGO_ENABLED=1 GOWORK=off go test ./internal/l1auth/
gofmt -l internal/l1auth l1finality AVC/BuddyNodes/MessagePassing/Service   # must print nothing
```

## Verification procedure (2-node / staging)

1. Configure `consensus.sequencer_pinned_peer_id` = the sequencer's libp2p peer ID fleet-wide.
2. From a non-sequencer peer, publish a forged `Type_L1CommitRange` to the L1-commit topic → the receiver
   logs `Dropping L1 commit range: authenticated sender is not the pinned sequencer` and applies nothing
   (`eth_getBlockByNumber wantL1Commit=true` still shows no L1 commit for those blocks).
3. Have the real sequencer receive an L1 commit (orchestrator → HTTP) and broadcast → peers apply it
   (`msg.Sender` == pinned sequencer) and RPC serves it. Confirms no false-negative on the legitimate path.
4. Unset the pin on a peer → it logs the `WITHOUT sender authentication` WARN and still applies (legacy).
5. Confirm production posture still refuses to boot with `SequencerPinnedPeerID` empty (pre-existing gate).

## Reporter reply posture
Confirmed and accepted; the PoC and honest bound are accurate. Severity set **High pending confirmation of
downstream finality consumers** (Medium if none). Gossip fix in progress; HTTP hardening + fingerprint fold
tracked as Phase 2. Good-faith disclosure (local-only, no publication, offer to re-verify).
