// Package logstore is the KV-backed implementation of backend.LogWriter: it
// persists EVM event logs at block-apply time and serves eth_getLogs filters.
//
// Before this package existed main.go wired `backend.New(gw, reader, nil)`, so
// eth_getLogs always failed with "LogWriter not configured" and clients (bridge
// relayers, indexers, wallets) had to reconstruct events from receipts.
//
// Key schema (all keys are zero-padded so byte order == numeric order, which
// lets ScanPrefix walk a block range in sequence and stop early):
//
//	evmlog:b:<block:020d>:<txIndex:05d>:<logIndex:05d>            → JSON(ethtypes.Log)
//	evmlog:a:<addr hex lower>:<block:020d>:<txIndex:05d>:<logIndex:05d> → primary key
//
// The address index makes the common filter shape ({address, fromBlock,
// toBlock}) a single prefix scan; topic filters are applied in memory on the
// candidates, matching geth semantics (positional, OR within a position, nil =
// wildcard).
//
// Writes are idempotent (same key → same value), so re-applying a block during
// sync or replay is safe. There is no reorg handling: jmdn is BFT-final, a
// block that has been applied is never removed.
package logstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"gossipnode/DB_OPs/store"
)

// KV is the narrow slice of ThebeDB's kv.Store this package needs (the same
// surface thebegateway.ThebeKVStore uses plus ScanPrefix). PutDerived is the
// overwritable "derived index" write — logs are re-derivable from receipts, so
// they belong in that class, not WORM.
type KV interface {
	Get(key []byte) ([]byte, error)
	PutDerived(key, value []byte) error
	ScanPrefix(prefix []byte, fn func(k, v []byte) error) error
}

// S4 (review): eth_getLogs DoS bounds. maxLogBlockSpan caps how many blocks an
// address-less query may cover, and maxLogResults caps how many logs any query may
// return, so an unauthenticated {fromBlock:earliest,toBlock:latest} request can
// neither walk unbounded history nor accumulate an unbounded response. Chosen to
// match common public-RPC provider limits (10k blocks / 10k logs per query).
//
// maxLogScanBlocks is the bound the other two do not provide: a block SPAN is a
// count, not a cost, and maxLogResults is only reached once a log MATCHES, so a
// filter that matches nothing never trips it. maxLogScanBlocks counts blocks
// actually touched, on both paths. It is what bounds the address path, which
// walks a contract's whole history from block 0 and had no work limit at all.
const (
	// 2_000 blocks, not the 10_000 this shipped with. 10_000 was the generous
	// end of what public providers allow (Alchemy's own default range is 2_000)
	// and was never derived from this node's cost: an address-less query at
	// 10_000 is 10_000 KV seeks whether or not a single log matches. The
	// consumers of this endpoint are wallets and explorers, which page; an
	// indexer that genuinely needs more is the case for raising it per node,
	// with rpc_query_refused_total{reason="logs_span"} as the evidence.
	maxLogBlockSpan = 2_000
	// Unchanged. This bounds RESPONSE MEMORY, not scan work, and 2_000 blocks
	// of a busy contract can legitimately carry more than 2_000 logs — cutting
	// it with the span would refuse valid queries for no saving.
	maxLogResults = 10_000
	// Matches the span, so the address-less path is bounded identically and the
	// address path — which has no range to bound it — gets the same ceiling.
	maxLogScanBlocks = 2_000
)

// Effective limits. These default to the constants above and are overridden
// once at startup from config (rpc.logs.*) via SetLimits — the package cannot
// import config/settings without an import cycle, so main injects them, the
// same seam Structs.SetCommittedTipFn uses.
//
// FAIL CLOSED: a zero or negative configured value keeps the compiled default.
// Atomics because the write happens on the startup goroutine while reads happen
// on every RPC goroutine.
var (
	limBlockSpan  atomic.Uint64
	limResults    atomic.Int64
	limScanBlocks atomic.Uint64
)

func init() {
	limBlockSpan.Store(maxLogBlockSpan)
	limResults.Store(maxLogResults)
	limScanBlocks.Store(maxLogScanBlocks)
}

// SetLimits overrides the eth_getLogs cost bounds. Call once at startup, before
// the RPC listeners accept traffic. Any argument that is zero or negative leaves
// that bound at its compiled default.
func SetLimits(maxBlockSpan uint64, maxResults int, maxScanBlocks uint64) {
	if maxBlockSpan > 0 {
		limBlockSpan.Store(maxBlockSpan)
	}
	if maxResults > 0 {
		limResults.Store(int64(maxResults))
	}
	if maxScanBlocks > 0 {
		limScanBlocks.Store(maxScanBlocks)
	}
}

// Limits reports the bounds in force, for logging and tests.
func Limits() (maxBlockSpan uint64, maxResults int, maxScanBlocks uint64) {
	return limBlockSpan.Load(), int(limResults.Load()), limScanBlocks.Load()
}

const (
	primaryPrefix = "evmlog:b:"
	addressPrefix = "evmlog:a:"
)

// errStop is returned from a scan callback to end the scan early (past ToBlock).
var errStop = errors.New("logstore: stop scan")

// errTooMany is returned from a scan callback when the result set exceeds
// maxLogResults. GetLogs converts it into a caller-facing error so the RPC layer
// returns a "query returned more than N results" style failure instead of
// buffering an unbounded slice.
var errTooMany = errors.New("logstore: result cap exceeded")

// errScanBudget is returned from a scan callback when the query has touched
// more blocks than the work budget allows. Converted by GetLogs into
// ErrScanBudgetExceeded, the same way errTooMany becomes ErrTooManyResults.
var errScanBudget = errors.New("logstore: scan budget exceeded")

// ErrQueryUnbounded is returned when an address-less eth_getLogs query does not
// carry a bounded [fromBlock, toBlock] range within the configured span. Such a
// query would force a full-history scan, so it is rejected up front.
//
// A sentinel, wrapped at each call site with the limit actually in force —
// the limits are configurable now, so a message baked in at init would quote a
// number the node is no longer using. errors.Is is unaffected.
var ErrQueryUnbounded = errors.New("logstore: address-less eth_getLogs requires a bounded block range")

// ErrTooManyResults is the caller-facing form of errTooMany.
var ErrTooManyResults = errors.New("logstore: eth_getLogs matched too many logs; narrow the range or add filters")

// ErrScanBudgetExceeded is returned when a query would touch more blocks than
// the configured work budget allows. Distinct from ErrQueryUnbounded: the range
// asked for may be legitimate, it is the SCAN it implies that is refused — on
// the address path there is no range to blame at all.
var ErrScanBudgetExceeded = errors.New("logstore: eth_getLogs would scan too many blocks; narrow the range or add filters")

// Store implements backend.LogWriter over a KV.
type Store struct {
	kv KV
}

// New returns a LogWriter backed by kv.
func New(kv KV) *Store { return &Store{kv: kv} }

func primaryKey(block uint64, txIndex uint, logIndex uint) []byte {
	return []byte(fmt.Sprintf("%s%020d:%05d:%05d", primaryPrefix, block, txIndex, logIndex))
}

func addressKey(addr common.Address, block uint64, txIndex uint, logIndex uint) []byte {
	return []byte(fmt.Sprintf("%s%s:%020d:%05d:%05d", addressPrefix, strings.ToLower(addr.Hex()), block, txIndex, logIndex))
}

// blockOfKey extracts the block number from either key form. It relies on the
// fixed-width layout: the block field is the 20 digits before the last two
// ":NNNNN" groups.
func blockOfKey(k []byte) (uint64, bool) {
	s := string(k)
	// strip ":<txIndex:05d>:<logIndex:05d>"
	if len(s) < 12 {
		return 0, false
	}
	s = s[:len(s)-12]
	if len(s) < 20 {
		return 0, false
	}
	var n uint64
	for _, c := range s[len(s)-20:] {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}

// StoreLogs persists logs. Each log must already carry BlockNumber, TxIndex
// and Index (the apply path stamps them before calling — see
// BlockProcessing.applyContractTx). A nil log is skipped.
func (s *Store) StoreLogs(_ context.Context, logs []*ethtypes.Log) error {
	for _, l := range logs {
		if l == nil {
			continue
		}
		raw, err := json.Marshal(l)
		if err != nil {
			return fmt.Errorf("logstore: marshal log: %w", err)
		}
		pk := primaryKey(l.BlockNumber, l.TxIndex, l.Index)
		if err := s.kv.PutDerived(pk, raw); err != nil {
			return fmt.Errorf("logstore: set primary: %w", err)
		}
		if err := s.kv.PutDerived(addressKey(l.Address, l.BlockNumber, l.TxIndex, l.Index), pk); err != nil {
			return fmt.Errorf("logstore: set address index: %w", err)
		}
	}
	return nil
}

// GetLogs returns logs matching filter, ordered by (block, txIndex, logIndex).
// ToBlock == 0 means "no upper bound" (the RPC layer maps "latest" to 0 when
// it cannot resolve the head).
//
// ctx is honoured inside both scan paths. It used to be discarded (the
// parameter was named `_`), so the 10s deadline DB_OPs.GetLogs creates for
// this call reached a function that could not observe it: a scan that got past
// the span bound ran to completion no matter how long it took, and neither the
// deadline nor a client disconnect could stop it.
func (s *Store) GetLogs(ctx context.Context, filter store.LogFilter) ([]*ethtypes.Log, error) {
	inRange := func(b uint64) (ok bool, past bool) {
		if b < filter.FromBlock {
			return false, false
		}
		if filter.ToBlock != 0 && b > filter.ToBlock {
			return false, true
		}
		return true, false
	}

	var out []*ethtypes.Log
	seen := make(map[string]struct{})

	collect := func(pk []byte, raw []byte) error {
		if _, dup := seen[string(pk)]; dup {
			return nil
		}
		var l ethtypes.Log
		if err := json.Unmarshal(raw, &l); err != nil {
			return fmt.Errorf("logstore: unmarshal %s: %w", pk, err)
		}
		if !matchTopics(l.Topics, filter.Topics) {
			return nil
		}
		seen[string(pk)] = struct{}{}
		out = append(out, &l)
		if len(out) > int(limResults.Load()) {
			return errTooMany
		}
		return nil
	}

	// Work budget, shared by both paths and by every address in one query: how
	// many distinct blocks this query may touch. Keys come back in ascending
	// block order, so a change of block is a new block touched. Blocks counted
	// once per address, so a query over several addresses that cover the same
	// blocks spends the budget faster — conservative, and the alternative is
	// remembering every block number seen.
	scanBudget := limScanBlocks.Load()
	var blocksScanned uint64
	lastBlock, haveLast := uint64(0), false
	countBlock := func(b uint64) error {
		if haveLast && b == lastBlock {
			return nil
		}
		lastBlock, haveLast = b, true
		blocksScanned++
		if blocksScanned > scanBudget {
			return errScanBudget
		}
		return nil
	}

	if len(filter.Addresses) > 0 {
		// Address path: one prefix scan per address. The address index is bounded
		// to the logs of one contract, but that is NOT a bound on work — it walks
		// the contract's whole history from block 0, and the result cap only fires
		// once a log MATCHES, so a filter that matches nothing scans everything.
		// scanBudget is what bounds it.
		for _, addr := range filter.Addresses {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			lastBlock, haveLast = 0, false // block runs are per address
			prefix := []byte(addressPrefix + strings.ToLower(addr.Hex()) + ":")
			err := s.kv.ScanPrefix(prefix, func(k, pk []byte) error {
				// Stop when the caller's deadline does.
				if err := ctx.Err(); err != nil {
					return err
				}
				b, ok := blockOfKey(k)
				if !ok {
					return nil
				}
				if err := countBlock(b); err != nil {
					return err
				}
				in, past := inRange(b)
				if past {
					return errStop
				}
				if !in {
					return nil
				}
				raw, err := s.kv.Get(pk)
				if err != nil || len(raw) == 0 {
					return nil // index without body: skip (partial write); never fail the query
				}
				return collect(pk, raw)
			})
			if errors.Is(err, errTooMany) {
				return nil, fmt.Errorf("%w (cap %d)", ErrTooManyResults, limResults.Load())
			}
			if errors.Is(err, errScanBudget) {
				return nil, fmt.Errorf("%w (budget %d blocks)", ErrScanBudgetExceeded, scanBudget)
			}
			if err != nil && !errors.Is(err, errStop) {
				return nil, err
			}
		}
	} else {
		// Address-less path: there is no per-contract index to bound the scan, so
		// the old code did ScanPrefix(primaryPrefix) — a walk of ALL logs from block
		// 0, the eth_getLogs DoS (S4). Require an explicit bounded range and seek
		// per block using the zero-padded key schema, so the scan touches only the
		// requested [FromBlock, ToBlock] window and never earlier history.
		if filter.ToBlock == 0 || filter.ToBlock < filter.FromBlock {
			return nil, fmt.Errorf("%w of at most %d blocks", ErrQueryUnbounded, limBlockSpan.Load())
		}
		// Span check without the +1. The guard above already proves
		// ToBlock >= FromBlock, so this subtraction cannot wrap; the previous
		// form, ToBlock-FromBlock+1 > maxLogBlockSpan, wrapped to 0 for
		// ToBlock = MaxUint64 and let the widest possible request straight
		// through. Same bound as before: a span of maxLogBlockSpan-1 is
		// maxLogBlockSpan blocks inclusive and is still allowed.
		span := limBlockSpan.Load()
		if filter.ToBlock-filter.FromBlock >= span {
			return nil, fmt.Errorf("%w of at most %d blocks", ErrQueryUnbounded, span)
		}
		for b := filter.FromBlock; b <= filter.ToBlock; b++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Normally a no-op here: the span bound above already caps this
			// loop at limBlockSpan blocks, and the budget defaults to the same
			// value. It bites only when an operator sets max_scan_blocks lower
			// than max_block_span, which is how a node asks for a tighter work
			// bound than its advertised range.
			if err := countBlock(b); err != nil {
				return nil, fmt.Errorf("%w (budget %d blocks)", ErrScanBudgetExceeded, scanBudget)
			}
			blockPrefix := []byte(fmt.Sprintf("%s%020d:", primaryPrefix, b))
			err := s.kv.ScanPrefix(blockPrefix, func(k, raw []byte) error {
				return collect(k, raw)
			})
			if errors.Is(err, errTooMany) {
				return nil, fmt.Errorf("%w (cap %d)", ErrTooManyResults, limResults.Load())
			}
			if err != nil {
				return nil, err
			}
			if b == filter.ToBlock {
				break // avoid uint64 overflow when ToBlock == math.MaxUint64
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.BlockNumber != b.BlockNumber {
			return a.BlockNumber < b.BlockNumber
		}
		if a.TxIndex != b.TxIndex {
			return a.TxIndex < b.TxIndex
		}
		return a.Index < b.Index
	})
	return out, nil
}

// matchTopics implements the eth_getLogs topic filter: filter[i] applies to
// topics[i]; an empty filter row is a wildcard; within a row any hash may
// match; a log with fewer topics than filter rows only matches if the missing
// rows are wildcards.
func matchTopics(topics []common.Hash, filter [][]common.Hash) bool {
	for i, row := range filter {
		if len(row) == 0 {
			continue // wildcard
		}
		if i >= len(topics) {
			return false
		}
		hit := false
		for _, want := range row {
			if want == topics[i] {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}
