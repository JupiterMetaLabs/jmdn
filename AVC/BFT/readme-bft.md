# BFT Implementation Documentation

## 📁 File Structure

### **Core BFT Logic**
```text
pkg/bft/
├── types.go              → Defines what things are (Config, Decision, etc.)
├── bft.go                → Main BFT boss (starts everything)
├── engine.go             → Does PREPARE & COMMIT phases
├── byzantine.go          → Catches cheaters
└── simulator.go          → Fake network for testing
```

### **Communication** (How They Talk)
```text
pkg/bft/
├── gossipsub_messenger.go → Real network messenger (Production)
├── buddy_service.go       → Buddy's gRPC server (listens for jobs)
├── sequencer_client.go    → Sequencer tells buddies "start!"
└── sequencer_server.go    → Sequencer collects results
```

### **Network Setup**
```text
pkg/network/
└── libp2p_setup.go       → Creates the network connections
```

### **Testing**
```text
pkg/bft/
└── bft_test.go           → Unit tests
```

---

## 🎬 The Flow (Step by Step)

### **Step 1: Setup** (Before anything happens)
```text
Each Buddy Node:
  1. Start libp2p (network connection)
  2. Start GossipSub (group chat)
  3. Start gRPC server (phone line for Sequencer)
  
Sequencer:
  1. Start gRPC server (to receive results)
  2. Connect to Peer Directory
```

### **Step 2: Sequencer Triggers BFT** (New block needs validation)
```text
Sequencer:
  1. Picks 13 buddies using VRF
  2. Creates GossipSub topic: "round-123"
  3. Sends gRPC message to all 13:
     "Join topic 'round-123' and run BFT!"
```

**File used:** `sequencer_client.go` → `InitiateBFTRound()`

---

### **Step 3: Buddy Receives Request**
```text
Each Buddy:
  1. gRPC server receives message
  2. "Oh! Sequencer wants me to run BFT!"
```

**File used:** `buddy_service.go` → `InitiateBFT()`

---

### **Step 4: Buddy Joins GossipSub**
```text
Each Buddy:
  1. Joins GossipSub topic "round-123"
  2. Waits for all 13 buddies to join
  3. Network mesh forms (everyone connected)
```

**File used:** `gossipsub_messenger.go` → `NewGossipSubMessenger()`

---

### **Step 5: BFT Runs** (The Main Event!)

#### **Phase 3a: PREPARE**
```text
Each of 13 buddies:
  1. Decides ACCEPT or REJECT (based on 87 votes)
  2. Broadcasts PREPARE message on GossipSub
  3. Listens for PREPARE from other 12 buddies
  4. Counts votes: Need 9+ to match
```

**Files used:** 
- `bft.go` → `RunConsensus()`
- `engine.go` → `runPrepare()`
- `gossipsub_messenger.go` → `BroadcastPrepare()`, `ReceivePrepare()`

#### **Phase 3b: COMMIT**
```text
Each buddy (if 9+ agreed in PREPARE):
  1. Creates COMMIT with proof of 9+ PREPARE votes
  2. Broadcasts COMMIT on GossipSub
  3. Listens for COMMIT from others
  4. Once 9+ COMMIT received → Consensus reached!
```

**Files used:**
- `engine.go` → `runCommit()`
- `gossipsub_messenger.go` → `BroadcastCommit()`, `ReceiveCommit()`
- `byzantine.go` → Checks for cheaters

---

### **Step 6: Report Back to Sequencer**
```text
Each Buddy:
  1. BFT finished (SUCCESS or FAIL)
  2. Sends result via gRPC to Sequencer
  3. "Hey boss, my answer is ACCEPT!"
```

**File used:** `buddy_service.go` → `reportResult()`

---

### **Step 7: Sequencer Collects Results**
```text
Sequencer:
  1. Receives results from buddies (via gRPC server)
  2. Waits until 9+ results received
  3. Final decision: ACCEPT or REJECT
  4. "Block is ACCEPTED! ✅"
```