package logstore

import (
	"bytes"
	"context"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"gossipnode/DB_OPs/store"
)

// UNTESTED-LOCALLY: written without a Go toolchain in the authoring environment.
// Validate with:  go test ./DB_OPs/logstore/ -v

// memKV is a sorted in-memory KV whose ScanPrefix yields keys in ascending byte
// order — the same contract ThebeDB's kv.Store documents (state_root.go relies
// on it for deterministic digests).
type memKV struct{ m map[string][]byte }

func newMemKV() *memKV { return &memKV{m: map[string][]byte{}} }

func (k *memKV) Get(key []byte) ([]byte, error) { return k.m[string(key)], nil }
func (k *memKV) PutDerived(key, val []byte) error {
	k.m[string(key)] = append([]byte(nil), val...)
	return nil
}
func (k *memKV) ScanPrefix(prefix []byte, fn func(kk, v []byte) error) error {
	keys := make([]string, 0, len(k.m))
	for s := range k.m {
		if bytes.HasPrefix([]byte(s), prefix) {
			keys = append(keys, s)
		}
	}
	sort.Strings(keys)
	for _, s := range keys {
		if err := fn([]byte(s), k.m[s]); err != nil {
			return err
		}
	}
	return nil
}

var (
	vault  = common.HexToAddress("0xe6e169bd3cB3Da76e213fB559Ba2aB16729bA5B7")
	other  = common.HexToAddress("0x00000000000000000000000000000000000000bb")
	topicA = common.HexToHash("0xaaaa")
	topicB = common.HexToHash("0xbbbb")
)

func mk(addr common.Address, block uint64, tx, idx uint, topics ...common.Hash) *ethtypes.Log {
	return &ethtypes.Log{
		Address: addr, Topics: topics, Data: []byte{1},
		BlockNumber: block, TxIndex: tx, Index: idx,
		TxHash: common.BigToHash(common.Big1), BlockHash: common.BigToHash(common.Big2),
	}
}

func seed(t *testing.T) *Store {
	t.Helper()
	s := New(newMemKV())
	logs := []*ethtypes.Log{
		mk(vault, 857, 0, 0, topicA),
		mk(vault, 857, 0, 1, topicB),
		mk(other, 858, 0, 0, topicA),
		mk(vault, 900, 2, 0, topicA, topicB),
		mk(vault, 1000, 0, 0, topicB),
	}
	if err := s.StoreLogs(context.Background(), logs); err != nil {
		t.Fatalf("StoreLogs: %v", err)
	}
	return s
}

func TestGetLogs_ByAddressAndRange(t *testing.T) {
	s := seed(t)
	got, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 857, ToBlock: 900, Addresses: []common.Address{vault}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 vault logs in [857,900], got %d", len(got))
	}
	// ordered by (block, tx, index)
	if got[0].BlockNumber != 857 || got[0].Index != 0 || got[1].Index != 1 || got[2].BlockNumber != 900 {
		t.Fatalf("bad order: %+v", got)
	}
	// fields survive the round trip (the whole point for relayers)
	if got[0].TxHash != common.BigToHash(common.Big1) || got[0].BlockHash != common.BigToHash(common.Big2) || got[2].TxIndex != 2 {
		t.Fatalf("log metadata lost in round trip: %+v", got[0])
	}
}

func TestGetLogs_NoAddress_NoUpperBound(t *testing.T) {
	s := seed(t)
	got, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 858})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 { // 858, 900, 1000
		t.Fatalf("want 3, got %d", len(got))
	}
}

func TestGetLogs_Topics(t *testing.T) {
	s := seed(t)
	// topic0 == B  → 857/1 and 1000/0
	got, _ := s.GetLogs(context.Background(), store.LogFilter{Topics: [][]common.Hash{{topicB}}})
	if len(got) != 2 {
		t.Fatalf("topic0=B: want 2, got %d", len(got))
	}
	// topic0 wildcard, topic1 == B → only 900/0
	got, _ = s.GetLogs(context.Background(), store.LogFilter{Topics: [][]common.Hash{{}, {topicB}}})
	if len(got) != 1 || got[0].BlockNumber != 900 {
		t.Fatalf("topic1=B: want [900], got %+v", got)
	}
	// OR within a position
	got, _ = s.GetLogs(context.Background(), store.LogFilter{Topics: [][]common.Hash{{topicA, topicB}}})
	if len(got) != 5 {
		t.Fatalf("topic0 in {A,B}: want 5, got %d", len(got))
	}
}

func TestStoreLogs_Idempotent(t *testing.T) {
	s := seed(t)
	if err := s.StoreLogs(context.Background(), []*ethtypes.Log{mk(vault, 857, 0, 0, topicA)}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetLogs(context.Background(), store.LogFilter{Addresses: []common.Address{vault}})
	if len(got) != 4 {
		t.Fatalf("re-storing the same log must not duplicate: want 4, got %d", len(got))
	}
}
