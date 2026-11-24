package contractDB

import (
	"bytes"
	"encoding/binary"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Deterministic commit ordering + state digest (audit EVM-09).
//
// CommitToDB previously iterated `for addr, obj := range c.stateObjects` — Go
// randomizes map order, so both the batch WRITE order and any state hash
// differed per node, and the returned root was empty. For contract execution
// to be consensus-safe every node must apply the same block to the same digest,
// independent of map iteration. The helpers below give a total order over the
// changed state so the digest is a deterministic commitment two nodes can
// compare. (This is a canonical-serialization keccak digest, not yet a full
// Ethereum MPT state root — P4 upgrades it; for P1/P2 it is a sufficient
// divergence detector.)

// sortedAddrs returns the addresses in ascending byte order.
func sortedAddrs(addrs []common.Address) []common.Address {
	out := make([]common.Address, len(addrs))
	copy(out, addrs)
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}

// sortedHashKeys returns the keys of m in ascending byte order.
func sortedHashKeys(m map[common.Hash]common.Hash) []common.Hash {
	out := make([]common.Hash, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}

// stateHasher accumulates a keccak digest over canonically-ordered state
// changes. Fold records in address order; within a record fold storage in key
// order. The result is independent of the order records were produced in.
type stateHasher struct{ h crypto.KeccakState }

func newStateHasher() *stateHasher { return &stateHasher{h: crypto.NewKeccakState()} }

// foldTombstone records the deletion of an account's contract state.
func (s *stateHasher) foldTombstone(addr common.Address) {
	s.h.Write(addr[:])
	s.h.Write([]byte{0x00}) // tombstone tag
}

// foldStorageWrite folds one (addr, key, value) storage write.
func (s *stateHasher) foldStorageWrite(addr common.Address, key, val common.Hash) {
	s.h.Write(addr[:])
	s.h.Write([]byte{0x01}) // storage-write tag
	s.h.Write(key[:])
	s.h.Write(val[:])
}

// foldStorageDelete folds one (addr, key) storage deletion.
func (s *stateHasher) foldStorageDelete(addr common.Address, key common.Hash) {
	s.h.Write(addr[:])
	s.h.Write([]byte{0x02}) // storage-delete tag
	s.h.Write(key[:])
}

// foldCode folds an account's code write (nil code = code deletion).
func (s *stateHasher) foldCode(addr common.Address, code []byte) {
	s.h.Write(addr[:])
	s.h.Write([]byte{0x03}) // code tag
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(code)))
	s.h.Write(n[:])
	s.h.Write(code)
}

// foldNonce folds an account's nonce write.
func (s *stateHasher) foldNonce(addr common.Address, nonce uint64) {
	s.h.Write(addr[:])
	s.h.Write([]byte{0x04}) // nonce tag
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], nonce)
	s.h.Write(n[:])
}

// sum returns the accumulated digest.
func (s *stateHasher) sum() common.Hash {
	var out common.Hash
	_, _ = s.h.Read(out[:]) // KeccakState.Read yields the 32-byte digest
	return out
}
