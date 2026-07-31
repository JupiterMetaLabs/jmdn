// Package RejectionReport tells the JMDT sequencer-orchestrator that a block
// THIS node proposed was rejected by the consensus committee.
//
// # Why it exists
//
// The orchestrator submits a block over /api/process-block (or the Block
// gRPC service) and marks every transaction in it "included" as soon as that
// call returns. But the handler returns right after Consensus.Start spawns the
// voting goroutine — before a single vote has been requested. Voting resolves
// tens of seconds later. So when the committee votes a block down, the block is
// discarded here (see Consensus.BroadcastAndProcessBlock) while the
// orchestrator still believes it landed, and the transactions stay mislabelled
// as included in its failed-transaction table forever.
//
// This package closes that gap: on the rejected path the sequencer POSTs the
// block's transactions plus the committee's reason to the orchestrator, which
// flips them to CONSENSUS_REJECTED with the reason in the same field it already
// renders for failed transactions.
//
// Design constraints
//
//   - NEVER block or fail consensus. Send() snapshots the block synchronously
//     (so the caller may hold the consensus lock) then hands off to a bounded
//     background queue. A full queue drops the report with a log line, exactly
//     like the alert service.
//   - Disabled by default. Empty orchestrator.url or orchestrator.api_key means
//     silent no-op.
//   - Best-effort delivery. Attempts are retried up to MaxAttempts, but a
//     report lost to a restart or a prolonged outage is not recovered; the
//     table stays stale for those hashes until they are resubmitted.
//
// Scope: this reports the verdict only. It does not requeue the transactions.
package RejectionReport

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"gossipnode/config"
	"gossipnode/config/settings"
)

// QueueCapacity bounds the pending-report queue. Reports are diagnostics, so
// the queue drops rather than grows or blocks.
const QueueCapacity = 64

// maxReasonLen truncates the reason/detail strings before they are sent. The
// orchestrator also caps what it stores; capping here keeps the payload small.
const maxReasonLen = 1024

// TxnReport is one transaction from the rejected block.
//
// Value, GasPrice and ChainID are base-10 DECIMAL strings (wei), matching the
// orchestrator's stored representation — sending hex here would silently
// corrupt the amounts it renders. Data is 0x-prefixed hex.
//
// These details are only used when the orchestrator has no row for the hash
// (never recorded, or pruned); otherwise its own stored details win.
type TxnReport struct {
	Hash     string `json:"hash"`
	From     string `json:"from"`
	To       string `json:"to"`
	Nonce    uint64 `json:"nonce"`
	Value    string `json:"value"`
	GasPrice string `json:"gas_price"`
	ChainID  string `json:"chain_id"`
	Type     uint32 `json:"type"`
	Data     string `json:"data"`
}

// Report is the payload POSTed to the orchestrator. Field names and JSON tags
// must stay in lockstep with ConsensusRejectedRequest in the orchestrator's
// cmd/orchestrator/consensus_reject_api.go, which decodes with
// DisallowUnknownFields — an extra field here becomes a 400 there.
type Report struct {
	BlockNumber  uint64      `json:"block_number"`
	BlockHash    string      `json:"block_hash"`
	Reason       string      `json:"reason"`
	Detail       string      `json:"detail"`
	RejectedAt   string      `json:"rejected_at"`
	Transactions []TxnReport `json:"transactions"`
}

// reporter is the configured singleton, or nil when reporting is disabled.
type reporter struct {
	url         string
	apiKey      string
	maxAttempts int
	client      *http.Client // http.Client is concurrency-safe; reused across sends
	queue       chan *Report
}

// service resolves the reporter exactly once, on first use. Self-initializing
// so no wiring in main.go is required: a node that never rejects a block never
// starts the sender goroutine.
var service = sync.OnceValue(func() *reporter {
	// settings.Get() PANICS when Load() was never called (early init, tooling,
	// tests). This runs on the consensus-rejection path, so gate on IsLoaded
	// instead of risking a panic inside a consensus round.
	if !settings.IsLoaded() {
		return nil
	}
	oc := settings.Get().Orchestrator
	if strings.TrimSpace(oc.URL) == "" || strings.TrimSpace(oc.APIKey) == "" {
		return nil
	}

	timeout := oc.HTTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	attempts := oc.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	r := &reporter{
		url:         strings.TrimSpace(oc.URL),
		apiKey:      strings.TrimSpace(oc.APIKey),
		maxAttempts: attempts,
		client:      &http.Client{Timeout: timeout},
		queue:       make(chan *Report, QueueCapacity),
	}
	go r.drain()
	return r
})

// Enabled reports whether a callback target is configured. Useful for logging
// and tests; Send() is already a safe no-op when disabled.
func Enabled() bool { return service() != nil }

// Send builds a report from the rejected block and queues it for delivery.
//
// The payload is built SYNCHRONOUSLY, so it is safe to call while holding the
// consensus lock and while block is still owned by the caller — nothing after
// this call reads the caller's memory. Delivery happens on the background
// sender.
//
// reason is the short committee verdict (e.g. "insufficient yes votes: 3 of 7
// valid, need 5 (committee size 7)"); detail is the optional compact per-buddy
// rejection line. Both come from Consensus.takeRejectSummary.
func Send(block *config.ZKBlock, reason, detail string) {
	// Hard invariant: this is diagnostics running inside a consensus round. No
	// defect in reporting may ever propagate into consensus, so contain panics
	// here rather than relying on every field access below being nil-safe.
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[REJECT-REPORT] Recovered while building consensus-rejection report: %v", rec)
		}
	}()

	r := service()
	if r == nil || block == nil {
		return
	}
	if len(block.Transactions) == 0 {
		// Nothing to mark failed; the orchestrator rejects an empty list.
		return
	}
	r.enqueue(build(block, reason, detail))
}

// build snapshots the block into a self-contained payload.
func build(block *config.ZKBlock, reason, detail string) *Report {
	rep := &Report{
		BlockNumber:  block.BlockNumber,
		BlockHash:    block.BlockHash.Hex(),
		Reason:       truncate(strings.TrimSpace(reason), maxReasonLen),
		Detail:       truncate(strings.TrimSpace(detail), maxReasonLen),
		RejectedAt:   time.Now().UTC().Format(time.RFC3339),
		Transactions: make([]TxnReport, 0, len(block.Transactions)),
	}
	for i := range block.Transactions {
		tx := &block.Transactions[i]

		// Fee: legacy/2930 carry GasPrice, 1559 carries MaxFee. Mirror the
		// orchestrator's own fallback so the rendered gasPrice matches.
		fee := tx.GasPrice
		if fee == nil {
			fee = tx.MaxFee
		}

		to := ""
		if tx.To != nil { // nil => contract creation
			to = tx.To.Hex()
		}
		from := ""
		if tx.From != nil {
			from = tx.From.Hex()
		}
		data := ""
		if len(tx.Data) > 0 {
			data = "0x" + hex.EncodeToString(tx.Data)
		}

		rep.Transactions = append(rep.Transactions, TxnReport{
			Hash:     tx.Hash.Hex(),
			From:     from,
			To:       to,
			Nonce:    tx.Nonce,
			Value:    decimal(tx.Value),
			GasPrice: decimal(fee),
			ChainID:  decimal(tx.ChainID),
			Type:     uint32(tx.Type),
			Data:     data,
		})
	}
	return rep
}

// decimal renders a big.Int as a base-10 string, "" for nil. Base 10 is
// required: the orchestrator parses these with big.Int.SetString(s, 10).
func decimal(v *big.Int) string {
	if v == nil {
		return ""
	}
	return v.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// enqueue hands the report to the sender without blocking. A full queue means
// consensus is failing faster than reports drain; dropping is preferable to
// stalling the consensus goroutine.
func (r *reporter) enqueue(rep *Report) {
	select {
	case r.queue <- rep:
	default:
		log.Printf("[REJECT-REPORT] Queue full, dropping consensus-rejection report for block %d (%s)",
			rep.BlockNumber, rep.BlockHash)
	}
}

// drain sends queued reports one at a time. A panic here would take down the
// whole node for a diagnostics failure, so each send is contained.
func (r *reporter) drain() {
	for rep := range r.queue {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("[REJECT-REPORT] Recovered while sending report for block %d: %v",
						rep.BlockNumber, rec)
				}
			}()
			r.send(rep)
		}()
	}
}

// send POSTs one report, retrying transport errors and 5xx responses up to
// maxAttempts. A 4xx is NOT retried: the orchestrator rejected the request
// itself (bad secret, malformed body), and repeating it cannot help.
func (r *reporter) send(rep *Report) {
	body, err := json.Marshal(rep)
	if err != nil {
		log.Printf("[REJECT-REPORT] Failed to marshal report for block %d: %v", rep.BlockNumber, err)
		return
	}

	backoff := time.Second
	for attempt := 1; attempt <= r.maxAttempts; attempt++ {
		retryable, err := r.post(body)
		if err == nil {
			log.Printf("[REJECT-REPORT] Reported consensus rejection of block %d (%s), %d txns, attempt %d",
				rep.BlockNumber, rep.BlockHash, len(rep.Transactions), attempt)
			return
		}
		if !retryable || attempt == r.maxAttempts {
			log.Printf("[REJECT-REPORT] Giving up reporting block %d (%s) after %d attempt(s): %v",
				rep.BlockNumber, rep.BlockHash, attempt, err)
			return
		}
		log.Printf("[REJECT-REPORT] Attempt %d/%d failed for block %d: %v (retrying in %s)",
			attempt, r.maxAttempts, rep.BlockNumber, err, backoff)
		time.Sleep(backoff)
		backoff *= 2
	}
}

// post performs a single attempt. It returns whether the failure is worth
// retrying alongside the error; (false, nil) means success.
func (r *reporter) post(body []byte) (retryable bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return false, err // malformed URL — retrying cannot fix it
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", r.apiKey)

	resp, err := r.client.Do(req)
	if err != nil {
		return true, err // transport/timeout — worth another attempt
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused.
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode >= 500:
		return true, &httpError{code: resp.StatusCode, body: string(respBody)}
	default:
		return false, &httpError{code: resp.StatusCode, body: string(respBody)}
	}
}

type httpError struct {
	code int
	body string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("orchestrator responded %d %s: %s", e.code, http.StatusText(e.code), e.body)
}
