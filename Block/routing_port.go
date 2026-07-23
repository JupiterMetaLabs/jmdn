package Block

import (
	"context"

	"gossipnode/config"
)

// MempoolRouter is the port through which jmdn talks to the Mempool Routing
// Engine (MRE). Consumers (RPC facade, CLI, block server) depend on this
// interface — never on the concrete gRPC client or generated proto types.
//
// The concrete implementation is the mreRouter singleton in
// Singleton_RoutingClient.go, speaking the MRE v1 gRPC API
// (/jmdt.proto.mre.v1.MREService). Tests inject fakes via SetRoutingClient.
type MempoolRouter interface {
	// SubmitTransaction routes a signed transaction to the MRE.
	// err covers transport/infrastructure failures only; an MRE-level
	// rejection is reported as Result.Accepted=false + Result.RejectReason
	// with a nil error, so callers can distinguish "couldn't ask" from "asked
	// and refused".
	SubmitTransaction(ctx context.Context, tx *config.Transaction, txHash string) (*SubmitResult, error)

	// PeekPendingTransactions returns up to limit pending transactions from
	// the MRE without consuming them (non-destructive across all shards).
	PeekPendingTransactions(ctx context.Context, limit int32) (*PendingBatch, error)

	// GetMempoolStats returns fleet-aggregated mempool counters.
	GetMempoolStats(ctx context.Context) (*MempoolStatsSummary, error)

	// GetFeeStatistics returns the MRE's current fee view.
	// NOTE: the upstream implementation is a stub (all values derived from a
	// single shard average; see docs/MRE-V1-MIGRATION-TRACKER.md U-1) — treat
	// these numbers as indicative, not market data.
	GetFeeStatistics(ctx context.Context) (*FeeStats, error)
}

// PendingTx is the read-only view of a pending transaction that consumers
// need. The generated proto type (*commonv1.Transaction) satisfies it
// implicitly, so no per-transaction copying is required while still keeping
// generated types out of the public API surface.
type PendingTx interface {
	GetHash() string
	GetFrom() string
	GetTo() string
	GetValue() string
	GetNonce() uint64
	GetGasLimit() string
	GetGasPrice() string
	GetMaxFee() string
	GetMaxPriorityFee() string
	GetData() []byte
	GetType() uint32
	GetTimestamp() uint64
	GetV() string
	GetR() string
	GetS() string
}

// SubmitResult is the outcome of routing one transaction through the MRE.
type SubmitResult struct {
	Accepted     bool
	Hash         string   // hash echoed by the MRE
	RejectReason string   // set when Accepted == false
	PrimaryNode  string   // shard that took the authoritative copy
	ReplicaNodes []string // shards that received best-effort replicas
	TotalNodes   int32    // primary + replicas (MRE reports len(all))
}

// PendingBatch is a non-destructive snapshot of pending transactions plus the
// MRE's fetch metadata (v1 GetPendingResponse fields 2-4).
type PendingBatch struct {
	Transactions []PendingTx
	TotalFetched int32
	RoundsUsed   int32
	DurationMs   int64
}

// MempoolStatsSummary carries the fleet-aggregated counters jmdn consumes.
// Field mapping from v1 GetMempoolStatsResponse (parity with the legacy
// MREStats shim, MRE mapper.go:174-178):
//
//	QueueCount ← aggregated.total_cache_size
//	DbCount    ← aggregated.total_primary_txns
//
// The legacy merkle_root field was hardcoded "" upstream and is deliberately
// not carried over (see tracker O-2/U-11); node health replaces it.
type MempoolStatsSummary struct {
	QueueCount   int64
	DbCount      int64
	NodeCount    int32
	HealthyNodes int32 // "healthy" = the node answered GetStats, nothing more
}

// FeeStats is the MRE fee view. PriorityFeeRatio, FeeDistribution,
// FeeByTxType and HistoricalTrend exist on the wire but are never populated
// upstream (tracker U-1) and are deliberately not exposed here.
type FeeStats struct {
	MinFee      uint64
	MaxFee      uint64
	MedianFee   uint64
	MeanFee     uint64
	Recommended RecommendedFees
}

// RecommendedFees are the MRE's suggested gas prices per urgency tier.
type RecommendedFees struct {
	Slow     uint64
	Standard uint64
	Fast     uint64
	Instant  uint64
}
