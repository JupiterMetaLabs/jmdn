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

const (
	primaryPrefix = "evmlog:b:"
	addressPrefix = "evmlog:a:"
)

// errStop is returned from a scan callback to end the scan early (past ToBlock).
var errStop = errors.New("logstore: stop scan")

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
func (s *Store) GetLogs(_ context.Context, filter store.LogFilter) ([]*ethtypes.Log, error) {
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
		return nil
	}

	if len(filter.Addresses) > 0 {
		for _, addr := range filter.Addresses {
			prefix := []byte(addressPrefix + strings.ToLower(addr.Hex()) + ":")
			err := s.kv.ScanPrefix(prefix, func(k, pk []byte) error {
				b, ok := blockOfKey(k)
				if !ok {
					return nil
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
			if err != nil && !errors.Is(err, errStop) {
				return nil, err
			}
		}
	} else {
		err := s.kv.ScanPrefix([]byte(primaryPrefix), func(k, raw []byte) error {
			b, ok := blockOfKey(k)
			if !ok {
				return nil
			}
			in, past := inRange(b)
			if past {
				return errStop
			}
			if !in {
				return nil
			}
			return collect(k, raw)
		})
		if err != nil && !errors.Is(err, errStop) {
			return nil, err
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
