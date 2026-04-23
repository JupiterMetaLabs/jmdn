# BLS Router – Quick Reference

**Purpose:**
High-level API for threshold (multi-node) BLS signatures, voting, and aggregation.

**When to Use:**
- Distributed consensus/voting (e.g., block or proposal voting)
- Threshold/collective signing (e.g., 2-of-3, 5-of-7 approval)
- Anywhere you need a cryptographically verifiable group agreement

---

## Main API (slightly detailed)

- `GenerateKeyPair() ([]byte, []byte, error)` – Generate a BLS private/public keypair for a participant (returns priv, pub, error).
- `NewBLSRouter() *BLSRouter` – Create a router instance to manage signers, signatures, and aggregation.
- `AddSigner(id string, pub, priv []byte)` – Register a node: unique string ID, their public key, and their private key (or nil if verify-only).
- `RemoveSigner(id string)` – Unregister/remove a node by ID (only needed for dynamic setups).
- `SignMessage(agree bool, msg string, signerID string)` – Sign a message if the signer agrees. Returns signature (string) or error if not agreed.
- `CollectAndAggregateSignatures(msgKey, msg string, signerIDs []string)` – Gather and aggregate signatures for a given vote/messageKey. Returns group signature and the pubkeys used.
- `VerifyAggregated(msgKey, msg string, aggSig []byte)` – Check the group signature: returns (isValid, signedIDs, unsignedIDs, error).

---

## Threshold and Aggregation Explained

- The router enforces a threshold (default=3): aggregation and verification only succeed if enough signerIDs provide valid signatures for the same message (msgKey, msg, and set must match).
- If not enough valid shares, or messages differ, aggregation/verification fails.

---

## Usage: Distributed Voting Example

```go
// Setup
priv, pub, _ := Router.GenerateKeyPair()
router := Router.NewBLSRouter()
router.AddSigner("node1", pub, priv)

// Node signs
sig, _ := router.SignMessage(true, "MyVote", "node1")

// Aggregator collects and combines
aggSig, pubs, _ := router.CollectAndAggregateSignatures("votekey", "MyVote", []string{"node1","node2"})

// Anyone verifies the aggregate
ok, signed, unsigned, err := router.VerifyAggregated("votekey", "MyVote", aggSig)
if ok { fmt.Println("Majority signature valid! By:", signed) }
```

---

**Notes:**
- All public keys must be registered before use, and all signatures MUST sign the identical message bytes for the same msgKey.
- On threshold failure, aggregation/verification will return an error.
- Use only the Router API (not blssign) for all BLS workflow.

Cut, paste, and adapt the above for any collective BLS-protected operation in your distributed protocol.