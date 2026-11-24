package contractDB

import (
	"fmt"
	"strconv"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/consensushash"
)

// Contract-state root (audit P4, Option B — sorted-scan keccak digest).
//
// A deterministic commitment to every contract's storage, folded into the P2.5
// block state fingerprint so a receiver whose contract STORAGE diverges from the
// producer HALTS (P2.5 already covers accounts + contract code/existence; this
// adds storage). Uses kv.Store.ScanPrefix, which yields keys in ascending byte
// order identical on every node — so the digest is deterministic fleet-wide.
// NOT an MPT (no Merkle proofs); the single-sequencer + guardian-bridge roadmap
// needs divergence DETECTION, not proofs (see docs/EVM-P4-STATE-ROOT-DESIGN.md).

const contractStorageRootDomain = "jmdn/contract-storage/v1"

// kvStoragePrefix is the scan prefix matching every storage slot of addr
// (kvKeyStorage = "contract:storage:<addr>:<slot>").
func kvStoragePrefix(addr common.Address) []byte {
	return []byte("contract:storage:" + addr.Hex() + ":")
}

// ComputeStorageRoot digests a contract's full storage: a domain-tagged keccak
// over every (slot, value) in ascending key order. Tombstoned slots (empty value)
// are skipped — a zeroed slot is absence, matching EVM semantics — so an empty
// contract yields the domain-only digest. Slot and value are canonicalized to
// their 32-byte forms so the digest is independent of stored hex casing.
func ComputeStorageRoot(store kv.Store, addr common.Address) (common.Hash, error) {
	h := crypto.NewKeccakState()
	h.Write([]byte(contractStorageRootDomain))
	prefix := kvStoragePrefix(addr)
	if err := store.ScanPrefix(prefix, func(k, v []byte) error {
		if len(v) == 0 {
			return nil // tombstone → slot absent
		}
		slot := common.HexToHash(string(k[len(prefix):]))
		val := common.HexToHash(string(v))
		h.Write(slot[:])
		h.Write(val[:])
		return nil
	}); err != nil {
		return common.Hash{}, fmt.Errorf("compute storage root %s: %w", addr.Hex(), err)
	}
	var out common.Hash
	_, _ = h.Read(out[:])
	return out, nil
}

// FoldAllContracts folds every deployed contract into f as a
// consensushash.ContractLeaf{address, nonce, codeHash, storageRoot}, in ascending
// address-key order — P4's contribution to the P2.5 state fingerprint. Contracts
// are enumerated by their code keys ("contract:code:<addr>"); a tombstoned code
// entry is a deleted contract and is skipped. Deterministic across nodes (same
// keys, same scan order). Intended to be registered as the DB_OPs contract-fold
// hook when contract execution is enabled.
func FoldAllContracts(store kv.Store, f *consensushash.StateFingerprinterV1) error {
	codePrefix := []byte("contract:code:")
	return store.ScanPrefix(codePrefix, func(k, code []byte) error {
		if len(code) == 0 {
			return nil // tombstoned code → not a live contract
		}
		addrHex := string(k[len(codePrefix):])
		addr := common.HexToAddress(addrHex)
		root, err := ComputeStorageRoot(store, addr)
		if err != nil {
			return err
		}
		nonce, err := readContractNonce(store, addr)
		if err != nil {
			return err
		}
		f.FoldContract(consensushash.ContractLeaf{
			Address:     addrHex,
			Nonce:       nonce,
			CodeHash:    crypto.Keccak256Hash(code),
			StorageRoot: root,
		})
		return nil
	})
}

// readContractNonce reads the stored contract nonce (decimal string), returning 0
// when absent or tombstoned. Mirrors KVStateRepository.GetNonce's format.
func readContractNonce(store kv.Store, addr common.Address) (uint64, error) {
	v, err := store.Get(kvKeyNonce(addr))
	if err != nil {
		if isKVNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read contract nonce %s: %w", addr.Hex(), err)
	}
	if len(v) == 0 {
		return 0, nil
	}
	n, err := strconv.ParseUint(string(v), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse contract nonce %s: %w", addr.Hex(), err)
	}
	return n, nil
}
