package gETH

import(
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"strconv"
	"fmt"
	"sort"
	"encoding/json"
	"encoding/hex"
	"crypto/sha256"
	"gossipnode/gETH/proto"
)

type immuDBServer struct{
	defaultdb config.ImmuClient
	accountsdb config.ImmuClient
}

func initDBs() (immuDBServer, error){
	defaultdb, err := DB_OPs.New()
	if err != nil {
		return immuDBServer{}, err
	}

	accountsdb, err := DB_OPs.NewAccountsClient()
	if err != nil {
		return immuDBServer{}, err
	}

	return immuDBServer{defaultdb: *defaultdb, accountsdb: *accountsdb}, nil
}

func ConvertZKTransactiontoETHTransaction(zktransactions []config.ZKBlockTransaction) ([]*proto.Transaction, error) {
    var transactions []*proto.Transaction
    
    for _, zktransaction := range zktransactions {
        nonce, err := strconv.ParseUint(zktransaction.Nonce, 10, 64)
        if err != nil {
            return nil, fmt.Errorf("failed to convert nonce %s to uint64: %w", zktransaction.Nonce, err)
        }

        gas, err := strconv.ParseUint(zktransaction.GasLimit, 10, 64)
        if err != nil {
            return nil, fmt.Errorf("failed to convert gas limit %s to uint64: %w", zktransaction.GasLimit, err)
        }

        transactionType, err := strconv.ParseUint(zktransaction.Type, 10, 32)
        if err != nil {
            return nil, fmt.Errorf("failed to convert type %s to uint32: %w", zktransaction.Type, err)
        }

        // convert Big.Int to byte
        rBytes, err := zktransaction.R.MarshalText()
        if err != nil {
            return nil, fmt.Errorf("failed to convert zktransaction.R to []byte: %w", err)
        }

        sBytes, err := zktransaction.S.MarshalText()
        if err != nil {
            return nil, fmt.Errorf("failed to convert zktransaction.S to []byte: %w", err)
        }

        // Convert Big.Int to uint32
        v, err := strconv.ParseUint(zktransaction.V.String(), 10, 32)
        if err != nil {
            return nil, fmt.Errorf("failed to convert v %s to uint32: %w", zktransaction.V, err)
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

        transactions = append(transactions, &proto.Transaction{
            Hash:  []byte(zktransaction.Hash),
            From:  []byte(zktransaction.From),
            To:    []byte(zktransaction.To),
            Input: []byte(zktransaction.Data),
            Nonce: nonce,
            Value: []byte(zktransaction.Value),
            Gas:   gas,
            GasPrice: []byte(zktransaction.MaxFee),
            Type:  uint32(transactionType),
            R:     rBytes,
            S:     sBytes,
            V:     uint32(v),
            AccessList: &proto.AccessList{
                AccessTuples: accessTuples,
            },
        })
    }
    
    return transactions, nil
}

func ConvertZKBlockToETHBlock(zkblock *config.ZKBlock) (*proto.Block, error) {
	Transactions, err:= ConvertZKTransactiontoETHTransaction(zkblock.Transactions)
	if err != nil {
		return nil, err
	}
	return &proto.Block{
		Header: &proto.BlockHeader{
			ParentHash: []byte(zkblock.PrevHash.Hex()),
			StateRoot: []byte(zkblock.StateRoot.Hex()),
			ReceiptsRoot: []byte(zkblock.TxnsRoot),
			LogsBloom: []byte(zkblock.LogsBloom),
			Miner: []byte(zkblock.CoinbaseAddr),
			Number: zkblock.BlockNumber,
			GasLimit: zkblock.GasLimit,
			GasUsed: zkblock.GasUsed,
			Timestamp: uint64(zkblock.Timestamp),
			MixHashOrPrevRandao: zkblock.PrevHash[:],
			ExtraData: []byte(zkblock.ExtraData),
			Hash: zkblock.BlockHash[:],
		},
		Transactions: Transactions,
	}, nil
}

func ConvertConfigTxnToETHTransaction(Txn *config.Transaction)(*proto.Transaction, error){

	// Convert BigInt to bytes
	Value, err := Txn.Value.MarshalText()
	if err != nil {
		return nil, fmt.Errorf("failed to convert Value to []byte: %w", err)
	}

	GasPrice, err := Txn.GasPrice.MarshalText()
	if err != nil {
		return nil, fmt.Errorf("failed to convert GasPrice to []byte: %w", err)
	}

	rBytes, err := Txn.R.MarshalText()
	if err != nil {
		return nil, fmt.Errorf("failed to convert R to []byte: %w", err)
	}

	sBytes, err := Txn.S.MarshalText()
	if err != nil {
		return nil, fmt.Errorf("failed to convert S to []byte: %w", err)
	}

	// Convert Big.Int to uint32
	v, err := strconv.ParseUint(Txn.V.String(), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("failed to convert v %s to uint32: %w", Txn.V, err)
	}

	return &proto.Transaction{
		From: []byte(Txn.From.Hex()),
		To: []byte(Txn.To.Hex()),
		Input: []byte(Txn.Data),
		Nonce: uint64(Txn.Nonce),
		Value: Value,
		Gas: uint64(Txn.GasLimit),
		GasPrice: GasPrice,
		R: rBytes,
		S: sBytes,
		V: uint32(v),
		AccessList: &proto.AccessList{
			AccessTuples: nil,
		},
	}, nil	
}

func ConvertGETHBlocktoReceipt(block *proto.Block) (*proto.Receipt, error) {
	return &proto.Receipt{
		TxHash: block.Header.Hash,
		Status: 1,
		CumulativeGasUsed: block.Header.GasUsed,
		GasUsed: block.Header.GasUsed,
		Logs: nil,
		ContractAddress: nil,
		Type: 0,
		BlockHash: block.Header.Hash,
		BlockNumber: block.Header.Number,
		TransactionIndex: 0,
	}, nil
}

func SortTransactionsByNonce(transactions []*config.ZKBlockTransaction) []*config.ZKBlockTransaction {
    // Create a copy of the slice to avoid modifying the original
    sorted := make([]*config.ZKBlockTransaction, len(transactions))
    copy(sorted, transactions)
    
    // Sort the transactions by nonce
    sort.Slice(sorted, func(i, j int) bool {
        return sorted[i].Nonce < sorted[j].Nonce
    })
    
    return sorted
}

// HashTransactions creates a SHA-256 hash of all transactions
func HashTransactions(transactions []*config.ZKBlockTransaction) (string, error) {
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