package Utils

import (
	"gossipnode/config"
	"math/big"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"gossipnode/gETH/Facade/Service/Types"
)

// Convert ZKBlock to Block - May required to review this piece
func ConvertZKBlockToBlock(zkBlock *config.ZKBlock) *Types.Block {
	return &Types.Block{
		Number:       big.NewInt(int64(zkBlock.BlockNumber)),
		Hash:         zkBlock.BlockHash.Hex(),
		ParentHash:   zkBlock.PrevHash.Hex(),
		Timestamp:    uint64(zkBlock.Timestamp),
		Transactions: *ConvertTransactionToTxs(&zkBlock.Transactions),
	}
}

// Convert Transaction to Tx - May required to review this piece
func ConvertTransactionToTxs(zkTx *[]config.Transaction) *[]Types.Tx{
	txs := make([]Types.Tx, len(*zkTx))
	for i, tx := range *zkTx {
		zkTx := ConvertTrabsactionToTx(&tx)
		txs[i] = *zkTx
	}
	return &txs
}

func ConvertTrabsactionToTx(tx *config.Transaction) *Types.Tx{
	Txn := &Types.Tx{
			Hash:          tx.Hash.Hex(),
			From:          tx.From.Hex(),
			To:            tx.To.Hex(),
			Input:         tx.Data,
			Value:         tx.Value,
			Nonce:         tx.Nonce,
			Gas:           big.NewInt(int64(tx.GasLimit)),
			GasPrice:      tx.GasPrice,
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