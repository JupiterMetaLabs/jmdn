package Utils

import (
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/utils"
	"gossipnode/gETH/Facade/Service/Types"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Convert ZKBlock to Block - May required to review this piece
func ConvertZKBlockToBlock(zkBlock *config.ZKBlock) *Types.Block {
	Header := ConvertZKBlockToblockheader(*zkBlock)

	return &Types.Block{
		Header:          &Header,
		Transactions:    ConvertTransactionToTxs(&zkBlock.Transactions),
		Ommers:          nil,
		WithdrawalsRoot: nil,
		Withdrawals:     nil,
		BlobGasUsed:     nil,
		ExcessBlobGas:   nil,
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

func ConvertTrabsactionToTx(tx *config.Transaction) *Types.Tx {
	accesslist := tx.AccessList
	// Handle MaxFee with nil check
	var maxFeePerGas []byte
	if tx.MaxFee != nil {
		maxFeePerGas = tx.MaxFee.Bytes()
	} else {
		maxFeePerGas = nil
	}

	// Handle MaxPriorityFee with nil check
	var maxPriorityFeePerGas []byte
	if tx.MaxPriorityFee != nil {
		maxPriorityFeePerGas = tx.MaxPriorityFee.Bytes()
	} else {
		maxPriorityFeePerGas = nil
	}

	// Handle GasPrice with nil check
	var gasPrice []byte
	if tx.GasPrice != nil {
		gasPrice = tx.GasPrice.Bytes()
	} else {
		gasPrice = nil
	}

	// Handle Value with nil check
	var value []byte
	if tx.Value != nil {
		value = tx.Value.Bytes()
	} else {
		value = nil
	}

	// Handle From address with nil check
	var from []byte
	if tx.From != nil {
		from = tx.From.Bytes()
	} else {
		from = nil
	}

	// Handle To address with nil check
	var to []byte
	if tx.To != nil {
		to = tx.To.Bytes()
	} else {
		to = nil
	}

	// Handle R with nil check
	var r []byte
	if tx.R != nil {
		r = tx.R.Bytes()
	} else {
		r = nil
	}

	// Handle S with nil check
	var s []byte
	if tx.S != nil {
		s = tx.S.Bytes()
	} else {
		s = nil
	}

	// Handle V with nil check
	var v uint32
	if tx.V != nil {
		v = uint32(tx.V.Uint64())
	} else {
		v = 0
	}

	Txn := &Types.Tx{
		Hash:                 tx.Hash.Bytes(),
		From:                 from,
		To:                   to,
		Input:                tx.Data,
		Value:                value,
		Nonce:                tx.Nonce,
		Gas:                  tx.GasLimit,
		GasPrice:             gasPrice,
		Type:                 uint32(tx.Type),
		R:                    r,
		S:                    s,
		V:                    v,
		AccessList:           &accesslist,
		MaxFeePerGas:         maxFeePerGas,
		MaxPriorityFeePerGas: maxPriorityFeePerGas,
		MaxFeePerBlobGas:     nil,
		BlobVersionedHashes:  nil,
	}
	return Txn
}

// Convert the address string to common.Address
func ConvertAddress(address string) common.Address {
	return common.HexToAddress(address)
}

// ConvertAddressCaseInsensitive converts address string to common.Address in a case-insensitive manner
func ConvertAddressCaseInsensitive(address string) common.Address {
	// Remove 0x prefix if present
	addr := address
	if strings.HasPrefix(addr, "0x") || strings.HasPrefix(addr, "0X") {
		addr = addr[2:]
	}

	// Convert to lowercase for case-insensitive comparison
	addr = strings.ToLower(addr)

	// Add 0x prefix back
	return common.HexToAddress("0x" + addr)
}

// ConvertAddressCaseInsensitiveWithFallback tries case-insensitive conversion first, then falls back to exact match
func ConvertAddressCaseInsensitiveWithFallback(address string) common.Address {
	// First try case-insensitive conversion
	normalized := ConvertAddressCaseInsensitive(address)

	// For now, return the normalized version
	// In a real implementation, we might need to try both and see which one works
	return normalized
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

// calculateBaseFee calculates the base fee for the current block based on parent block using EIP-1559 formula
// Formula: parentBaseFee * (1 + (parentGasUsed - parentGasTarget) / parentGasTarget / 8)
// parentBlockNum is the block number of the parent (currentBlockNum - 1)
func calculateBaseFee(parentBlockNum uint64) []byte {
	// Initial base fee for genesis or first EIP-1559 block (35 gwei = 35000000000 wei)
	initialBaseFee := big.NewInt(35000000000)

	// If parent block number is 0, this is block 1, use initial base fee
	// For genesis (block 0), we handle it separately in ConvertZKBlockToblockheader
	if parentBlockNum == 0 {
		return initialBaseFee.Bytes()
	}

	// Get parent block to get its gas usage
	parentBlock, err := DB_OPs.GetZKBlockByNumber(nil, parentBlockNum)
	if err != nil {
		// If parent block doesn't exist, return initial base fee
		return initialBaseFee.Bytes()
	}

	// Get parent block's base fee (by calculating it recursively, but with a depth limit)
	// For performance, we'll calculate from parent's parent if needed
	var parentBaseFee *big.Int
	if parentBlockNum == 1 {
		parentBaseFee = initialBaseFee
	} else {
		// Get parent's base fee by calling this function on parent's parent
		parentParentNum := parentBlockNum - 1
		parentBaseFeeBytes := calculateBaseFee(parentParentNum)
		parentBaseFee = new(big.Int).SetBytes(parentBaseFeeBytes)
	}

	// Get parent block's gas usage and limit
	parentGasUsed := big.NewInt(int64(parentBlock.GasUsed))
	parentGasLimit := big.NewInt(int64(parentBlock.GasLimit))
	parentGasTarget := new(big.Int).Div(parentGasLimit, big.NewInt(2)) // Target is 50% of limit

	// Calculate base fee using EIP-1559 formula:
	// newBaseFee = parentBaseFee + parentBaseFee * (parentGasUsed - parentGasTarget) / parentGasTarget / 8
	// This is equivalent to: parentBaseFee * (1 + (parentGasUsed - parentGasTarget) / parentGasTarget / 8)
	gasDiff := new(big.Int).Sub(parentGasUsed, parentGasTarget)
	gasDiff.Mul(gasDiff, parentBaseFee)
	if parentGasTarget.Sign() > 0 {
		gasDiff.Div(gasDiff, parentGasTarget)
		gasDiff.Div(gasDiff, big.NewInt(8))
	} else {
		gasDiff = big.NewInt(0)
	}

	newBaseFee := new(big.Int).Add(parentBaseFee, gasDiff)

	// Ensure base fee doesn't go below minimum (1 wei)
	if newBaseFee.Sign() <= 0 {
		newBaseFee = big.NewInt(1)
	}

	return newBaseFee.Bytes()
}

// Conversion
func ConvertZKBlockToblockheader(ZKBlock config.ZKBlock) Types.BlockHeader {
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

	// Calculate BaseFee from parent block using EIP-1559 formula
	var baseFee []byte
	if ZKBlock.BlockNumber > 0 {
		parentBlockNum := ZKBlock.BlockNumber - 1
		baseFee = calculateBaseFee(parentBlockNum)
	} else {
		// Genesis block - use initial base fee (35 gwei)
		baseFee = big.NewInt(35000000000).Bytes()
	}
	return Types.BlockHeader{
		ParentHash:          ZKBlock.PrevHash.Bytes(),
		StateRoot:           ZKBlock.StateRoot.Bytes(),
		ReceiptsRoot:        Receiptshash,
		LogsBloom:           LogsBloom,
		Miner:               ZKBlock.ZKVMAddr.Bytes(),
		Number:              ZKBlock.BlockNumber,
		GasLimit:            ZKBlock.GasLimit,
		GasUsed:             ZKBlock.GasUsed,
		Timestamp:           uint64(ZKBlock.Timestamp),
		MixHashOrPrevRandao: nil,
		BaseFee:             baseFee,
		ExtraData:           []byte(ZKBlock.ExtraData),
		Hash:                ZKBlock.BlockHash.Bytes(),
	}
}
