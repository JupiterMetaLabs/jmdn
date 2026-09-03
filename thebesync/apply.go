package thebesync

// Receiver-side apply for a single synced block. This is the sync-path analogue
// of messaging.ProcessBlockLocally: it does its OWN contiguous linkage (it must
// NOT use the gossip checkLinkage, which fail-closes a fresh node at tip 0) and
// then applies through the shared, hardened apply path. Verification follows the
// hybrid trust model in docs/THEBESYNC-DESIGN.md. The P2.5 state-fingerprint gate
// is added in P2.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/messaging"
	"gossipnode/messaging/BlockProcessing"

	"github.com/ethereum/go-ethereum/common"
)

// applyBlock verifies and applies one synced block, then stores it and advances
// the tip. prevNumber/prevHash identify the last applied block (the parent this
// block must link to). requireCert enforces the "once certified, always
// certified" monotonic rule: once a certified block has been applied, every later
// block MUST carry a valid certificate. hasCert reports whether THIS block
// carried a verified certificate so the caller can latch requireCert.
func applyBlock(ctx context.Context, block *config.ZKBlock, prevNumber uint64, prevHash common.Hash, requireCert bool) (hasCert bool, err error) {
	if block == nil {
		return false, fmt.Errorf("thebesync apply: nil block")
	}

	// 1. Body binding — the canonical BlockHash (and TxnsRoot, when present) must
	//    match the carried transactions. Same recompute the live receive path uses.
	if want := messaging.RecomputeBlockHashFromTxs(block.Transactions); block.BlockHash != want {
		return false, fmt.Errorf("thebesync apply: block %d body mismatch: hash %s != recomputed %s",
			block.BlockNumber, block.BlockHash.Hex(), want.Hex())
	}
	if strings.TrimSpace(block.TxnsRoot) != "" {
		want := messaging.RecomputeTxnsRoot(block.Transactions)
		if !strings.EqualFold(strings.TrimPrefix(block.TxnsRoot, "0x"), strings.TrimPrefix(want, "0x")) {
			return false, fmt.Errorf("thebesync apply: block %d txnsroot mismatch (want %s got %s)",
				block.BlockNumber, want, block.TxnsRoot)
		}
	}

	// 2. Contiguous linkage against the last applied block.
	if block.BlockNumber != prevNumber+1 {
		return false, fmt.Errorf("thebesync apply: non-contiguous block %d (expected %d)", block.BlockNumber, prevNumber+1)
	}
	if block.PrevHash != prevHash {
		return false, fmt.Errorf("thebesync apply: block %d prevHash %s != last applied %s",
			block.BlockNumber, block.PrevHash.Hex(), prevHash.Hex())
	}

	// 3. Certificate (hybrid trust). A persisted committee certificate is verified
	//    through the single shared verifier (2f+1, fail-closed). A cert-less block
	//    is accepted only while still in the legacy prefix (requireCert=false); once
	//    a cert has been seen, a cert-less block is rejected (no downgrade).
	if cert := strings.TrimSpace(block.CommitteeCertificate); cert != "" {
		var responses []BLS_Signer.BLSresponse
		if uerr := json.Unmarshal([]byte(cert), &responses); uerr != nil {
			return false, fmt.Errorf("thebesync apply: block %d malformed certificate: %w", block.BlockNumber, uerr)
		}
		res, verr := messaging.VerifyCertificate(responses, block.BlockHash.Hex(), block.ConsensusHashHex(), block.BlockNumber)
		if verr != nil {
			return false, fmt.Errorf("thebesync apply: block %d certificate verify failed (fail closed): %w", block.BlockNumber, verr)
		}
		if !res.Reached {
			return false, fmt.Errorf("thebesync apply: block %d certificate below quorum: %d/%d over committee %d",
				block.BlockNumber, res.YesVotes, res.Threshold, res.CommitteeSize)
		}
		hasCert = true
	} else if requireCert {
		return false, fmt.Errorf("thebesync apply: block %d missing required certificate (post-activation block cannot be legacy)", block.BlockNumber)
	}

	// 4. Apply -> store -> advance tip. Process BEFORE store (F-train ordering) so a
	//    failed apply never persists the block, mirroring ProcessBlockLocally.
	if perr := BlockProcessing.ProcessBlockTransactions(ctx, block, nil); perr != nil {
		return hasCert, fmt.Errorf("thebesync apply: block %d process txs: %w", block.BlockNumber, perr)
	}
	if serr := DB_OPs.StoreZKBlock(nil, block); serr != nil {
		return hasCert, fmt.Errorf("thebesync apply: block %d store: %w", block.BlockNumber, serr)
	}
	// Tip marker is monotonic and self-healing (ReconcileBlockNumber), so a marker
	// failure is non-fatal — the block is already durably stored. Matches
	// ProcessBlockLocally's non-fatal treatment.
	_, _, _ = DB_OPs.UpdateLatestBlockMonotonic(block.BlockNumber)

	// 5. Entropy side effects — the sync-path twin of ProcessBlockLocally's
	//    ApplyBlockEntropyEffects.
	//
	//    WHY THIS IS HERE AT ALL. Until this call, the sync path performed NONE
	//    of them: messaging.VerifyAndRecordPrevCert had exactly two callers,
	//    both live (broadcast.go, blockPropagation.go), and thebesync had no
	//    reference to PrevAggCert anywhere. A node that caught up through sync
	//    therefore held no aggregate for any slot it synced, so every epoch
	//    that fell back during the catch-up failed closed on that node while
	//    its peers resolved normally — silently, with no log marking it. That
	//    is the divergence this closes: the live path and the sync path now
	//    feed the SAME aggregate store.
	//
	//    Runs AFTER a successful apply+store, so a block that failed to apply
	//    never contributes entropy. Non-fatal by construction: every step
	//    inside logs and returns rather than erroring, because a certificate
	//    this node cannot fold must not abort a sync of a block whose OWN
	//    committee certificate already verified above.
	//
	//    RecordSyncedBlockEntropy, not ApplyBlockEntropyEffects: it omits epoch
	//    finalisation deliberately. See its doc comment — finalising during a
	//    bulk replay would launch one background VDF evaluation per crossed
	//    epoch boundary, for epochs whose entropy is long past useful.
	messaging.RecordSyncedBlockEntropy(block)

	return hasCert, nil
}
