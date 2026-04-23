# CLAUDE_CONSENSUS.md

This file is a full context document for Claude Code. Reading this file gives complete understanding of:
1. The consensus system overhaul already implemented (branch: `fix/Fastsync`)
2. The startup sync feature already implemented
3. The planned Sequencer FastSync-style refactor (6 phases, not yet implemented)

---

## Part 1: Consensus System — What Was Changed and Why

### Intended Vote Architecture (Important — Do Not Misread This)

The vote routing uses **deliberate scattering via consistent hashing**, not direct-to-sequencer voting. Understanding this is critical before reading the bug list.

**Flow:**
1. Sequencer/block publisher broadcasts the block to buddy nodes AND all nodes in the network
2. Each voting node hashes its own peer ID via `PickListnerWithOffset` to pick exactly **1 out of N buddy nodes** to send its vote to (deterministic — same node always picks the same buddy)
3. Votes are scattered across the N buddy nodes (~1/N of all votes per buddy)
4. Each buddy node aggregates the votes it received
5. Buddy nodes push the combined result to the sequencer
6. Sequencer converges on a final result from N buddy reports

**Why this design:**
- **Security**: votes are spread across N nodes — an attacker must compromise all N buddy nodes to intercept all votes, not just one
- **Network efficiency**: fan-in is all_nodes/N per buddy, then N→sequencer; far less congestion than all_nodes→sequencer directly
- **Compute**: sequencer processes N aggregated results, not all_nodes raw votes

`PickListnerWithOffset` is intentional and correct. Do not treat it as a bug.

---

### The Problems (Before This Branch)

The original consensus system had these bugs:

1. **Vote trigger sent to all peers, not just committee**: `BroadcastVoteTrigger` was broadcasting to all `h.Network().Peers()`. Non-committee nodes (not buddy nodes) received the trigger and attempted to vote, causing unnecessary network traffic and compute. The trigger should only go to the committee (the N buddy nodes).

2. **Hard-coded sleep**: `Consensus.go` used `time.Sleep(15 * time.Second)` to wait for votes. No event-driven signaling. If votes arrived early, the sequencer waited for no reason. If votes arrived late, they were missed.

3. **Two competing vote collection paths**: `Sequencer/Triggers/Triggers.go` had `ListeningTrigger` / `BFTTrigger` / `StartBFTConsensus` with their own timers, while `Consensus.go` had a separate pull-based collection. They conflicted.

4. **Vote results stored without block hash scope**: `Maps.StoreVoteResult` used `map[string]int8` (peerID→vote). Cross-round contamination was possible if two consensus rounds overlapped.

### What Was Changed

#### `config/PubSubMessages/Consensus.go`
Added two new fields to `ConsensusMessage`:
```go
SequencerID string  // peer.ID of the sequencer running this round
RoundID     string  // == blockHash, scopes this round uniquely
```

#### `config/PubSubMessages/Consensus_Builder.go`
Added `SetSequencerID`/`GetSequencerID`, `SetRoundID`/`GetRoundID` getters/setters. Updated `NewConsensusMessageBuilder` to copy both fields.

#### `config/PubSubMessages/vote_notification.go` (NEW FILE)
```go
type VoteNotification struct {
    PeerID    string
    BlockHash string
    Vote      int8
}
```
Used to push vote events from the AVC listener into the sequencer's vote collection loop.

#### `AVC/BuddyNodes/MessagePassing/vote_collector.go` (NEW FILE)
```go
var activeVoteCollector chan<- PubSubMessages.VoteNotification
var voteCollectorMu sync.RWMutex

func RegisterVoteCollector(ch chan<- PubSubMessages.VoteNotification)
func UnregisterVoteCollector()
func NotifyVoteCollector(notification PubSubMessages.VoteNotification)
```
The sequencer registers a channel before each consensus round. When a vote arrives in `ListenerHandler.handleSubmitVote`, it calls `NotifyVoteCollector` which pushes to that channel — non-blocking (drops if no collector registered).

#### `AVC/BuddyNodes/MessagePassing/ListenerHandler.go`
After a vote is successfully stored in the CRDT (inside `handleSubmitVote`):
```go
NotifyVoteCollector(AVCStruct.VoteNotification{
    PeerID:    remotePeer.String(),
    BlockHash: blockHash,
    Vote:      int8(voteValue),
})
```
Also fixed: `Maps.StoreVoteResult` calls were updated to pass `blockHash` as the first parameter (after the Maps API changed to be block-hash scoped).

#### `Sequencer/Triggers/Maps/vote_results.go`
Changed from `map[string]int8` to `map[string]map[string]int8` (blockHash → peerID → vote):
```go
var voteResults = make(map[string]map[string]int8)

func StoreVoteResult(blockHash string, peerID string, vote int8)
func GetVoteResultsCount(blockHash string) int
func GetAllVoteResults(blockHash string) map[string]int8
func ClearVoteResultsForBlock(blockHash string)
```
All 5 call sites updated to pass `blockHash`.

#### `Sequencer/consensus_statemachine.go`
Added to `Consensus` struct:
```go
voteNotifyCh chan PubSubMessages.VoteNotification
roundCtx     context.Context
roundCancel  context.CancelFunc
```
`roundCancel()` called in `CleanupSubscriptions()`.

#### `Sequencer/Consensus.go` — Key Changes

**SequencerID embedded in broadcast:**
```go
// After SetZKBlockData:
consensus.ZKBlockData.SetSequencerID(consensus.Host.ID().String())
consensus.ZKBlockData.SetRoundID(zkblock.BlockHash.Hex())
```

**Event-driven vote collection (replaced time.Sleep):**
```go
roundCtx, roundCancel := context.WithTimeout(trace_ctx, config.ConsensusTimeout)
consensus.roundCtx = roundCtx
consensus.roundCancel = roundCancel

voteNotifyCh := make(chan PubSubMessages.VoteNotification, config.MaxMainPeers)
consensus.voteNotifyCh = voteNotifyCh
MessagePassing.RegisterVoteCollector(voteNotifyCh)
defer MessagePassing.UnregisterVoteCollector()

for {
    select {
    case notification := <-voteNotifyCh:
        // store notification, check if enough votes collected
        if enoughVotes { goto VOTES_COLLECTED }
    case <-roundCtx.Done():
        goto VOTES_COLLECTED
    }
}
VOTES_COLLECTED:
// CollectVoteResultsFromBuddies → VerifyConsensusWithBLS → BroadcastAndProcessBlock
```

**Targeted vote trigger (committee only):**
```go
messaging.BroadcastVoteTriggerToCommittee(consensus.Host, consensus.ZKBlockData, consensus.PeerList.MainPeers)
```

**`isCommitteeMember` helper added** (package-level function before `VerifySubscriptions`):
```go
func isCommitteeMember(peerIDStr string, mainPeers []peer.ID) bool
```

**`Maps.StoreVoteResult` call fixed** to pass `blockHash` as first arg.

#### `Vote/Trigger.go`
`SubmitVote()` still uses `PickListnerWithOffset` to route votes from voting nodes to a buddy node (the correct, intentional design). The `SequencerID` field is used by **buddy nodes** to know which peer to report their aggregated result back to — not by voting nodes to bypass the buddy scatter. Falls back gracefully if `SequencerID` is empty.

#### `messaging/broadcast.go`
Added:
```go
func BroadcastVoteTriggerToCommittee(h host.Host, consensusMessage *PubSubMessages.ConsensusMessage, committeePeers []peer.ID) error
```
Same as `BroadcastVoteTrigger` but sends only to `committeePeers` instead of all `h.Network().Peers()`.

#### `Sequencer/Triggers/Triggers.go`
Updated all `Maps.StoreVoteResult`, `GetVoteResultsCount`, `GetAllVoteResults` calls to pass `blockHash` as the first argument.

### Dead Code (Should Eventually Be Removed)
The timer-based path in `Sequencer/Triggers/Triggers.go` — `ListeningTrigger`, `BFTTrigger`, `StartBFTConsensus` — is now superseded by the event-driven path in `Consensus.go`. It's inert but still compiles. Do not re-enable it.

---

## Part 2: Startup Sync — What Was Changed and Why

### The Problem
When a node restarts, it may be behind the network. Previously, syncing only happened when manually triggered via `fastsyncv2 <peer>` CLI command.

### Implementation

#### `FastsyncV2/fastsyncv2.go`
- Old `HandleSync(targetPeer string) error` body extracted into `handleSyncInternal(targetPeer string, startBlock uint64) error`
- `HandleSync` becomes: `return fs.handleSyncInternal(targetPeer, 0)` (preserves existing CLI behavior)
- `PriorSync` call in `handleSyncInternal` uses `startBlock` instead of hardcoded `0`:
  ```go
  fs.PriorRouter.PriorSync(startBlock, localBlockNum, startBlock, math.MaxUint64, targetNodeInfo, availResp.Auth)
  ```
- New method:
  ```go
  func (fs *FastsyncV2) HandleStartupSync(peerID peer.ID, addrs []multiaddr.Multiaddr) error {
      targetMultiaddr := fmt.Sprintf("%s/p2p/%s", addrs[0].String(), peerID.String())
      localBlockNum := fs.blockInfoAdapter.GetBlockDetails().Blocknumber
      startBlock := localBlockNum // 0 if fresh node → full sync
      return fs.handleSyncInternal(targetMultiaddr, startBlock)
  }
  ```
  **Important**: multiaddr string must be built as `addrs[0].String() + "/p2p/" + peerID.String()` — the protocol functions require a full multiaddr, even for already-connected peers.

#### `main.go`
After `fastSyncerV2 = initFastsyncV2(n)`, a background goroutine:
```go
if fastSyncerV2 != nil {
    goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.StartupSyncThread, func(ctx context.Context) error {
        time.Sleep(5 * time.Second) // let peer connections establish

        peers := n.Host.Network().Peers()
        if len(peers) == 0 {
            // TODO: Query seed node for available sync peers when no direct peers are connected
            log.Info().Msg("[StartupSync] No peers connected, skipping startup sync")
            return nil
        }

        for _, peerID := range peers {
            addrs := n.Host.Peerstore().Addrs(peerID)
            if len(addrs) == 0 { continue }

            log.Info().Str("peer", peerID.String()).Msg("[StartupSync] Attempting startup sync")
            if err := fastSyncerV2.HandleStartupSync(peerID, addrs); err != nil {
                log.Warn().Err(err).Str("peer", peerID.String()).Msg("[StartupSync] Failed, trying next peer")
                continue
            }
            log.Info().Str("peer", peerID.String()).Msg("[StartupSync] Sync completed successfully")
            return nil
        }

        log.Warn().Msg("[StartupSync] Failed to sync with any connected peer")
        return nil
    })
}
```

#### `config/GRO/constants.go`
Added:
```go
StartupSyncThread = "thread:startup:sync"
```

---

## Part 3: Planned Sequencer FastSync-Style Refactor (NOT YET IMPLEMENTED)

### Why This Refactor Is Needed

Six specific problems in the Sequencer module:

1. **Two competing `HandleSubmitMessageStream` implementations**
   - `StructListener.HandleSubmitMessageStream` in `AVC/BuddyNodes/MessagePassing/MessageListener.go:34` — stateless, immediately delegates to `ListenerHandler`
   - `ListenerHandler.HandleSubmitMessageStream` in `AVC/BuddyNodes/MessagePassing/ListenerHandler.go:67` — stateful (`bftContexts map[string]*BFTContext`, `sequencerPeerID`), has the actual logic
   - Both have near-identical switch statements on `ACK.Stage`. `StructListener` is a dead wrapper.

2. **No transport abstraction**
   - Current framing: JSON + `0x1E` ASCII delimiter (`config.Delimiter`)
   - `bufio.ReadString(config.Delimiter)` scattered across 11+ files
   - Write: `stream.Write([]byte(msg + string(rune(config.Delimiter))))`
   - No shared framing package

3. **Bidirectional AVC ↔ Sequencer dependency cycle**
   - `Sequencer/Consensus.go` imports `AVC/BuddyNodes/MessagePassing` (and BLS_Signer, BLS_Verifier, Service)
   - `AVC/BuddyNodes/MessagePassing/ListenerHandler.go:21` imports `gossipnode/Sequencer/Triggers/Maps`
   - Both modules are tightly coupled — hard to test or refactor independently

4. **`Consensus.go` is a 2,061-line monolith**
   Mixes: connectivity checks, PubSub management, subscription negotiation, vote collection orchestration, BLS verification, CRDT synchronization

5. **Package-level globals in `Sequencer/Triggers/Triggers.go`**
   `globalVoteData`, `subscriptionService`, `bftEngine`, `consensusCancel` — not safe for concurrent rounds

6. **JSON on hot paths**
   JSON + `0x1E` on vote submission, BFT requests = identified CPU bottleneck

### Reference Architecture: JMDN-FastSync

Located at `/Users/neeraj/CodeSection/JM/JMDN-FastSync/`.

#### pbstream (`internal/pbstream/pbstream.go`)
```go
func WriteDelimited(w io.Writer, msg proto.Message) error  // uvarint len + proto bytes
func ReadDelimited(r io.Reader, msg proto.Message) error   // read uvarint len → read bytes → unmarshal
```
Uses `bufio.NewReader` for efficient variable-length prefix reads. Language-independent.

#### Communicator interface (`core/protocol/communication/communication.go`)
Abstracts all outbound request-response patterns:
```go
type Communicator interface {
    SendPriorSync(ctx, merkle, peer, data) (*PriorSyncMessage, error)
    SendMerkleRequest(ctx, peerNode, req) (*MerkleMessage, error)
    SendHeaderSyncRequest(ctx, peerNode, req) (*HeaderSyncResponse, error)
    SendDataSyncRequest(ctx, peerNode, req) (*DataSyncResponse, error)
    SendAvailabilityRequest(ctx, peerNode, req) (*AvailabilityResponse, error)
    SendPoTSRequest(ctx, peerNode, req) (*PoTSResponse, error)
}
```

#### DataRouter (`core/protocol/router/data_router.go`)
Dispatches by `req.Phase.PresentPhase` constant:
```go
switch state {
case constants.SYNC_REQUEST:   Data := router.SYNC_REQUEST(ctx, req.Priorsync, peerNode, remote)
case constants.REQUEST_MERKLE: Data := router.REQUEST_MERKLE(ctx, merkleRange, config, remote)
// ...
}
```

#### Thin stream handlers (`core/sync/sync_protocols.go`)
Short ops:
```
defer str.Close() → SetReadDeadline → ReadDelimited(req) → extract remote peer → router.Handle*(ctx, req, remote) → SetWriteDeadline → WriteDelimited(resp)
```
Long ops: add heartbeat goroutine on a ticker; cancels compute context if heartbeat write fails.

### The 6 Phases

---

#### Phase 1: Transport Framing + SequencerCommunicator Interface

**Create `config/transport/transport.go`** (neutral location, no new dependency cycles):
```go
func WriteMessage(w io.Writer, msg *PubSubMessages.Message) error  // JSON marshal + 0x1E
func ReadMessage(r io.Reader) (*PubSubMessages.Message, error)     // ReadString(0x1E) + unmarshal
```

**Create `Sequencer/protocol/communication/communication.go`**:
```go
type VoteResultResponse struct { PeerID, BlockHash string; Vote int8; BLSSig []byte; ... }

type SequencerCommunicator interface {
    AskForSubscription(ctx context.Context, peers []peer.ID, topic string, callbackCh chan<- bool) error
    RequestVoteResult(ctx context.Context, peerID peer.ID, consensusMsg *PubSubMessages.ConsensusMessage) (*VoteResultResponse, error)
}

func New(h host.Host) SequencerCommunicator
```

**Modify**:
- `Sequencer/consensus_statemachine.go` — add `communicator SequencerCommunicator` to `Consensus` struct
- `Sequencer/Consensus.go` — replace `requestVoteResultFromBuddy` (~115 lines) + `readVoteResultResponse` with `consensus.communicator.RequestVoteResult(...)`; replace raw framing with `transport.WriteMessage`/`transport.ReadMessage`
- `Sequencer/Communication.go` — replace inline framing in `AskForSubscription` helpers
- `Sequencer/Triggers/Triggers.go` — replace inline framing in `RequestVoteResultsFromBuddies`

**Wire format stays JSON + 0x1E. AVC files untouched.**

---

#### Phase 2: StreamRouter (Inbound Dispatch)

Create `Sequencer/protocol/router/stream_router.go`:
- `StreamRouter` type that dispatches on `ACK.Stage` constant
- Each stage maps to a handler method (replacing the giant switch in `HandleSubmitMessageStream`)
- `RegisterHandler(stage string, fn HandlerFunc)` pattern

Register handlers for: `Type_AskForSubscription`, `Type_SubscriptionResponse`, `Type_SubmitVote`, `Type_VoteResult`, `Type_BFTRequest`, `Type_BFTResult`, `Type_VerifySubscription`

---

#### Phase 3: Unify Duplicate Handlers

- Delete `StructListener.HandleSubmitMessageStream` entirely (it's a dead wrapper)
- Wire `ListenerHandler.HandleSubmitMessageStream` directly as the protocol's stream handler in `AVC/BuddyNodes/MessagePassing/Streaming.go`
- Optionally delete `StructListener` type if it has no other methods
- Replace remaining raw framing in `ListenerHandler.go` with `transport.ReadMessage`/`transport.WriteMessage`

---

#### Phase 4: Split `Consensus.go` into Phase Files

Break the 2,061-line monolith:
| File | Contents |
|------|----------|
| `Sequencer/consensus_init.go` | `ConnectedNessCheck`, `warmup`, startup helpers |
| `Sequencer/consensus_subscribe.go` | `RequestSubscriptionPermission`, `startEventDrivenFlowAfterSubscriptionPermission`, `VerifySubscriptions` |
| `Sequencer/consensus_vote.go` | `BroadcastVoteTrigger`, `ProcessVoteCollection`, `CollectVoteResultsFromBuddies`, event loop |
| `Sequencer/consensus_verify.go` | `VerifyConsensusWithBLS`, `BroadcastAndProcessBlock` |
| `Sequencer/Consensus.go` | Just `Start()` orchestrating the above + package-level doc comment |

All in the same `Sequencer` package — pure file split, no interface changes.

---

#### Phase 5: Protobuf Migration

1. Inventory hot paths: `Type_SubmitVote`, `Type_BFTRequest`, `Type_VoteResult` send/receive
2. Add `proto/sequencer/v1/` schemas (message.proto, vote.proto, bft.proto)
3. Swap `config/transport/transport.go` implementation to `WriteDelimited`/`ReadDelimited` with protobuf instead of JSON + `0x1E`
4. Update message builders to output proto instead of JSON
5. Roll out message type by message type on hot paths; keep JSON for cold paths until complete

---

#### Phase 6: Decouple `Sequencer/Triggers/Maps` from AVC

The cycle source: `AVC/BuddyNodes/MessagePassing/ListenerHandler.go:21` imports `gossipnode/Sequencer/Triggers/Maps`.

Fix options:
- Move `Maps/vote_results.go` into `config/VoteMaps/` (neutral location)
- Or move it into `AVC/BuddyNodes/` since AVC is the one writing to it
- Update all import paths in both `ListenerHandler.go` and `Sequencer/Triggers/Triggers.go`

After this phase: AVC and Sequencer have no import cycle.

---

### Key Files Quick Reference

| File | Role | Lines |
|------|------|-------|
| `AVC/BuddyNodes/MessagePassing/MessageListener.go` | `StructListener` — dead wrapper over `ListenerHandler` | ~300 |
| `AVC/BuddyNodes/MessagePassing/ListenerHandler.go` | Real inbound handler — stateful BFT contexts | ~1600 |
| `AVC/BuddyNodes/MessagePassing/Streaming.go` | Stream handler registration (`SetStreamHandler`) | ~200 |
| `AVC/BuddyNodes/MessagePassing/vote_collector.go` | `RegisterVoteCollector`/`NotifyVoteCollector` push mechanism | ~50 |
| `Sequencer/Consensus.go` | Main consensus orchestration — monolith | 2061 |
| `Sequencer/consensus_statemachine.go` | `Consensus` struct definition | ~300 |
| `Sequencer/Communication.go` | `AskForSubscription`, `VerifySubscriptions` | 657 |
| `Sequencer/Triggers/Triggers.go` | Timer-based triggers + `RequestVoteResultsFromBuddies` | 717 |
| `Sequencer/Triggers/Maps/vote_results.go` | Block-hash scoped vote result storage | 73 |
| `Sequencer/Router/Router.go` | Pass-through to VerificationService | 190 |
| `Vote/Trigger.go` | `SubmitVote()` — routes to `SequencerID` or fallback | ~200 |
| `messaging/broadcast.go` | `BroadcastVoteTriggerToCommittee` | ~500 |
| `config/PubSubMessages/Consensus.go` | `ConsensusMessage` with `SequencerID`, `RoundID` | ~100 |
| `config/PubSubMessages/vote_notification.go` | `VoteNotification` struct | ~15 |
| `JMDN-FastSync/internal/pbstream/pbstream.go` | Reference: length-delimited framing | ~80 |
| `JMDN-FastSync/core/protocol/communication/communication.go` | Reference: Communicator interface | ~200 |
| `JMDN-FastSync/core/protocol/router/data_router.go` | Reference: DataRouter dispatch | ~500 |
| `JMDN-FastSync/core/sync/sync_protocols.go` | Reference: thin stream handlers | ~400 |

### Protocol Constants
- `config.SubmitMessageProtocol` = `"/p2p/submit/message/1.0.0"` — main sequencer/buddy protocol
- `config.BuddyNodesMessageProtocol` — buddy→sequencer callback (BFT results)
- `config.Delimiter` = `0x1E` (ASCII Record Separator)

### ACK Stage Constants (message type discriminator on the wire)
- `config.Type_AskForSubscription`
- `config.Type_SubscriptionResponse`
- `config.Type_SubmitVote`
- `config.Type_VoteResult`
- `config.Type_BFTRequest`
- `config.Type_BFTResult`
- `config.Type_VerifySubscription`
- `config.Type_ACK_True` / `config.Type_ACK_False`
