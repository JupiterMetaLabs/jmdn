package Utils

import (
	"fmt"
	"math/big"
	"strings"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/utils"
	"gossipnode/gETH/Facade/Service/Types"

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
		L1TxHash:        zkBlock.L1TxHash,
		L1BlockNumber:   zkBlock.L1BlockNumber,
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

	var chainID []byte
	if tx.ChainID != nil {
		chainID = tx.ChainID.Bytes()
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
		ChainID:              chainID,
	}
	return Txn
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

// Convert the balance string to big.Int
func ConvertBalance(balance string) (*big.Int, error) {
	balanceInt, status := new(big.Int).SetString(balance, 10)
	if !status {
		return nil, fmt.Errorf("failed to convert balance from string to big.Int: invalid big.Int %q (base 10)", balance)
	}
	return balanceInt, nil
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

	// TODO (migration): EIP-1559 requires a persistent `BaseFee` field in the `ZKBlock` struct
	// populated by the ZKVM during execution and stored in ImmuDB, in order to avoid
	// O(N^2) backward recursive database lookups.
	// For now, since the sequencer only accepts Type 0 (Legacy) transactions which do not burn BaseFee,
	// we return a constant 35 Gwei to satisfy downstream JSON-RPC wallets (MetaMask) without triggering OOM node crashes.
	baseFee := big.NewInt(config.BaseFeeWei).Bytes()

	return Types.BlockHeader{
		ParentHash:          ZKBlock.PrevHash.Bytes(),
		StateRoot:           ZKBlock.StateRoot.Bytes(),
		ReceiptsRoot:        Receiptshash,
		TxnsRoot:            common.HexToHash(ZKBlock.TxnsRoot).Bytes(), // 32-byte txns trie root for eth_getBlock*
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
