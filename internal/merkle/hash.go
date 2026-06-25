package merkle

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"

	fastsync_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
)

// hashBlock computes a canonical SHA256 digest over all ZKBlock fields that
// constitute block content, INCLUDING transactions.
//
// BlockHash is explicitly excluded — it is a wire-derived value set from the
// proto snapshot, not a recomputation from block content, so it cannot serve
// as a tamper-detection input.
//
// Encoding rules (prevent boundary/collision attacks):
//   - Variable-length byte slices/strings: 4-byte big-endian length prefix + data
//   - Nullable pointers (*common.Address, *big.Int): 1-byte flag (0x00=nil,
//     0x01=present) followed by length-prefixed value when present
//   - []uint32 Commitment: 4-byte count prefix + each element as 4-byte BE (big-endian).
//     NOTE: FastsyncV2.commitmentToBytes uses LITTLE-endian for proto wire encoding.
//     These two encodings serve different purposes and must NOT be mixed.
//     See FastsyncV2/fastsyncv2.go:commitmentToBytes for the authoritative note.
//   - Transactions: 4-byte count prefix + each tx hashed independently (sub-hash)
//   - AccessList per tx: 4-byte entry count + per-entry 4-byte key count
// HashBlock is the exported form of hashBlock, available to other packages
// (e.g. DB_OPs/merkletree) that need to produce Merkle leaves consistent
// with the SyncMonitor root.
func HashBlock(b *fastsync_types.ZKBlock) [32]byte { return hashBlock(b) }

func hashBlock(b *fastsync_types.ZKBlock) [32]byte {
	h := sha256.New()

	var u64buf [8]byte
	var u32buf [4]byte

	writeU64 := func(v uint64) {
		binary.BigEndian.PutUint64(u64buf[:], v)
		h.Write(u64buf[:])
	}
	writeU32BE := func(dst interface{ Write([]byte) (int, error) }, v uint32) {
		binary.BigEndian.PutUint32(u32buf[:], v)
		dst.Write(u32buf[:])
	}
	writeVar := func(dst interface{ Write([]byte) (int, error) }, data []byte) {
		writeU32BE(dst, uint32(len(data)))
		dst.Write(data)
	}
	writeBigInt := func(dst interface{ Write([]byte) (int, error) }, v *big.Int) {
		if v == nil {
			dst.Write([]byte{0x00})
		} else {
			dst.Write([]byte{0x01})
			writeVar(dst, v.Bytes())
		}
	}
	writeAddr := func(dst interface{ Write([]byte) (int, error) }, a interface{ Bytes() []byte }) {
		// a is *common.Address — caller checks nil before calling
		dst.Write([]byte{0x01})
		dst.Write(a.Bytes())
	}

	// ── Scalars ───────────────────────────────────────────────────────────
	writeU64(b.BlockNumber)
	writeU64(uint64(b.Timestamp))
	writeU64(b.GasLimit)
	writeU64(b.GasUsed)

	// ── Fixed-size hashes (32 bytes each, no length prefix needed) ────────
	h.Write(b.PrevHash.Bytes())
	h.Write(b.StateRoot.Bytes())

	// ── Variable-length strings ────────────────────────────────────────────
	writeVar(h, []byte(b.TxnsRoot))
	writeVar(h, []byte(b.ProofHash))
	writeVar(h, []byte(b.Status))
	writeVar(h, []byte(b.ExtraData))

	// ── Variable-length byte slices ───────────────────────────────────────
	writeVar(h, b.StarkProof)
	writeVar(h, b.LogsBloom)

	// ── Commitment []uint32 — count-prefixed to prevent boundary collisions
	writeU32BE(h, uint32(len(b.Commitment)))
	for _, v := range b.Commitment {
		writeU32BE(h, v)
	}

	// ── Nullable addresses — 1-byte flag prevents "nil" text collision ─────
	if b.CoinbaseAddr != nil {
		writeAddr(h, b.CoinbaseAddr)
	} else {
		h.Write([]byte{0x00})
	}
	if b.ZKVMAddr != nil {
		writeAddr(h, b.ZKVMAddr)
	} else {
		h.Write([]byte{0x00})
	}

	// ── Transactions — count-prefixed; each tx → independent sub-hash ─────
	writeU32BE(h, uint32(len(b.Transactions)))
	for i := range b.Transactions {
		tx := &b.Transactions[i]
		th := sha256.New()

		// tx.Hash is the stored per-tx hash (32 bytes, fixed)
		th.Write(tx.Hash.Bytes())

		// Nullable addresses
		if tx.From != nil {
			writeAddr(th, tx.From)
		} else {
			th.Write([]byte{0x00})
		}
		if tx.To != nil {
			writeAddr(th, tx.To)
		} else {
			th.Write([]byte{0x00})
		}

		writeBigInt(th, tx.Value)

		th.Write([]byte{tx.Type})

		var u64b [8]byte
		binary.BigEndian.PutUint64(u64b[:], tx.Timestamp)
		th.Write(u64b[:])
		binary.BigEndian.PutUint64(u64b[:], tx.Nonce)
		th.Write(u64b[:])
		binary.BigEndian.PutUint64(u64b[:], tx.GasLimit)
		th.Write(u64b[:])

		writeBigInt(th, tx.ChainID)
		writeBigInt(th, tx.GasPrice)
		writeBigInt(th, tx.MaxFee)
		writeBigInt(th, tx.MaxPriorityFee)

		writeVar(th, tx.Data)

		// AccessList: entry count + per-entry (address + key count + keys)
		var u32b [4]byte
		binary.BigEndian.PutUint32(u32b[:], uint32(len(tx.AccessList)))
		th.Write(u32b[:])
		for _, entry := range tx.AccessList {
			th.Write(entry.Address.Bytes())
			binary.BigEndian.PutUint32(u32b[:], uint32(len(entry.StorageKeys)))
			th.Write(u32b[:])
			for _, key := range entry.StorageKeys {
				th.Write(key.Bytes())
			}
		}

		writeBigInt(th, tx.V)
		writeBigInt(th, tx.R)
		writeBigInt(th, tx.S)

		h.Write(th.Sum(nil))
	}

	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
