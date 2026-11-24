package main

import (
	"log"

	"gossipnode/Block"
	"gossipnode/config/settings"
	"gossipnode/gETH/Facade/Service"
	"gossipnode/metrics"
	"gossipnode/txstatus"
)

// initTxStatus wires transaction-status resolution.
//
// Called once during startup, after the mempool routing client is created and
// before the RPC facade starts. When tx_status.enabled is false this installs
// nothing: no submit records are written, jmdt_getTransactionStatus reports the
// method as unavailable, and eth_getTransactionByHash behaves exactly as it did
// before. That is the default, so the feature is dark until an operator opts in.
func initTxStatus(cfg *settings.NodeConfig) {
	if cfg == nil {
		return
	}
	ts := cfg.TxStatus

	if !ts.Enabled {
		log.Printf("Transaction status resolution disabled (set tx_status.enabled or JMDN_TX_STATUS_ENABLED=true to enable)")
		return
	}

	// The submit log must be installed before the resolver: it is the only
	// evidence that permits a `processing` answer, and Block's submit path
	// writes to it through the process-wide instance.
	submitLog := txstatus.InitSubmitLog(ts.SubmitRecordTTL, ts.SubmitRecordCapacity)

	resolver := txstatus.NewResolver(txstatus.Deps{
		Chain:   Service.NewChainStoreAdapter(),
		Mempool: Block.NewMRELookup(),
		// Failed is intentionally nil: routing rejections from the sequencer
		// orchestrator are not yet delivered to jmdn, so no rejection is known
		// locally. A nil FailedStore makes `failed` unreachable — the resolver
		// answers `processing` or `unknown` instead, never a wrong `failed`.
		Failed:    nil,
		SubmitLog: submitLog,
		Config: txstatus.Config{
			MempoolTimeout:          ts.MempoolTimeout,
			ChainTimeout:            ts.ChainTimeout,
			NegativeCacheTTL:        ts.NegativeCacheTTL,
			NegativeCacheSize:       ts.NegativeCacheSize,
			RateLimitPerSec:         ts.RateLimitPerSec,
			RateLimitBurst:          ts.RateLimitBurst,
			BreakerFailureThreshold: ts.BreakerFailureThreshold,
			BreakerCooldown:         ts.BreakerCooldown,
		},
		Observer: metrics.TxStatusObserver{},
	})

	Service.SetTxStatusResolver(resolver)
	Service.SetPendingTxByHashEnabled(ts.PendingTxByHash)

	log.Printf("Transaction status resolution enabled (submit_record_ttl=%s mempool_timeout=%s pending_tx_by_hash=%t)",
		ts.SubmitRecordTTL, ts.MempoolTimeout, ts.PendingTxByHash)

	if ts.PendingTxByHash {
		log.Printf("eth_getTransactionByHash will serve queued mempool transactions with null block fields; " +
			"eth_getTransactionReceipt remains null until a transaction is in a block")
	}
}
