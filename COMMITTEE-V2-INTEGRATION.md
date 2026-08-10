# Wiring v2 into jmdn — the exact changes

**Status of the code:** `committee_v2.go` and `committee_v2_test.go` were **compiled and tested**
against stub packages mirroring your real signatures (`BLSresponse`, `VerifyForBlock`,
`DomainChainID`, `ConsensusSettings`, `eligibleMembers`). 11 tests pass, race-clean. What is *not* verified is
that they compile inside the real `messaging` package — that needs your toolchain.

Everything is gated on `JMDN_COMMITTEE_V2`, **default false**. With the flag off the behaviour is
byte-identical to today, so the binary can ship ahead of the fleet-wide flip.

---

## Change 0 — PRECONDITION: pin `consensus.seed_authority_bls_pub`

**v2 refuses to run without it**, and this is enforced in code
(`ErrLegacySourceUnderV2`), not left to documentation.

Under the legacy source, `eligibleMembers()` derives from `PeerList.MainPeers`, which derives from
the per-node VRF shuffle in `jmdn/AVC/NodeSelection`. That is a set **the nodes already disagree
about**. Seed-ranking it would order a different input on every node and produce a different
committee — exactly the failure v2 exists to remove. Garbage in.

So the ordering is: pin the authority key first, confirm the authenticated snapshot is being served
and consumed, *then* flip `JMDN_COMMITTEE_V2`. Pinning the key is independently the highest-value
security change available today — it also activates the `peer_id → bls_pub` binding, which is
currently a no-op because the legacy source supplies empty keys.

---

## Change 1 — `jmdn/messaging/consensus_hardening.go`

Split `eligibleMembers()` so the authenticated set can be obtained **without** the alphabetical cap.
The existing function keeps working exactly as before; the new one is what v2 consumes.

Replace the body of `eligibleMembers()` (currently lines ~151–205) with:

```go
// eligibleMembers is the authenticated eligible set, CAPPED to
// consensus.max_validators by sorted peer_id.
//
// The cap is correct for AGREEMENT — every node computes the same capped set and
// therefore the same threshold — and wrong for FAIRNESS: at P > k the peers whose
// votes can count are permanently the k alphabetically-first peer ids. See
// SelectCommittee for the seed-ranked replacement, which keeps the determinism
// and adds rotation.
func eligibleMembers() (map[string]string, error) {
	eligible, err := eligibleMembersUncapped()
	if err != nil {
		return nil, err
	}
	if lim := committeeSizeLimit(); lim > 0 && len(eligible) > lim {
		ids := make([]string, 0, len(eligible))
		for pid := range eligible {
			ids = append(ids, pid)
		}
		sort.Strings(ids)
		capped := make(map[string]string, lim)
		for _, pid := range ids[:lim] {
			capped[pid] = eligible[pid]
		}
		log.Warn().Int("eligible", len(eligible)).Int("cap", lim).
			Msg("committee: hard-capped validator set to consensus.max_validators")
		eligible = capped
	}
	return eligible, nil
}

// eligibleMembersUncapped is the authenticated eligible set with the blocklist
// applied but NO size cap. Seed-ranked selection caps instead.
func eligibleMembersUncapped() (map[string]string, error) {
	committeeEligibilityMu.RLock()
	fn := committeeEligibilityFn
	committeeEligibilityMu.RUnlock()

	if fn == nil {
		return nil, fmt.Errorf("committee eligibility source not configured (fail closed): call messaging.SetCommitteeEligibilitySource at startup")
	}
	buddies, err := fn()
	if err != nil {
		return nil, fmt.Errorf("committee eligibility source failed: %w", err)
	}
	if len(buddies) == 0 {
		return nil, fmt.Errorf("committee eligibility source returned an empty buddy set")
	}

	blocked := blockedBuddies()
	eligible := make(map[string]string, len(buddies))
	for pid, blsPub := range buddies {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		if _, isBlocked := blocked[pid]; isBlocked {
			log.Warn().Str("peer", pid).Msg("committee: buddy excluded by block_buddy blocklist")
			continue
		}
		eligible[pid] = normalizeBLSPub(blsPub)
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("committee empty after applying block_buddy blocklist")
	}
	return eligible, nil
}
```

This is a pure refactor — `eligibleMembers()` returns exactly what it returned before.

---

## Change 2 — add `jmdn/messaging/committee_v2.go`

Drop the file in. It compiles against `github.com/JupiterMetaLabs/avc/committee`, which jmdn already
reaches via the existing `replace github.com/JupiterMetaLabs/avc => ../avc` in `go.mod`. **No
`go.mod` change is needed.**

One line to adjust: `verifyCertificateLegacy` in the shipped file should be

```go
func verifyCertificateLegacy(responses []BLS_Signer.BLSresponse, blockHashHex string, height uint64) (CertificateResult, error) {
	return VerifyCertificate(responses, blockHashHex, height)
}
```

(the stub returns a zero value so the test module can build without the real verifier).

---

## Change 3 — the three certificate call sites

Each gains a `RoundContext`. With the flag off these are no-ops.

**`jmdn/Sequencer/Consensus.go:2189`** — inside `VerifyConsensusWithBLS`:

```go
// before
certRes, certErr := messaging.VerifyCertificate(blsResults, blockHashHex, blockHeight)

// after
var prevHash []byte
if zb := consensus.ZKBlockData.GetZKBlock(); zb != nil {
	prevHash = zb.PrevHash.Bytes()
}
certRes, certErr := messaging.VerifyCertificateForRound(
	blsResults, blockHashHex, blockHeight,
	messaging.RoundContext{PrevHash: prevHash, Height: blockHeight, Period: 0},
)
```

**`jmdn/messaging/blockPropagation.go:628`**:

```go
res, err := VerifyCertificateForRound(
	responses, msg.Block.BlockHash.Hex(), msg.Block.BlockNumber,
	RoundContext{PrevHash: msg.Block.PrevHash.Bytes(), Height: msg.Block.BlockNumber, Period: 0},
)
```

**`jmdn/messaging/broadcast.go:727`**:

```go
res, err := VerifyCertificateForRound(
	blsResults, block.BlockHash.Hex(), block.BlockNumber,
	RoundContext{PrevHash: block.PrevHash.Bytes(), Height: block.BlockNumber, Period: 0},
)
```

`config.ZKBlock.PrevHash` is a `common.Hash` (`config/ZKBlock.go:57`), so `.Bytes()` gives the
32 bytes.

> **`Period: 0` is a known gap, not an oversight.** With the period pinned at zero a timed-out round
> re-derives the *same* committee, so an offline or hostile committee can stall a height. The seed
> already accounts for period — the round loop just does not track it yet. Thread the real value
> through as soon as it does.

---

## Change 4 — the selection side (what closes F1 completely)

Changes 1–3 make the *certificate* path seed-ranked. F1 is only fully closed once the peers who
actually **sign** are chosen the same way.

In `jmdn/Sequencer/Consensus.go` around lines 285–297, the split into `MainCandidates` /
`BackupCandidates` currently preserves the order returned by `QueryBuddyNodes()` — which is the
per-node VRF shuffle. Under the flag, replace that ordering with `messaging.SelectCommittee`:

```go
if messaging.CommitteeV2Enabled {
	seated, err := messaging.SelectCommittee(messaging.RoundContext{
		PrevHash: parentHash, Height: height, Period: 0,
	})
	if err != nil {
		return fmt.Errorf("committee selection failed (fail closed): %w", err)
	}
	// MainCandidates = candidates whose PeerID is in `seated`, in seated order.
	// Everything else becomes Backup.
} else {
	// existing "first MaxMainPeers connected" logic, unchanged
}
```

### The distinction that makes this correct

`candidates` (from `QueryBuddyNodes`) and the authenticated snapshot are **two different things, and
both are needed**:

- `candidates` is the **address book** — `Buddy_PeerMultiaddr` carries the multiaddrs needed to dial.
  The snapshot has only `peer_id` and `bls_pub`, so it cannot tell you how to reach anyone.
- the snapshot is the **membership authority** — it decides who may vote.

The bug today is that the address book is *also* being used as the membership authority. The fix is
not to delete the query; it is to stop letting it decide membership.

### The patch

At `Consensus.go:285`, before the split loop:

```go
if messaging.CommitteeV2Enabled {
	seated, err := messaging.SelectCommittee(messaging.RoundContext{
		PrevHash: parentHash, Height: height, Period: 0,
	})
	if err != nil {
		// Fail closed. Never fall back to the shuffled order.
		return fmt.Errorf("CONSENSUSERROR.COMMITTEE: selection failed: %w", err)
	}

	// Index the address book by peer id.
	byID := make(map[string]PubSubMessages.Buddy_PeerMultiaddr, len(candidates))
	for _, c := range candidates {
		byID[c.PeerID.String()] = c
	}

	// Reorder: seated members first, in seated order. The existing split loop
	// then puts exactly them in MainCandidates and everyone else in Backup,
	// with its logic untouched.
	ordered := make([]PubSubMessages.Buddy_PeerMultiaddr, 0, len(candidates))
	seatedSet := make(map[string]bool, len(seated))
	for _, m := range seated {
		seatedSet[m.PeerID] = true
		if c, ok := byID[m.PeerID]; ok {
			ordered = append(ordered, c)
			continue
		}
		// OPERATIONAL ALARM: this peer is seated but we have no address for it.
		// It cannot be dialled, so it cannot vote, and the round loses a seat.
		// Repeated occurrences mean the address book and the snapshot have
		// drifted apart.
		logger().NamedLogger.Error(splitCtx, "seated committee member has no known address",
			fmt.Errorf("peer %s seated but absent from the candidate address book", m.PeerID),
			ion.String("peer", m.PeerID),
			ion.String("function", "Consensus.Start.splitCandidates"))
	}
	for _, c := range candidates {
		if !seatedSet[c.PeerID.String()] {
			ordered = append(ordered, c) // backup
		}
	}
	candidates = ordered
}
```

The split loop below it is **unchanged** — it still walks `candidates` in order and takes the first
`MaxMainPeers` connected ones. Reordering the input is the whole change.

### What I could not resolve, and why it needs your eyes

`Consensus.Start` runs **before the block exists**, so `parentHash` and `height` are not obviously in
scope there. Two ways out, and this is a design call rather than a mechanical one:

1. **Connect broadly, select per height.** At `Start`, dial every reachable eligible peer rather than
   a selected 7. Per height, `SelectCommittee` decides who votes — which the certificate path already
   does. Cleaner, and it matches the two different rhythms: connection setup is occasional, selection
   is per block. It needs `MaxMainPeers` to stop being the peer-list cap.
2. **Re-select at block time.** Move the split, or redo it, once the block header is known.

I recommend (1). It makes the peer list "everyone eligible we can reach" and leaves committee
membership entirely to `SelectCommittee`, which is where it belongs. But it touches connection
management, and that is the part of the system I have read least.

---

## Verify

```bash
cd ~/Block/avc  && go test ./committee/... ./randao/... ./vdf/... -race -cover
cd ~/Block/jmdn && go build ./... && go test ./messaging/...

# flag off: behaviour must be identical to today
JMDN_COMMITTEE_V2=0 go test ./messaging/...
# flag on: the v2 path
JMDN_COMMITTEE_V2=1 go test ./messaging/...
```

## Rollout — this is F3

The committee travels on the wire inside `ConsensusMessage`
(`Sequencer/helper/buddynodes_operations.go:73`), so old and new nodes running different selection
would exchange messages carrying different committees. That makes this a **coordinated flag flip**,
not a free deploy:

1. Ship the binary fleet-wide with `JMDN_COMMITTEE_V2` unset. Nothing changes.
2. Measure `P` — log `len(candidates)` at `Consensus.go:285`. If `P > 7`, F1 is live *today* and
   this is an incident, not a backlog item.
3. Flip on a shadow/testnet fleet, all nodes together. Watch for certificate failures.
4. Flip mainnet, all nodes together.

Reword "no fork" to "coordinated flag flip" in the LLD while you are there.
