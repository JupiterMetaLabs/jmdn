
# **🛰 Communication Protocol — Sequencer & Buddy Network**

## **Overview**

This module defines the **peer-to-peer communication layer** for the distributed consensus system.

It uses **LibP2P PubSub** for controlled message dissemination and **CRDT-based state synchronization** for eventual consistency.

---

## **⚙️ System Architecture**

Each **round** of communication begins with the **Sequencer Node**, which coordinates the setup and maintenance of a **PubSub channel** among a selected subset of nodes, known as the **Buddy Set**.

### **Components**

* **Sequencer Node** **:**
* Initiates the round and manages the PubSub session.
* Requests the buddy set from the **Buddy Selection Module**.
* Validates node participation and ensures channel reliability.
* **Buddy Selection Module** **:**
* Returns a deterministic or randomized set of **16 nodes**:
  * **13 Main Nodes**
  * **3 Backup Nodes**
* Backup nodes are used for fault tolerance and replacement.
* **Main Nodes** **:**
* Participate in the active PubSub channel.
* Handle message propagation, aggregation, and CRDT merges.
* **Backup Nodes** **:**
* Stand by to replace any failed main nodes (< 3 failures allowed).

---

## **🔁 Communication Flow**

### **1. Buddy Set Request**

1. **Sequencer sends a ****Buddy Set Request** to the  **Buddy Selection Module** **.**
2. The module responds with:

```
{
  "main_nodes": [node1, node2, ..., node13],
  "backup_nodes": [node14, node15, node16]
}
```

---

### **2. PubSub Channel Setup**

1. The Sequencer creates a **PubSub channel** for the selected 13 main nodes.
2. It sends a **PUBSUB_REQ** to all 13 nodes.
3. Each node must **subscribe** to the channel within a defined timeout.

---

### **3. Fault Handling & Node Replacement**

* If **> 3 main nodes** fail to subscribe:
  ➤ The Sequencer **cancels** the buddy set and requests a **new one** from the Buddy Selection Module.
* If **≤ 3 main nodes** fail:
  ➤ They are **replaced** with backup nodes from the buddy set.

> Only the **13 main nodes + Sequencer (14 total)** have access to the PubSub channel.

> Unauthorized nodes cannot publish or subscribe.

---

### **4. Two-Way PubSub Communication**

Once established, the **PubSub channel** operates in **bi-directional mode**:

* **All nodes** can **publish and consume** messages.
* Messages are deduplicated, retried, and aggregated through the channel.

Each node:

* Maintains a **local CRDT** to merge state updates.
* Aggregates **votes** or other consensus data from received messages.

---

### **5. Message Propagation**

* All network nodes (outside the buddy set) send their messages to their **assigned main node**.
* The **main node** then **propagates** it to the rest of the network using PubSub.
* This ensures:
  * Reduced message overhead.
  * Built-in **deduplication** and **retry** handling.

---

### **6. Timeout and CRDT Merge**

* Each round runs for **25 seconds**.
* After the timeout:
  * Nodes **synchronize states** using **CRDT merge** across the buddy set.
  * This ensures all nodes converge to a consistent state, even if some messages were delayed or dropped.

---

### **7. Finalization — BFT Voting**

* Once synchronization is complete:
  * Each node participates in **Byzantine Fault Tolerant (BFT)** voting.
  * The network reaches a consensus on the **validity of the block** (valid or invalid).
  * The Sequencer finalizes and broadcasts the result.

---

## **🧩 Reliability Mechanisms**

| **Mechanism**               | **Purpose**                                    |
| --------------------------------- | ---------------------------------------------------- |
| **Buddy Selection (13+3)**  | Ensures fault tolerance and load balancing.          |
| **PubSub Access Control**   | Restricts channel access to trusted nodes.           |
| **Deduplication & Retries** | Prevents redundant message floods.                   |
| **CRDT Merge**              | Guarantees eventual consistency even after failures. |
| **BFT Consensus**           | Ensures integrity and finality of block validation.  |

---

## **⏱ Timing Parameters**

| **Parameter**         | **Value** | **Description**            |
| --------------------------- | --------------- | -------------------------------- |
| PubSub Subscription Timeout | 5s              | Time to join the channel         |
| Round Timeout               | 25s             | Time window for message exchange |
| CRDT Merge Interval         | 25s             | Sync interval after each round   |

---

## **🔒 Security Notes**

* Only Sequencer and authorized main nodes can access the channel topic.
* Each message is signed by the sender’s public key.
* All communications are authenticated via **LibP2P secure channels** (TLS/Noise).

---

## **📜 Summary**

| **Step** | **Component** | **Action**                      |
| -------------- | ------------------- | ------------------------------------- |
| 1              | Sequencer           | Requests buddy set                    |
| 2              | Buddy Module        | Returns 13 main + 3 backup nodes      |
| 3              | Sequencer           | Creates PubSub & sends PUBSUB_REQ     |
| 4              | Nodes               | Subscribe to channel                  |
| 5              | Sequencer           | Replaces failed nodes or retries      |
| 6              | All                 | Exchange messages via PubSub          |
| 7              | All                 | Merge CRDT states after timeout       |
| 8              | All                 | Execute BFT voting to reach consensus |
