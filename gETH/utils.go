package gETH

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/proto"
	"sort"
)

type immuDBServer struct {
	defaultdb  config.PooledConnection
	accountsdb config.PooledConnection
}

func initDBs() (immuDBServer, error) {
	defaultdb, err := DB_OPs.GetMainDBConnectionandPutBack(context.Background())
	if err != nil {
		return immuDBServer{}, err
	}

	accountsdb, err := DB_OPs.GetAccountConnectionandPutBack(context.Background())
	if err != nil {
		return immuDBServer{}, err
	}

	return immuDBServer{defaultdb: *defaultdb, accountsdb: *accountsdb}, nil
}

func ConvertZKTransactiontoETHTransaction(zktransactions []config.Transaction) ([]*proto.Transaction, error) {
	var transactions []*proto.Transaction

	for _, zktransaction := range zktransactions {

		typebytes := make([]byte, 1)
		typebytes[0] = zktransaction.Type

		// convert BigInt to bytes
		var rBytes []byte
		if zktransaction.R != nil {
			rBytes = zktransaction.R.Bytes()
		}

		// Convert BigInt to bytes
		var sBytes []byte
		if zktransaction.S != nil {
			sBytes = zktransaction.S.Bytes()
		}

		// Convert AccessList to []accesslist
		var accessTuples []*proto.AccessTuple
		for _, tuple := range zktransaction.AccessList {
			// Convert common.Address to []byte
			addrBytes := tuple.Address.Bytes()

			// Convert each storage key from common.Hash to []byte
			storageKeys := make([][]byte, len(tuple.StorageKeys))
			for i, key := range tuple.StorageKeys {
				storageKeys[i] = key.Bytes()
			}

			accessTuples = append(accessTuples, &proto.AccessTuple{
				Address:     addrBytes,
				StorageKeys: storageKeys,
			})
		}

		var valueBytes []byte
		if zktransaction.Value != nil {
			valueBytes = zktransaction.Value.Bytes()
		}

		var gasPriceBytes []byte
		if zktransaction.MaxFee != nil {
			gasPriceBytes = zktransaction.MaxFee.Bytes()
		}

		var maxPriorityFeeBytes []byte
		if zktransaction.MaxPriorityFee != nil {
			maxPriorityFeeBytes = zktransaction.MaxPriorityFee.Bytes()
		}

		var v uint32
		if zktransaction.V != nil {
			v = uint32(zktransaction.V.Uint64())
		}

		transactions = append(transactions, &proto.Transaction{
			Hash:     zktransaction.Hash.Bytes(),
			From:     zktransaction.From.Bytes(),
			To:       zktransaction.To.Bytes(),
			Input:    []byte(zktransaction.Data),
			Nonce:    zktransaction.Nonce,
			Value:    valueBytes,
			Gas:      zktransaction.GasLimit,
			GasPrice: gasPriceBytes,
			Type:     uint32(zktransaction.Type),
			R:        rBytes,
			S:        sBytes,
			V:        v,
			AccessList: &proto.AccessList{
				AccessTuples: accessTuples,
			},
			MaxFeePerGas:         gasPriceBytes,
			MaxPriorityFeePerGas: maxPriorityFeeBytes,
		})
	}

	return transactions, nil
}

func ConvertZKBlockToETHBlock(zkblock *config.ZKBlock) (*proto.Block, error) {
	Transactions, err := ConvertZKTransactiontoETHTransaction(zkblock.Transactions)
	if err != nil {
		return nil, err
	}
	return &proto.Block{
		Header: &proto.BlockHeader{
			ParentHash:          []byte(zkblock.PrevHash.Hex()),
			StateRoot:           []byte(zkblock.StateRoot.Hex()),
			ReceiptsRoot:        []byte(zkblock.TxnsRoot),
			LogsBloom:           []byte(zkblock.LogsBloom),
			Miner:               zkblock.CoinbaseAddr.Bytes(), // Convert *common.Address to []byte
			Number:              zkblock.BlockNumber,
			GasLimit:            zkblock.GasLimit,
			GasUsed:             zkblock.GasUsed,
			Timestamp:           uint64(zkblock.Timestamp),
			MixHashOrPrevRandao: zkblock.PrevHash[:],
			ExtraData:           []byte(zkblock.ExtraData),
			Hash:                zkblock.BlockHash[:],
		},
		Transactions: Transactions,
	}, nil
}

func ConvertConfigTxnToETHTransaction(Txn *config.Transaction) (*proto.Transaction, error) {

	// convert BigInt to bytes
	var rBytes []byte
	if Txn.R != nil {
		rBytes = Txn.R.Bytes()
	}

	// Convert BigInt to bytes
	var sBytes []byte
	if Txn.S != nil {
		sBytes = Txn.S.Bytes()
	}

	var valueBytes []byte
	if Txn.Value != nil {
		valueBytes = Txn.Value.Bytes()
	}

	var gasPriceBytes []byte
	if Txn.GasPrice != nil {
		gasPriceBytes = Txn.GasPrice.Bytes()
	}

	var v uint32
	if Txn.V != nil {
		v = uint32(Txn.V.Uint64())
	}

	return &proto.Transaction{
		From:     Txn.From.Bytes(),
		To:       Txn.To.Bytes(),
		Input:    []byte(Txn.Data),
		Nonce:    Txn.Nonce,
		Value:    valueBytes,
		Gas:      Txn.GasLimit,
		GasPrice: gasPriceBytes,
		R:        rBytes,
		S:        sBytes,
		V:        v,
		Type:     0,
		AccessList: &proto.AccessList{
			AccessTuples: nil,
		},
	}, nil
}

func ConvertGETHBlocktoReceipt(block *proto.Block) (*proto.Receipt, error) {
	return &proto.Receipt{
		TxHash:            block.Header.Hash,
		Status:            1,
		CumulativeGasUsed: block.Header.GasUsed,
		GasUsed:           block.Header.GasUsed,
		Logs:              nil,
		ContractAddress:   nil,
		Type:              0,
		BlockHash:         block.Header.Hash,
		BlockNumber:       block.Header.Number,
		TransactionIndex:  0,
	}, nil
}

func SortTransactionsByNonce(transactions []*config.Transaction) []*config.Transaction {
	// Create a copy of the slice to avoid modifying the original
	sorted := make([]*config.Transaction, len(transactions))
	copy(sorted, transactions)

	// Sort the transactions by nonce
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Nonce < sorted[j].Nonce
	})

	return sorted
}

// HashTransactions creates a SHA-256 hash of all transactions
func HashTransactions(transactions []*config.Transaction) (string, error) {
	// Sort transactions by nonce for consistent ordering
	sortedTxs := SortTransactionsByNonce(transactions)

	// Create a new SHA-256 hash
	hasher := sha256.New()

	// Process each transaction
	for _, tx := range sortedTxs {
		// Convert transaction to JSON for hashing
		txJSON, err := json.Marshal(tx)
		if err != nil {
			return "", fmt.Errorf("failed to marshal transaction: %w", err)
		}

		// Write the transaction data to the hash
		if _, err := hasher.Write(txJSON); err != nil {
			return "", fmt.Errorf("failed to hash transaction: %w", err)
		}
	}

	// Get the final hash and return as hex string
	hashBytes := hasher.Sum(nil)
	return hex.EncodeToString(hashBytes), nil
}
