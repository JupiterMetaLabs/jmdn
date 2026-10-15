# Block broadcast amplification on testnet — findings, code walkthrough, proposed fix

Status: implemented on branch `fix/block-broadcast-amplification` (see §9); review pending
Date: 2026-10-15
Repo/commit analysed: `jmdn` @ `7ae1a724` (`v3base`, also `feat/consensus-flags-yaml`)
Author of analysis: Claude (Cowork), with measurements run by Doc on `testnet-seq`
Audience: a reviewer or coding agent who has not seen the investigation and needs enough context to propose and implement a fix without re-deriving it.

---

## 0. TL;DR

- Testnet blocks are 2.66 MB each (Risc0 proof is 2.66 M base64 chars). Every block is copied roughly **465 times across the fleet, ≈1.2 GB of libp2p traffic per block**. At the observed cadence (one block every ~1.8 min while the tx simulator runs) that is 60–85 Mbps continuous, matching the ~550 GB burst seen on the dashboard between 15:10 on 14 Oct and 02:07 on 15 Oct.
- About **90% of the copies are validator-to-validator re-floods** that deliver nothing new: every validator already received the block directly from the sequencer.
- Root cause is structural, not a bug in one line: three fan-out sites use `h.Network().Peers()` (every connected libp2p peer, 30 on the seq, not the committee of 15), the vote-trigger path has a 5-hop rebroadcast, and the finalized block is sent to every peer over two transports (direct stream + gossipsub FloodPublish) after it was already delivered as the proposal.
- The fix that preserves consensus semantics: deliver the proposal once to committee peers with rebroadcast disabled; deliver finalize to committee peers as header + certificate (they already hold the body); stop validator-side direct forwarding when gossip is on; keep full-block gossip for non-committee nodes. Expected: ≈45–80 MB per block. No libp2p protocol IDs change.

---

## 1. Environment this was measured on

| Item | Value |
|---|---|
| Sequencer host | `testnet-seq`, jmdn as systemd unit `jmdn` (binary `v2.0.1-592-gc7d8eae7`), orchestrator as unit `orchestrator` |
| Validators | 15 docker containers `jmdt-node-1..5` on three hosts, libp2p at `10.90.{2,3,4}.{11..15}:15000` (5 of them run on the seq host itself; the dashboard numbers include them) |
| Seq peer count | 30 (`"total": 30` on every `Block broadcast complete` log line) — 15 validators + ~15 other peers (seed nodes / peer-exchange discoveries; not yet enumerated) |
| Block payload | `payload_size_bytes: 2662810` for block 2121; `starkproof` 2,661,192 chars; one tx |
| Block cadence | 33 blocks/hour (01:00–02:00 IST) while the simulator ran; production stopped at 02:07 when the simulator was stopped (not a fault) |
| Config defaults in play | `consensus.p2p = 1` (direct + gossip), `JMDN_BLOCK_GOSSIP` default on, `config.MaxHops = 5`, `config.MaxBlockMessageBytes = 8 MiB`, `broadcastWriteTimeout = 20 s`, `ConsensusTimeout = 90 s` |
| Gossipsub options | `WithFloodPublish(true)`, `WithPeerExchange(true)`, `WithMaxMessageSize(8 MiB)`, `StrictSign` (`config/PubSubMessages/GossipSub_Helper.go:41–52`) |

### Measurements (all from the seq unless stated)

```
# fan-out size per block
journalctl -u jmdn --since "2026-10-15 01:00" --until "2026-10-15 02:10" -o cat \
  | grep 'Block broadcast complete' | grep -oE '"(success|total)": *[0-9]+' | sort | uniq -c
     38 "total": 30       20 "success": 29       17 "success": 30       1 "success": 28

# per-peer counters since jmdn start (13 Oct 17:08) — 380 ≈ blocks produced in that window
p2p_messages_sent_total{peer_id="12D3KooW…",protocol="zkblock"}   380   # ×30 peers
p2p_messages_sent_total{peer_id="12D3KooW…",protocol="broadcast"} 380   # ×30 peers

# per-socket bytes (cumulative for the life of each connection)
ss -tnpi  →  seq → each validator  bytes_sent ≈ 3.31–3.68 GB, bytes_received ≈ 77–92 MB

# host-side veth counters for the 5 validator containers on the seq host
/proc/net/dev  veth…: rx ≈ 28.5–31.1 GB (container → host, i.e. validator *transmit*), tx ≈ 5.9–6.0 GB

# loopback on the seq (separate finding, §7)
lo: rx=tx=1,284,099,573,762 bytes (1.28 TB) over ~41 h
```

Arithmetic check, 380 blocks:
- seq → validator: 3.3 GB / 380 ≈ 8.7 MB/block ≈ **3.3 × 2.66 MB** → three full copies per validator per block (stages 2, 5a, 5b below).
- validator transmit: 29 GB / 380 ≈ 76 MB/block ≈ **28 × 2.66 MB** → two full copies to each of 14 sibling validators (stages 2b and 6a below).
- validator → seq ≈ 80 MB total → validators do **not** send blocks back to the seq (dedup works in that direction).

The seq-side sends (3 × 30 × 2.66 MB ≈ 240 MB/block) plus validator sends (15 × 2 × 14 × 2.66 MB ≈ 1.1 GB/block) ≈ 1.3 GB/block, consistent with ~550 GB over ~400 blocks.

---

## 2. Data structures involved

```go
// messaging/broadcast.go:35 — generic flood-broadcast envelope (vote trigger uses this)
type BroadcastMessageStruct struct {
	ID        string `json:"id"`        // sha256(sender-content-timestamp)[:16]
	Sender    string `json:"sender"`    // origin peer ID — the ONLY peer excluded from re-flood
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
	Hops      int    `json:"hops"`      // re-flooded while Hops < config.MaxHops (5)
	Type      string `json:"type"`      // "vote_trigger", timeout types, "general"
	Data      string `json:"data"`      // for vote_trigger: json.Marshal(ConsensusMessage) → contains the full block
}

// config/PubSubMessages/Consensus.go:58 — what the vote trigger carries
type ConsensusMessage struct {
	ZKBlock      *config.ZKBlock               // full block incl. StarkProof (2.66 MB)
	Buddies      map[int]Buddy_PeerMultiaddr   // THE COMMITTEE: peer IDs + multiaddrs — available at proposal time
	EndTimeout   time.Time
	StartTime    time.Time
	InteriumTime time.Time
	TotalNodes   int
	SequencerID  string
}

// config/ZKBlock.go:345 — finalized-block envelope (direct stream and gossip)
type BlockMessage struct {
	ID        string            `json:"id"`
	Sender    string            `json:"sender"`
	Timestamp int64             `json:"timestamp"`
	Nonce     string            `json:"nonce"`
	Block     *ZKBlock          `json:"block"`     // full block again (2.66 MB)
	Hops      int               `json:"hops"`
	Type      string            `json:"type"`      // "zkblock"
	Data      map[string]string `json:"data"`      // block_hash, block_number, txn_count, proof_hash, status, timestamp, bls_results (JSON string)
}
```

Dedup state:
- Broadcast path: `seenMessages map[string]time.Time` keyed by `BroadcastMessageStruct.ID`, checked in `HandleBroadcastStream` (`broadcast.go:142–149`). Prevents re-*processing* and re-*forwarding* of a second copy; does not prevent *receiving* it.
- Block path: `dedupMessageCache` (LRU) keyed by `getBlockDedupID(msg)` = ConsensusHash-based (`blockPropagation.go:253`). The seq marks its own finalized block processed before sending (`broadcast.go:664`), so copies that come back are dropped at `HandleReceivedBlockMessage` (`:323–331`).

Committee source (already exists, used by the certificate verifier):
- `messaging.AuthorizedCommittee()`, `messaging.EligibleCommitteePeerIDs()`, `authenticatedCommittee()` in `messaging/consensus_hardening.go:324–335, 500`. Backed by the seedNode committee snapshot (`SetCommitteeEligibilitySource`, `:128`) minus the `block_buddy` blocklist.
- On the seq at proposal time, `ConsensusMessage.Buddies` is populated by `Sequencer/Metadata/Metadata.go:16` from the buddy selection (`Sequencer/Triggers/Triggers.go`).

---

## 3. Lifecycle of one block, with the code that moves bytes

```mermaid
sequenceDiagram
    participant O as Orchestrator
    participant S as Sequencer jmdn
    participant V as Validator (×15)
    participant P as Other peers (×15)

    O->>S: 1. POST /api/process-block (2.66 MB, loopback)
    Note over S: 2. BroadcastVoteTrigger: Data = json(ConsensusMessage incl. full ZKBlock)
    S->>V: vote_trigger 2.66 MB ×15
    S->>P: vote_trigger 2.66 MB ×15
    Note over V: 2b. HandleBroadcastStream → forwardBroadcast (hops<5, all peers but origin)
    V->>V: vote_trigger re-flood 2.66 MB ×14 per validator
    V->>S: 3. BLS vote (bytes) direct + gossip + CRDT
    Note over S: 4. 2f+1 verified, ProcessBlockLocally
    Note over S: 5. BroadcastBlockToEveryNodeWithExtraData
    S-->>V: 5b. gossipsub FloodPublish zkblock 2.66 MB
    S-->>P: 5b. gossipsub FloodPublish zkblock 2.66 MB
    S->>V: 5a. direct zkblock 2.66 MB ×15
    S->>P: 5a. direct zkblock 2.66 MB ×15
    Note over V: 6. HandleReceivedBlockMessage: dedup, admit, apply; forward=true on direct copy
    V->>V: 6a. forwardBlock 2.66 MB ×14 per validator
```

### Stage 1 — proposal arrives (loopback, 1 copy)
Orchestrator → `POST http://localhost:16050/api/process-block` with the full block. jmdn stores it as `consensus.ZKBlockData` and enters the consensus flow (`Sequencer/Consensus.go:1112`, `:1352`).

### Stage 2 — vote trigger = the proposal transport (seq → 30 peers, 30 copies, ≈80 MB)
`Sequencer/Consensus.go:1352` → `messaging.BroadcastVoteTrigger(consensus.Host, consensus.ZKBlockData)`.

```go
// messaging/broadcast.go:475–600 (abridged)
func BroadcastVoteTrigger(h host.Host, consensusMessage *PubSubMessages.ConsensusMessage) error {
	...
	consensusData, err := json.Marshal(consensusMessage)          // includes ZKBlock + StarkProof
	msg := BroadcastMessageStruct{
		Sender: h.ID().String(), Content: "Vote trigger broadcast - initiate voting process",
		Timestamp: now.Unix(), Hops: 0, Type: "vote_trigger", Data: string(consensusData),
	}
	msg.ID = generateMessageID(msg.Sender, msg.Content, now.Unix())
	markMessageSeen(msg.ID)                                         // seq will ignore copies that come back
	msgBytes, _ := json.Marshal(msg); msgBytes = append(msgBytes, '\n')

	peers := h.Network().Peers()                                    // <-- ALL connected peers, not committee
	for _, peerID := range peers {                                  // one goroutine per peer
		stream, err := h.NewStream(ctx, peer, config.BroadcastProtocol)
		_ = stream.SetWriteDeadline(time.Now().Add(broadcastWriteTimeout))
		_, err = stream.Write(msgBytes)
		metrics.MessagesSentCounter.WithLabelValues("broadcast", peer.String()).Inc()
	}
	wg.Wait()
	// logs "Vote trigger broadcast complete" success/total
}
```

Receiver side — the validator votes on the block **from this payload**; there is no earlier delivery of the proposed block:

```go
// messaging/broadcast.go:387–473 (abridged)
func handleVoteTriggerBroadcast(msg BroadcastMessageStruct) {
	var consensusMessage PubSubMessages.ConsensusMessage
	json.Unmarshal([]byte(msg.Data), &consensusMessage)
	... // populate buddy list from consensusMessage.Buddies, cache message for CRDT multiaddr lookup
	voteTrigger := Vote.NewVoteTrigger()
	voteTrigger.SetConsensusMessage(&consensusMessage)             // signs consensusMessage.ZKBlock
	voteTrigger.SubmitVote()                                        // direct SubmitMessageProtocol + gossip
}
```

Consequence for any fix: the committee must still receive the full proposal exactly once. Replacing the payload with a hash alone would leave validators with nothing to vote on.

### Stage 2b — vote-trigger re-flood (15 validators × 14 peers = 210 copies, ≈560 MB, zero information)

```go
// messaging/broadcast.go:142–199 (abridged)
func HandleBroadcastStream(stream network.Stream) {
	...
	if isMessageSeen(msg.ID) { return }          // second copy dropped HERE — after it was fully received
	markMessageSeen(msg.ID)
	if msg.Type == "vote_trigger" { handleVoteTriggerBroadcast(msg) }
	...
	if msg.Hops < config.MaxHops {               // MaxHops = 5
		msg.Hops++
		forwardBroadcast(hostInstance, msg)      // re-flood: all peers except msg.Sender (the origin)
	}
}

// messaging/broadcast.go:223–280 (abridged)
func forwardBroadcast(h host.Host, msg BroadcastMessageStruct) {
	peers := h.Network().Peers()                 // <-- ALL connected peers
	msgBytes, _ := json.Marshal(msg); msgBytes = append(msgBytes, '\n')
	for _, peerID := range peers {
		if peerID.String() == msg.Sender { continue }   // only the origin is skipped
		stream, err := h.NewStream(ctx, peerID, config.BroadcastProtocol)
		stream.Write(msgBytes)                   // 2.66 MB to every sibling
	}
}
```

Every validator already received the trigger from the seq, so every one of these 210 copies is dropped by `isMessageSeen` on arrival — after the bytes crossed the wire. This path exists for genuinely small gossip (timeout votes, general broadcasts); it was never sized for a 2.66 MB payload.

### Stage 3 — votes (small)
`Vote.SubmitVote()` sends the BLS signature (domain-bound to block hash, chain ID, height) to the seq by direct `SubmitMessageProtocol`, plus a gossip publish; CRDT sync is a fallback. Bytes per vote. This is why seq inbound from validators is only ~80 MB over 380 blocks.

### Stage 4 — finalize on the seq
`Sequencer/consensus_statemachine.go:455–476`: 2f+1 over the authenticated committee → `ProcessBlockLocally` → on success `BroadcastBlockToEveryNodeWithExtraData`. The rejected-round branch (`:505–517`) calls the same function with the full block as a "rejected-block notice" — another 2.66 MB × 30 per failed round.

### Stage 5 — finalized zkblock over two transports (seq → 30 peers, up to 60 copies, ≈160 MB)

```go
// messaging/broadcast.go:606–731 (abridged)
func BroadcastBlockToEveryNodeWithExtraData(h host.Host, block *config.ZKBlock, result bool,
	extra map[string]string, bls []BLS_Signer.BLSresponse) error {
	peers := h.Network().Peers()                 // <-- ALL connected peers
	data := map[string]string{ "block_hash": ..., "block_number": ..., "txn_count": ..., "proof_hash": block.ProofHash,
	                           "status": block.Status, "timestamp": ... }
	if len(bls) > 0 { data["bls_results"] = string(json.Marshal(bls)) }   // the certificate — small
	msg := config.BlockMessage{ Sender: h.ID().String(), Nonce: nonce, Block: block, Type: "zkblock", Hops: 0, Data: data }
	msg.ID = generateBlockMessageID(msg.Sender, nonce, now)
	markMessageProcessed(getBlockDedupID(msg))   // seq ignores its own copies coming back

	PublishBlockGossip(msg)                      // 5b: gossipsub, FloodPublish → every subscribed peer gets it from the seq directly

	if EnableBlockGossip && !directBlockPropagationEnabled() { return nil }   // only when consensus.p2p == 0
	// 5a: direct stream to every peer (default path, consensus.p2p == 1)
	msgBytes, _ := json.Marshal(msg); msgBytes = append(msgBytes, '\n')
	for _, peerID := range peers {
		stream, err := h.NewStream(ctxWithTimeout, peer, config.BlockPropagationProtocol)
		stream.Write(msgBytes)
		metrics.MessagesSentCounter.WithLabelValues("zkblock", peer.String()).Inc()
	}
	wg.Wait()
	// logs "Block broadcast complete (with extra data)" success/total
}
```

`directBlockPropagationEnabled()` (`messaging/blockgossip.go:40`) returns true when `settings.Consensus.P2P >= 1`; the default is 1 (`config/settings/defaults.go:251`). Gossip is wired in `blockgossip/blockgossip.go:60` (publisher = `json.Marshal(BlockMessage)` wrapped in a `PubSubMessages.Message`; subscriber calls `HandleReceivedBlockMessage(msg, "gossip:"+sender, false)`).

For the 15 validators this is the **second** full delivery of a block they already hold from stage 2; for the direct+gossip pair it is the second and third.

### Stage 6 — validator forward (15 × 14 = 210 copies, ≈560 MB, zero information)

```go
// messaging/blockPropagation.go:323–410 (abridged)
func HandleReceivedBlockMessage(msg config.BlockMessage, remotePeer string, forward bool) {
	messageID := getBlockDedupID(msg)
	if isMessageProcessed(messageID) { /* "Duplicate message received" */ return }
	... // admitZKBlock certificate gate (fail-closed), equivocation check, apply + store
	markMessageProcessed(messageID)
	// comment at :390 says "The direct re-flood is OFF by default (gossip-only)" — this is STALE:
	if forward && directBlockPropagationEnabled() && msg.Hops < config.MaxHops {   // true at default p2p=1
		msg.Hops++
		forwardBlock(globalHost, msg)            // :823 — peers := h.Network().Peers(); 2.66 MB to each
	}
}
```

`forward` is `true` for the direct-stream copy (`HandleBlockStream`, `:313`) and `false` for the gossip copy (`blockgossip.go:71`). Whichever copy arrives first wins the dedup; on testnet the direct copy usually arrives first (same goroutine burst from the seq), so each validator forwards to its 14 siblings, each of which drops it as a duplicate on arrival. Validators do not forward to the seq because the seq already marked the ID processed — hence the ~80 MB validator→seq figure.

### Per-block totals (15 validators, 30 seq peers)

| Stage | Path | Copies | Bytes | Needed? |
|---|---|---|---|---|
| 2 | vote trigger, seq → 30 peers | 30 | 80 MB | 15 of 30 (committee only) |
| 2b | vote trigger re-flood, 15 × 14 | 210 | 560 MB | no |
| 5a | zkblock direct, seq → 30 | 30 | 80 MB | no for committee; non-committee covered by 5b |
| 5b | zkblock gossip FloodPublish, seq → ≤30 | ≤30 | ≤80 MB | yes for non-committee nodes |
| 6a | zkblock forward, 15 × 14 | 210 | 560 MB | no |
| | **Total** | **≈465** | **≈1.2 GB** | |

---

## 4. Proposed changes (jmdn, one branch off `v3base`)

Ordered by traffic removed. Items 1, 3, 4, 5 are backward compatible node-by-node; item 2 needs a flag and fleet-wide rollout.

### 4.1 Vote trigger: committee only, non-forwardable (removes 2b, halves 2)
- In `BroadcastVoteTrigger`, replace `h.Network().Peers()` with the committee from `consensusMessage.Buddies` (peer IDs are right there), falling back to `messaging.EligibleCommitteePeerIDs()` if `Buddies` is empty. Intersect with connected peers; log committee members that are not connected (they will miss the round — that is already the case today, just invisible).
- Mark the message non-forwardable. Simplest: set `Hops = config.MaxHops` before marshalling so `HandleBroadcastStream`'s `msg.Hops < config.MaxHops` check is false on every receiver, no wire-format change. Cleaner: add `NoRebroadcast bool \`json:"no_rebroadcast,omitempty"\`` to `BroadcastMessageStruct` and honour it in `HandleBroadcastStream` — old receivers ignore the unknown field and would still re-flood, so the `Hops` trick is the one that works on a mixed fleet.
- Keep the full block in `Data` — it is the proposal.

### 4.2 Finalize to committee as header + certificate (removes 5a for committee)
- Validators already hold the proposed block from 4.1. Cache it per height/ConsensusHash in `handleVoteTriggerBroadcast` (there is already a consensus-message cache for CRDT multiaddr lookup at `broadcast.go:442`; extend or reuse it, with TTL ≥ `ConsensusTimeout`).
- In `BroadcastBlockToEveryNodeWithExtraData`, for committee peers send a `BlockMessage` with `Block = nil` and `Data` carrying `block_hash`, `block_number`, `proof_hash`, `consensus_hash`, `bls_results`, `status`. Add `Type: "zkblock_cert"` so receivers can branch explicitly.
- In `HandleReceivedBlockMessage`, when `Block == nil && Type == "zkblock_cert"`: look up the cached proposal by consensus hash, verify the certificate against the hash of the **cached** block (not the message), then run the same admit/apply path. If no cached proposal matches (validator missed the trigger, restarted mid-round): request the block by hash from the sender over `BlockPropagationProtocol` (small request/response to add), or fall through to the existing FastSync catch-up. Log and count the miss.
- Non-committee peers continue to get the full `BlockMessage` via gossip only (5b). Keep direct full-block sends only for peers that are neither committee nor gossip-subscribed, if that set exists at all (today `PublishBlockGossip` reaches every subscribed peer because of `FloodPublish`).
- Gate behind `consensus.flags.finalize_cert_only` (default off) until every validator runs the new binary; an old validator receiving `Block == nil` would fail to apply.

### 4.3 `forwardBlock` off when gossip is on (removes 6a)
- Make `blockPropagation.go:390` match its own comment: `if forward && directBlockPropagationEnabled() && !EnableBlockGossip && msg.Hops < config.MaxHops`. With gossip on (default), the mesh re-propagates with its own dedup; direct re-forwarding is redundant.
- If direct-only mode (`JMDN_BLOCK_GOSSIP=0`) must keep a forward for resilience, restrict `forwardBlock` to committee peers that are not the sender and not the origin (`msg.Sender`).

### 4.4 Rejected-block notice without the block
- `consensus_statemachine.go:517`: send `Type: "zkblock_rejected"` with hash/height/reason in `Data` and `Block = nil`. Receivers today only use it to discard (`"Received consensus REJECTION for block - discarding"` in `blockPropagation.go`); confirm that handler does not need the body.

### 4.5 Peer hygiene
- Drop `pubsub.WithPeerExchange(true)` in `GossipSub_Helper.go:46` so the seq stops accreting non-validator peers via PX; the ~15 non-committee peers on the seq should be identified first (`ss -tnpi` + `p2p_messages_sent_total` peer IDs) — if they are seed nodes/explorers that only need headers, they should not be in the block fan-out at all.
- `FloodPublish` can stay; it only affects the publisher's own send and is bounded by peer count.

### Expected result
Seq per block: 15 × 2.66 MB (proposal) + 30 small finalize/cert messages + one gossip publish reaching ≤15 non-committee peers → ≈45–80 MB. Validators per block: votes only (bytes). Fleet total drops from ≈1.2 GB to under 100 MB (≥12×).

### Rollout
1. Ship 4.1, 4.3, 4.4, 4.5 together; deploy seq first, then validators. Old validators still work (they just receive fewer copies).
2. Ship 4.2 behind the flag; enable only after all 15 validators are upgraded. Watch `BlocksRejectedCounter` and the cache-miss counter during the first hour.

---

## 5. Immediate mitigation without code (partial)

Set `consensus.p2p: 0` (gossip-only) on validators. On a validator this makes `directBlockPropagationEnabled()` false and disables 6a today; on the seq it also disables 5a. It does **not** touch 2b (vote-trigger re-flood), which is the largest single component, so this buys ~50% at most. Note the seq must keep gossip enabled and wired for this to be safe (it is on testnet).

---

## 6. Verification plan

Before/after on the seq (same simulator load):
```sh
journalctl -u jmdn -o cat | grep -E 'Vote trigger broadcast complete|Block broadcast complete' \
  | grep -oE '"total": *[0-9]+' | sort | uniq -c            # expect committee size (15), not 30
curl -s localhost:<metrics>/metrics | grep p2p_messages_sent_total | grep -c zkblock   # only committee peer IDs increment
ss -tnpi | awk '/users:/{peer=$5} /bytes_sent/{...}'       # seq → validator bytes per block ≈ 2.7 MB, was 8.7 MB
```
On a validator (logs are in the container's log files, not stdout — `docker logs` returned nothing; check `docker inspect -f '{{.HostConfig.LogConfig.Type}}'` and `/app/logs/node.log`):
```sh
grep -cE 'Rebroadcasting message|Forwarding validated ZK block to peers' node.log   # expect 0 for block-bearing messages
awk '/veth/{print $1, "tx="$10}' /proc/net/dev                                        # per-block validator tx < 1 MB, was ~76 MB
```
Consensus invariants that must not regress: votes per round = committee size, 2f+1 reached within the same latency, `BlocksRejectedCounter` flat, no `Failed to submit vote from broadcast trigger`, FastSync catch-up still succeeds on a restarted validator.

Unit tests to add: `BroadcastVoteTrigger` peer selection (committee ∩ connected), `HandleBroadcastStream` does not call `forwardBroadcast` for `Hops >= MaxHops`, `HandleReceivedBlockMessage` cert-only path hits the proposal cache and the fetch fallback, `forwardBlock` not invoked when gossip is on.

---

## 7. Related findings outside this change (for separate tickets)

1. **Orchestrator logs the full 2.6 MB proof to journald on every block.** `🔍 Block template summary` prints `starkproof:QVBSRg…` in full; other lines repeat it. ≈1.3 GB/day of journal and it makes `journalctl | grep` unusable (a 2.6 M-char base64 string matches `409`, `error`, etc. by chance). Log `proof_hash` and the length only.
2. **Loopback on the seq: 1.28 TB in ~41 h (≈8.7 MB/s).** Not the NIC problem, not yet attributed. Candidates: orchestrator → jmdn block POST (2.66 MB/block), 1-second `/api/latest-block` and `eth_blockNumber`/`eth_gasPrice` polling if they return bodies with proofs, ThebeDB/Postgres reads of block rows that include the proof column (SyncMonitor fingerprint). Attribute with `ss -tnp state established | awk '$4 ~ /127\.0\.0\.1/{split($4,a,":"); print a[2]}' | sort | uniq -c | sort -rn`.
3. **Stale comment** at `messaging/blockPropagation.go:390` ("direct re-flood is OFF by default") contradicts `defaults.go:251` (`P2P: 1`). Fixed by 4.3 either way.
4. **OTEL exporter** dials `10.50.0.2:4317` / `127.0.0.1:4317` every 10 s and fails ("no route to host" / "connection refused") on both jmdn and orchestrator — noise in the journal, probably a dead collector config.

---

## 8. Open questions for the reviewer

1. Committee source for fan-out: `ConsensusMessage.Buddies` (set by the seq from buddy selection) vs `messaging.EligibleCommitteePeerIDs()` (seedNode snapshot minus blocklist). Which is authoritative for "who must receive the proposal"? They should agree; if they can differ, fan out to the union.
2. Who are the ~15 non-committee peers on the seq? Full nodes that need every block (then gossip full-block stays), or seed/explorer nodes (then they should not be subscribed to `PubSub_BlockPropagation` at all)?
3. For 4.2, is a fetch-by-hash RPC over `BlockPropagationProtocol` acceptable, or should the miss path go straight to FastSync catch-up?
4. Is there any consumer of the rejected-block notice that reads `Block` (4.4)?
5. Should `MaxBlockMessageBytes` (8 MiB) and the gossip max message size be revisited once proofs are no longer flooded, or kept as the hard cap?

---

## 9. Implementation status (branch `fix/block-broadcast-amplification`, off `v3base` @ 7ae1a724)

| Item | Status | Where |
|---|---|---|
| 4.1 vote trigger → committee only, dial unconnected members from `Buddies` multiaddr, `Hops = MaxHops`; receivers never re-flood `vote_trigger` | done | `messaging/broadcast.go`: `voteTriggerTargets`, `BroadcastVoteTrigger`, `HandleBroadcastStream` |
| 4.2 certificate-only finalize (`zkblock_cert`, header + `bls_results`, no proof) to voters/committee; receiver substitutes the cached proposal (`LookupConsensusMessageByBlockHash`), ignores the envelope if it has no proposal and waits for gossip | done, **flag default off** | `messaging/broadcast.go` (`FinalizeCertOnly` branch, `finalizeCommitteeTargets`, `stripProof`), `messaging/blockPropagation.go` (top of `HandleReceivedBlockMessage`), flag `JMDN_FINALIZE_CERT_ONLY` / `consensus.flags.finalize_cert_only` |
| 4.3 `forwardBlock` only when gossip is off | done | `messaging/blockPropagation.go` (`!EnableBlockGossip` added to the forward gate) |
| 4.4 rejected-block notice without the proof | done | `messaging/broadcast.go` (`!result` branch sends `stripProof(block)`, direct only) |
| 4.5 `WithPeerExchange(true)` removed | done | `config/PubSubMessages/GossipSub_Helper.go` |
| metrics | `p2p_messages_sent_total{protocol=...}` now labels `zkblock` / `zkblock_cert` separately; new rejection reason `cert_only_no_proposal` | `messaging/broadcast.go` `sendBlockMessageDirect` |
| tests | `messaging/broadcast_amplification_test.go` (7 tests: committee targeting, dial-on-demand, no re-flood, MaxHops stamp, stripProof, finalize targets, cert-only without proposal) | pass; full `./messaging`, `./config/...`, root package pass |

Behaviour with the flag OFF (default): stages 2b, 6a removed; stage 2 goes to the committee (7 on testnet) instead of 30; stage 5a/5b unchanged (full block, direct + gossip, to all peers). Per block on the seq: 7 + 30 + ≤30 full copies ≈ 180 MB (was ≈240 MB + 1.1 GB of validator re-flood). Fleet total ≈ 180 MB (was ≈1.2 GB).

Behaviour with the flag ON (after all validators run this build): stage 5a becomes a few-KB `zkblock_cert` to voters; full block reaches non-committee peers via gossip only. Seq per block ≈ 7 × 2.66 MB + gossip ≈ 20–60 MB.

Not done / deliberately left:
- Non-committee peers still receive the full block over gossip (`FloodPublish`). Whether the ~15 non-validator peers should be subscribed to `PubSub_BlockPropagation` at all is open question §8.2.
- `consensus.p2p` default stays 1. With gossip on, 5a is still a duplicate of 5b for every peer; switching the default to 0 (gossip-only) is a one-line config change once the fleet is comfortable relying on gossip.
- The consensus-message cache (`cacheConsensuMessage`) is never evicted (noted at `ListenerHandler.go:1621`); with 2.66 MB proposals that is ~1 GB RSS per 380 blocks on every validator. Separate ticket; it predates this change but 4.2 relies on the cache, so eviction must keep at least the last few heights.
- Build in the sandbox used `replace` directives to local checkouts of the private modules pinned at the tags in `go.mod` (`avc v0.1.0-v3base.5`, `ThebeDB v0.2.0-v3base.2`, `JMDN-FastSync v0.1.0-v3base.3`, `JMDN_Merkletree b819e61`); `go.mod`/`go.sum` are unchanged in the commit.
