package contractDB

import "github.com/ethereum/go-ethereum/common"

// makeCodeKey builds the binary KVStore key for contract bytecode.
// Used by sharedKVStore (the local in-memory KVStore for EVM execution).
// Note: kvKeyCode in kv_state_repository.go uses a different hex-string format
// for the ThebeDB BadgerDB store — these two key spaces are intentionally separate.
func makeCodeKey(addr common.Address) []byte {
	key := make([]byte, len(PrefixCode)+common.AddressLength)
	copy(key, PrefixCode)
	copy(key[len(PrefixCode):], addr[:])
	return key
}
