package contractDB

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests — use a real temp-dir PebbleDB (on-disk round-trip).

func TestPebblePersistence(t *testing.T) {
	dbPath := t.TempDir()

	kvStore, err := NewPebbleStore(dbPath)
	require.NoError(t, err)
	defer kvStore.Close()

	repo := NewPebbleAdapter(kvStore)
	db := NewContractDB(nil, repo) // nil DID is fine for persistence tests

	// -----------------------------------------------------------------------
	// Sub-test: ContractMetadata round-trip
	// -----------------------------------------------------------------------
	t.Run("ContractMetadata", func(t *testing.T) {
		addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
		txHash := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

		meta := ContractMetadata{
			ContractAddress:  addr,
			CodeHash:         common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
			CodeSize:         100,
			DeployerAddress:  common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcdef"),
			DeploymentTxHash: txHash,
			DeploymentBlock:  10,
			CreatedAt:        time.Now().Unix(),
		}

		require.NoError(t, db.SetContractMetadata(addr, meta))

		loaded, err := db.GetContractMetadata(addr)
		require.NoError(t, err)
		require.NotNil(t, loaded)

		assert.Equal(t, meta.ContractAddress, loaded.ContractAddress)
		assert.Equal(t, meta.CodeHash, loaded.CodeHash)
		assert.Equal(t, meta.DeploymentBlock, loaded.DeploymentBlock)
	})

	// -----------------------------------------------------------------------
	// Sub-test: TransactionReceipt round-trip
	// -----------------------------------------------------------------------
	t.Run("TransactionReceipt", func(t *testing.T) {
		txHash := common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
		contractAddr := common.HexToAddress("0x4444444444444444444444444444444444444444")

		receipt := TransactionReceipt{
			TxHash:          txHash,
			BlockNumber:     15,
			TxIndex:         2,
			Status:          1,
			GasUsed:         21000,
			ContractAddress: contractAddr,
			Logs: []*types.Log{
				{
					Address: contractAddr,
					Topics:  []common.Hash{common.HexToHash("0xdeadbeef")},
					Data:    []byte{0x01, 0x02, 0x03},
					TxHash:  txHash,
				},
			},
			CreatedAt: time.Now().Unix(),
		}

		require.NoError(t, db.WriteReceipt(receipt))

		loaded, err := db.GetReceipt(txHash)
		require.NoError(t, err)
		require.NotNil(t, loaded)

		assert.Equal(t, receipt.TxHash, loaded.TxHash)
		assert.Equal(t, receipt.Status, loaded.Status)
		require.Len(t, loaded.Logs, 1)
		assert.Equal(t, contractAddr, loaded.Logs[0].Address)
	})

	// -----------------------------------------------------------------------
	// Sub-test: StorageMetadata written by CommitToDB
	// -----------------------------------------------------------------------
	t.Run("StorageMetadata", func(t *testing.T) {
		addr := common.HexToAddress("0x5555555555555555555555555555555555555555")
		txHash := common.HexToHash("0x6666666666666666666666666666666666666666")

		db.SetTxContext(txHash, 20)

		obj := db.getOrNewStateObject(addr)
		obj.setBalance(uint256.NewInt(1000))

		key := common.HexToHash("0xaaaa")
		val := common.HexToHash("0xbbbb")
		obj.setState(key, val)

		_, err := db.CommitToDB(false)
		require.NoError(t, err)

		// Verify raw metadata key in the underlying store.
		metaKey := append(PrefixStorageMeta, append(addr.Bytes(), key.Bytes()...)...)
		data, err := kvStore.Get(metaKey)
		require.NoError(t, err)
		require.NotEmpty(t, data, "storage metadata should be written to PebbleDB")

		var sm StorageMetadata
		require.NoError(t, json.Unmarshal(data, &sm))
		assert.Equal(t, txHash, sm.LastModifiedTx)
		assert.Equal(t, uint64(20), sm.LastModifiedBlock)
	})

	// -----------------------------------------------------------------------
	// Sub-test: Code / storage survives close → reopen
	// -----------------------------------------------------------------------
	t.Run("PersistCloseReopen", func(t *testing.T) {
		// Use its own temp dir for isolation.
		reopenPath, err := os.MkdirTemp("", "pebble-reopen-*")
		require.NoError(t, err)
		defer os.RemoveAll(reopenPath)

		addr := common.HexToAddress("0x7777777777777777777777777777777777777777")
		code := []byte{0x60, 0x60, 0x60, 0x40, 0x52}
		storageKey := common.HexToHash("0x01")
		storageVal := common.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000000")

		// Write
		{
			kv, err := NewPebbleStore(reopenPath)
			require.NoError(t, err)
			db1 := NewContractDB(nil, NewPebbleAdapter(kv))
			db1.SetCode(addr, code, 0)
			db1.SetState(addr, storageKey, storageVal)
			db1.SetNonce(addr, 7, 0)
			_, err = db1.CommitToDB(false)
			require.NoError(t, err)
			require.NoError(t, kv.Close())
		}

		// Reopen and verify
		{
			kv, err := NewPebbleStore(reopenPath)
			require.NoError(t, err)
			defer kv.Close()
			db2 := NewContractDB(nil, NewPebbleAdapter(kv))
			assert.Equal(t, code, db2.GetCode(addr), "code should survive close/reopen")
			assert.Equal(t, storageVal, db2.GetState(addr, storageKey), "storage should survive close/reopen")
			assert.Equal(t, uint64(7), db2.GetNonce(addr), "nonce should survive close/reopen")
		}
	})
}
