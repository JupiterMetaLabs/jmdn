package rpc

// Golden-shape tests for jmdt_getTransactionStatus.
//
// The contract that matters here is not the field list — it is that `degraded`
// is always present and always accurate. A degraded `unknown` means "we could
// not tell"; a clean `unknown` means "we have no evidence this transaction ever
// existed". A client that conflates them either polls forever or gives up on a
// transaction that is about to be mined, so the marshaller must never lose the
// distinction.

import (
	"testing"
	"time"

	"gossipnode/txstatus"
)

func TestMarshalTxStatus_MinedShape(t *testing.T) {
	out := marshalTxStatus(&txstatus.Result{
		Hash:   "0xabc",
		Status: txstatus.StatusMined,
		Source: txstatus.SourceChain,
	})

	if out["status"] != string(txstatus.StatusMined) {
		t.Errorf("status = %v, want mined", out["status"])
	}
	if out["source"] != string(txstatus.SourceChain) {
		t.Errorf("source = %v, want chain", out["source"])
	}
	if out["degraded"] != false {
		t.Errorf("degraded = %v, want false", out["degraded"])
	}
	// Optional fields are omitted, not emitted null, so a caller can tell
	// "not applicable" from "known to be empty".
	for _, k := range []string{"detail", "submitted_at", "mempool_node", "shard_id", "reason"} {
		if _, present := out[k]; present {
			t.Errorf("key %q should be omitted for a mined transaction", k)
		}
	}
}

func TestMarshalTxStatus_QueuedIncludesLocation(t *testing.T) {
	shard := int32(3)
	out := marshalTxStatus(&txstatus.Result{
		Hash:        "0xabc",
		Status:      txstatus.StatusQueued,
		Source:      txstatus.SourceMempool,
		MempoolNode: "mempool-03",
		ShardID:     &shard,
	})

	if out["status"] != string(txstatus.StatusQueued) {
		t.Errorf("status = %v, want queued", out["status"])
	}
	if out["mempool_node"] != "mempool-03" {
		t.Errorf("mempool_node = %v", out["mempool_node"])
	}
	if out["shard_id"] != int32(3) {
		t.Errorf("shard_id = %v, want 3", out["shard_id"])
	}
}

func TestMarshalTxStatus_ProcessingCarriesSubmittedAt(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	out := marshalTxStatus(&txstatus.Result{
		Hash:        "0xabc",
		Status:      txstatus.StatusProcessing,
		Source:      txstatus.SourceSubmitLog,
		SubmittedAt: &at,
	})

	got, ok := out["submitted_at"].(string)
	if !ok {
		t.Fatalf("submitted_at missing or not a string: %v", out["submitted_at"])
	}
	if got != at.Format(time.RFC3339Nano) {
		t.Errorf("submitted_at = %q, want RFC3339Nano %q", got, at.Format(time.RFC3339Nano))
	}
}

func TestMarshalTxStatus_FailedCarriesReason(t *testing.T) {
	out := marshalTxStatus(&txstatus.Result{
		Hash:   "0xabc",
		Status: txstatus.StatusFailed,
		Source: txstatus.SourceFailedStore,
		Reason: "nonce too low",
	})

	if out["status"] != string(txstatus.StatusFailed) {
		t.Errorf("status = %v, want failed", out["status"])
	}
	if out["reason"] != "nonce too low" {
		t.Errorf("reason = %v", out["reason"])
	}
}

// A degraded result must surface degraded=true AND an explanation, so an
// operator reading a log or a client reading the response can tell that the
// answer is "could not determine", not "does not exist".
func TestMarshalTxStatus_DegradedUnknownIsDistinguishable(t *testing.T) {
	degraded := marshalTxStatus(&txstatus.Result{
		Hash:     "0xabc",
		Status:   txstatus.StatusUnknown,
		Source:   txstatus.SourceNone,
		Degraded: true,
		Detail:   "mempool lookup circuit breaker is open",
	})
	clean := marshalTxStatus(&txstatus.Result{
		Hash:   "0xabc",
		Status: txstatus.StatusUnknown,
		Source: txstatus.SourceNone,
	})

	if degraded["degraded"] != true {
		t.Error("degraded result did not report degraded=true")
	}
	if degraded["detail"] == nil || degraded["detail"] == "" {
		t.Error("degraded result carried no detail")
	}
	if clean["degraded"] != false {
		t.Error("clean result reported degraded=true")
	}
	if _, present := clean["detail"]; present {
		t.Error("clean result should not carry a detail")
	}
	if degraded["status"] != clean["status"] {
		t.Fatal("test setup: both cases should share the same status")
	}
	// The two must not serialise identically, or the distinction is lost.
	if degraded["degraded"] == clean["degraded"] {
		t.Error("degraded and conclusive unknowns are indistinguishable in the response")
	}
}

// A nil result with no error should not happen, but if it does the only safe
// rendering is an INCONCLUSIVE unknown — never a clean one, which a client
// would read as proof the transaction does not exist.
func TestMarshalTxStatus_NilIsDegradedUnknown(t *testing.T) {
	out := marshalTxStatus(nil)

	if out == nil {
		t.Fatal("nil result produced a nil response")
	}
	if out["status"] != string(txstatus.StatusUnknown) {
		t.Errorf("status = %v, want unknown", out["status"])
	}
	if out["degraded"] != true {
		t.Error("a nil result must render as degraded, not as a conclusive unknown")
	}
}

// I1 guard: nothing in the status response may look like a receipt. Wallets and
// libraries treat a non-null receipt as proof of mining and read its status
// field, so a receipt-shaped payload leaking out of this method — or any
// suggestion that a caller could build one from it — would render a queued
// transaction as FAILED.
func TestMarshalTxStatus_EmitsNoReceiptFields(t *testing.T) {
	shard := int32(1)
	at := time.Now().UTC()
	for _, res := range []*txstatus.Result{
		{Hash: "0xa", Status: txstatus.StatusQueued, Source: txstatus.SourceMempool, MempoolNode: "m1", ShardID: &shard},
		{Hash: "0xa", Status: txstatus.StatusProcessing, Source: txstatus.SourceSubmitLog, SubmittedAt: &at},
		{Hash: "0xa", Status: txstatus.StatusFailed, Source: txstatus.SourceFailedStore, Reason: "rejected"},
		{Hash: "0xa", Status: txstatus.StatusUnknown, Source: txstatus.SourceNone, Degraded: true},
	} {
		out := marshalTxStatus(res)
		for _, forbidden := range []string{
			"blockNumber", "blockHash", "transactionIndex",
			"gasUsed", "cumulativeGasUsed", "logs", "logsBloom",
			"contractAddress", "effectiveGasPrice", "root",
		} {
			if _, present := out[forbidden]; present {
				t.Errorf("status %q emitted receipt field %q", res.Status, forbidden)
			}
		}
		// "status" here is our string enum, never an Ethereum 0x0/0x1 flag.
		if s, ok := out["status"].(string); ok && (s == "0x0" || s == "0x1") {
			t.Errorf("status %q rendered as an Ethereum receipt status flag", s)
		}
	}
}
