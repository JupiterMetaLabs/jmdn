//go:build ignore
// +build ignore

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"strconv"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	thebecfg "github.com/JupiterMetaLabs/ThebeDB/pkg/config"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/profile"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/DB_OPs/cassata"
	contractDB "gossipnode/DB_OPs/contractDB"
	"gossipnode/DB_OPs/thebeprofile"
)

// migrate_contract_pebble_to_thebe reads all data from the existing PebbleDB
// contract storage and ingests it into ThebeDB via cassata.
// Run once after Phase D deployment with thebe.enabled: true.
//
// Usage:
//
//	go run ./Scripts/migrate_contract_pebble_to_thebe.go \
//	  --pebble-path ./contract_storage_pebble \
//	  --thebe-dsn "postgres://..." \
//	  --thebe-kv-path ./data/thebe-kv
func main() {
	pebblePath := flag.String("pebble-path", "./contract_storage_pebble", "Pebble DB path")
	thebeDSN := flag.String("thebe-dsn", "", "ThebeDB Postgres DSN")
	thebeKVPath := flag.String("thebe-kv-path", "./data/thebe-kv", "ThebeDB BadgerDB path")
	flag.Parse()

	if *thebeDSN == "" {
		log.Fatal("--thebe-dsn is required")
	}

	ctx := context.Background()

	pebbleStore, err := contractDB.NewPebbleStore(*pebblePath)
	if err != nil {
		log.Fatalf("open pebble: %v", err)
	}
	defer pebbleStore.Close()

	reg := profile.NewRegistry()
	reg.Register(thebeprofile.New())
	db, err := thebedb.NewFromConfig(thebedb.Config{
		KV:       kv.Config{Backend: kv.BackendBadger, Path: *thebeKVPath},
		SQL:      thebecfg.SQL{DSN: *thebeDSN},
		Profiles: reg,
	})
	if err != nil {
		log.Fatalf("open thebedb: %v", err)
	}
	defer db.Close()

	cas := cassata.New(db, nil)
	counts := map[string]int{
		"code":          0,
		"storage":       0,
		"storage_meta":  0,
		"nonces":        0,
		"contract_meta": 0,
		"receipts":      0,
	}

	if err := migratePrefix(pebbleStore, contractDB.PrefixCode, func(key, val []byte) error {
		addrBytes := key[len(contractDB.PrefixCode):]
		addr := common.BytesToAddress(addrBytes)
		if err := cas.IngestContractCode(ctx, cassata.ContractCodeResult{
			Address:  addr.Hex(),
			Code:     val,
			CodeHash: crypto.Keccak256Hash(val).Hex(),
		}); err != nil {
			return err
		}
		counts["code"]++
		return nil
	}); err != nil {
		log.Fatalf("migrate code: %v", err)
	}

	if err := migratePrefix(pebbleStore, contractDB.PrefixStorage, func(key, val []byte) error {
		rest := key[len(contractDB.PrefixStorage):]
		addr := common.BytesToAddress(rest[:common.AddressLength])
		slot := common.BytesToHash(rest[common.AddressLength:])
		value := common.BytesToHash(val)
		if err := cas.IngestContractStorage(ctx, cassata.ContractStorageResult{
			Address:   addr.Hex(),
			SlotHash:  slot.Hex(),
			ValueHash: value.Hex(),
		}); err != nil {
			return err
		}
		counts["storage"]++
		return nil
	}); err != nil {
		log.Fatalf("migrate storage: %v", err)
	}

	if err := migratePrefix(pebbleStore, contractDB.PrefixStorageMeta, func(key, val []byte) error {
		rest := key[len(contractDB.PrefixStorageMeta):]
		addr := common.BytesToAddress(rest[:common.AddressLength])
		slot := common.BytesToHash(rest[common.AddressLength:])
		var meta contractDB.StorageMetadata
		if err := json.Unmarshal(val, &meta); err != nil {
			return fmt.Errorf("unmarshal storage meta: %w", err)
		}
		if err := cas.IngestContractStorageMeta(ctx, cassata.ContractStorageMetaResult{
			Address:           addr.Hex(),
			SlotHash:          slot.Hex(),
			ValueHash:         meta.ValueHash.Hex(),
			LastModifiedBlock: meta.LastModifiedBlock,
			LastModifiedTx:    meta.LastModifiedTx.Hex(),
		}); err != nil {
			return err
		}
		counts["storage_meta"]++
		return nil
	}); err != nil {
		log.Fatalf("migrate storage meta: %v", err)
	}

	if err := migratePrefix(pebbleStore, contractDB.PrefixNonce, func(key, val []byte) error {
		addrBytes := key[len(contractDB.PrefixNonce):]
		addr := common.BytesToAddress(addrBytes)
		nonce := new(big.Int).SetBytes(val).Uint64()
		if err := cas.IngestContractNonce(ctx, cassata.ContractNonceResult{
			Address: addr.Hex(),
			Nonce:   strconv.FormatUint(nonce, 10),
		}); err != nil {
			return err
		}
		counts["nonces"]++
		return nil
	}); err != nil {
		log.Fatalf("migrate nonces: %v", err)
	}

	if err := migratePrefix(pebbleStore, contractDB.PrefixContractMeta, func(key, val []byte) error {
		addrBytes := key[len(contractDB.PrefixContractMeta):]
		addr := common.BytesToAddress(addrBytes)
		var meta contractDB.ContractMetadata
		if err := json.Unmarshal(val, &meta); err != nil {
			return fmt.Errorf("unmarshal contract meta: %w", err)
		}
		if err := cas.IngestContractMeta(ctx, cassata.ContractMetaResult{
			Address:         addr.Hex(),
			CodeHash:        meta.CodeHash.Hex(),
			CodeSize:        meta.CodeSize,
			DeployerAddress: meta.DeployerAddress.Hex(),
			DeploymentTx:    meta.DeploymentTxHash.Hex(),
			DeploymentBlock: meta.DeploymentBlock,
			Raw:             val,
		}); err != nil {
			return err
		}
		counts["contract_meta"]++
		return nil
	}); err != nil {
		log.Fatalf("migrate contract meta: %v", err)
	}

	if err := migratePrefix(pebbleStore, contractDB.PrefixReceipt, func(key, val []byte) error {
		hashBytes := key[len(contractDB.PrefixReceipt):]
		txHash := common.BytesToHash(hashBytes)
		var receipt contractDB.TransactionReceipt
		if err := json.Unmarshal(val, &receipt); err != nil {
			return fmt.Errorf("unmarshal receipt: %w", err)
		}
		if err := cas.IngestContractReceipt(ctx, cassata.ContractReceiptResult{
			TxHash:          txHash.Hex(),
			BlockNumber:     receipt.BlockNumber,
			TxIndex:         receipt.TxIndex,
			Status:          int16(receipt.Status),
			GasUsed:         strconv.FormatUint(receipt.GasUsed, 10),
			ContractAddress: receipt.ContractAddress.Hex(),
			RevertReason:    receipt.RevertReason,
			Raw:             val,
		}); err != nil {
			return err
		}
		counts["receipts"]++
		return nil
	}); err != nil {
		log.Fatalf("migrate receipts: %v", err)
	}

	log.Printf("migration complete: %+v", counts)
}

// migratePrefix iterates all keys with the given prefix and calls fn for each.
func migratePrefix(store *contractDB.PebbleStore, prefix []byte, fn func(key, val []byte) error) error {
	iter, err := store.NewIterator(prefix)
	if err != nil {
		return err
	}
	defer iter.Close()

	for ok := iter.First(); ok; ok = iter.Next() {
		if err := fn(iter.Key(), iter.Value()); err != nil {
			return err
		}
	}
	return nil
}
