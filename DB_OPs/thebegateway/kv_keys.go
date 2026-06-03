// MODULE: DB_OPs/thebegateway/kv_keys.go
// PURPOSE: Binary key builders for contract data in ThebeDB BadgerDB KV store.
//          Separate from cache_keys.go (Redis string keys) — these are raw []byte keys.
//
// KEY SCHEMA:
//   contract:code:<addr_hex>                     → PutWorm  (immutable)
//   contract:nonce:<addr_hex>                    → PutDerived (mutable)
//   contract:storage:<addr_20_raw><slot_32_raw>  → PutDerived (mutable, binary concat)
//   contract:meta:<addr_hex>                     → PutWorm  (immutable)
//
// DO NOT use these for Redis cache — use cache_keys.go for that.
// DO NOT use string concat for storage keys — binary concat only.

package thebegateway

import "encoding/binary"

const (
	kvPrefixCode    = "contract:code:"
	kvPrefixNonce   = "contract:nonce:"
	kvPrefixStorage = "contract:storage:"
	kvPrefixMeta    = "contract:meta:"
)

// kvCodeKey returns the KV key for contract bytecode.
// Time: O(1)
func kvCodeKey(addrHex string) []byte {
	return []byte(kvPrefixCode + addrHex)
}

// kvNonceKey returns the KV key for contract EVM nonce.
// Time: O(1)
func kvNonceKey(addrHex string) []byte {
	return []byte(kvPrefixNonce + addrHex)
}

// kvStorageKey returns the binary KV key for a contract storage slot.
// Key = "contract:storage:" + addr_raw_20_bytes + slot_raw_32_bytes = 68 bytes total.
// addrRaw must be exactly 20 bytes; slotRaw must be exactly 32 bytes.
// Time: O(1)
func kvStorageKey(addrRaw, slotRaw []byte) []byte {
	key := make([]byte, len(kvPrefixStorage)+20+32)
	n := copy(key, kvPrefixStorage)
	n += copy(key[n:], addrRaw[:20])
	copy(key[n:], slotRaw[:32])
	return key
}

// kvMetaKey returns the KV key for contract deployment metadata.
// Time: O(1)
func kvMetaKey(addrHex string) []byte {
	return []byte(kvPrefixMeta + addrHex)
}

// encodeUint64 encodes a uint64 as big-endian 8 bytes.
func encodeUint64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// decodeUint64 decodes big-endian 8 bytes to uint64.
func decodeUint64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}
