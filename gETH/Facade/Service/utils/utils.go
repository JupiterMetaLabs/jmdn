package Utils

import (
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/utils"
	"gossipnode/gETH/Facade/Service/Types"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Convert ZKBlock to Block - May required to review this piece
func ConvertZKBlockToBlock(zkBlock *config.ZKBlock) *Types.Block {
	Header := ConvertZKBlockToblockheader(*zkBlock)
	return &Types.Block{
		Header: &Header,
		Transactions: ConvertTransactionToTxs(&zkBlock.Transactions),
		Ommers: nil,
		WithdrawalsRoot: nil,
		Withdrawals: nil,
		BlobGasUsed: nil,
		ExcessBlobGas: nil,
	}
}

// Convert Transaction to Tx - May required to review this piece
func ConvertTransactionToTxs(zkTx *[]config.Transaction) []*Types.Tx {
	txs := make([]*Types.Tx, len(*zkTx))
	for i, tx := range *zkTx {
		txs[i] = ConvertTrabsactionToTx(&tx)
	}
	return txs
}

func ConvertTrabsactionToTx(tx *config.Transaction) *Types.Tx{
	accesslist := tx.AccessList
	Txn := &Types.Tx{
		Hash:          tx.Hash.Bytes(),
		From:          tx.From.Bytes(),
		To:            tx.To.Bytes(),
		Input:         tx.Data,
		Value:         tx.Value.Bytes(),
		Nonce:         tx.Nonce,
		Gas:           tx.GasLimit,
		GasPrice:      tx.GasPrice.Bytes(),
		Type:          uint32(tx.Type),
		R:             tx.R.Bytes(),
		S:             tx.S.Bytes(),
		V:             uint32(tx.V.Uint64()),
		AccessList: &accesslist,
		MaxFeePerGas:         tx.MaxFee.Bytes(),
		MaxPriorityFeePerGas: tx.MaxPriorityFee.Bytes(),
		MaxFeePerBlobGas:     nil,
		BlobVersionedHashes:  nil,
	}
	return Txn
}

// Convert the address string to common.Address
func ConvertAddress(address string) common.Address {
	return common.HexToAddress(address)
}

// Convert the balance string to big.Int
func ConvertBalance(balance string) (*big.Int, error) {
	balanceInt, status := new(big.Int).SetString(balance, 10)
	if !status {
		return nil, fmt.Errorf("failed to convert balance from string to big.Int: invalid big.Int %q (base 10)", balance)
	}
	return balanceInt, nil
}

// convertLogsToMap converts receipt logs to a map format suitable for JSON serialization
func ConvertLogsToMap(logs []config.Log) []map[string]any {
	logMaps := make([]map[string]any, len(logs))
	for i, log := range logs {
		topics := make([]string, len(log.Topics))
		for j, topic := range log.Topics {
			topics[j] = topic.Hex()
		}

		logMaps[i] = map[string]any{
			"address":     log.Address.Hex(),
			"topics":      topics,
			"data":        fmt.Sprintf("%x", log.Data),
			"blockNumber": fmt.Sprintf("%x", log.BlockNumber),
			"blockHash":   log.BlockHash.Hex(),
			"txHash":      log.TxHash.Hex(),
			"txIndex":     fmt.Sprintf("%x", log.TxIndex),
			"logIndex":    fmt.Sprintf("%x", log.LogIndex),
			"removed":     log.Removed,
		}
	}
	return logMaps
}


// Conversion
func ConvertZKBlockToblockheader(ZKBlock config.ZKBlock) (Types.BlockHeader){
	// First Compute the Receipts
	Receipts, err := DB_OPs.GetReceiptsofBlock(nil, ZKBlock.BlockNumber)
	if err != nil {
		return Types.BlockHeader{}
	}

	// Second Compute the Receipt hash
	Receiptshash, err := utils.GenerateReceiptRoot(Receipts)
	if err != nil {
		return Types.BlockHeader{}
	}

	LogsBloom := utils.GenerateBlockLogsBloom(Receipts)

	return Types.BlockHeader{
		ParentHash: ZKBlock.PrevHash.Bytes(),
		StateRoot: ZKBlock.StateRoot.Bytes(),
		ReceiptsRoot: Receiptshash,
		LogsBloom: LogsBloom,
		Miner: ZKBlock.ZKVMAddr.Bytes(),
		Number: ZKBlock.BlockNumber,
		GasLimit: ZKBlock.GasLimit,
		GasUsed: ZKBlock.GasUsed,
		Timestamp: uint64(ZKBlock.Timestamp),
		MixHashOrPrevRandao: nil,
		BaseFee: nil,
		ExtraData: []byte(ZKBlock.ExtraData),
		Hash: ZKBlock.BlockHash.Bytes(),
	}
}
