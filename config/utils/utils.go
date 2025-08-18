package utils

import (
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

type Transaction struct {
	From                 *common.Address // Sender's address
	ChainID              *big.Int
	Nonce                uint64
	To                   *common.Address // nil for contract creation
	Value                *big.Int
	Data                 []byte
	GasLimit             uint64
	GasPrice             *big.Int          // Only for Type 0 and Type 1
	MaxPriorityFeePerGas *big.Int          // Only for Type 2
	MaxFeePerGas         *big.Int          // Only for Type 2
	AccessList           config.AccessList // Now uses the locally defined type     // For EIP-2930 (Type 1) and EIP-1559 (Type 2)
	V, R, S              *big.Int          // Signature values
}

func ConvertZKBlockTransactionToTransaction(tx *config.ZKBlockTransaction) (*Transaction, error) {
	var err error
	// 1. Convert `From` string to common.Address
	var from common.Address
	if tx.From[:3] == "did" {
		temp, err := DB_OPs.ExtractAddressFromDID(tx.From)
		if err != nil {
			panic(fmt.Sprintf("invalid From: %s", tx.From))
		}
		from = temp
	} else {
		from = common.HexToAddress(tx.From)
	}

	// 2. Convert `ChainID` string to big.Int
	// Convert string to *big.Int with error handling
	var chainID *big.Int
	chainID, err = StrToBigIntBase(tx.ChainID, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid ChainID: %s", tx.ChainID)
	}

	// 3. Convert `Nonce` string to uint64
	var nonce uint64
	nonce, err = StrToUint64(tx.Nonce, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid Nonce: %s", tx.Nonce)
	}

	// 4. Convert `To` string to common.Address
	var to common.Address
	if tx.To[:3] == "did" {
		temp, err := DB_OPs.ExtractAddressFromDID(tx.To)
		if err != nil {
			return nil, fmt.Errorf("invalid To: %s", tx.To)
		}
		to = temp
	} else {
		to = common.HexToAddress(tx.To)
	}

	// 5. Convert `Value` string to big.Int
	var value *big.Int
	value, err = StrToBigIntBase(tx.Value, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid Value: %s", tx.Value)
	}

	// 6. Convert string to bytes
	data := common.FromHex(tx.Data)

	// 7. Convert `GasLimit` string to uint64
	var gasLimit uint64
	gasLimit, err = StrToUint64(tx.GasLimit, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid GasLimit: %s", tx.GasLimit)
	}

	// 8. Convert `GasPrice` string to big.Int
	var gasPrice *big.Int
	gasPrice, err = StrToBigIntBase(tx.GasPrice, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid GasPrice: %s", tx.GasPrice)
	}

	// 9. Convert `MaxPriorityFeePerGas` string to big.Int
	var maxPriorityFeePerGas *big.Int
	if tx.Type == "2" {
		if tx.MaxPriorityFee == "" {
			maxPriorityFeePerGas = big.NewInt(0)
		} else {
			maxPriorityFeePerGas, err = StrToBigIntBase(tx.MaxPriorityFee, 10)
			if err != nil {
				return nil, fmt.Errorf("invalid MaxPriorityFeePerGas: %s", tx.MaxPriorityFee)
			}
		}
	}
	// 10. Convert `MaxFeePerGas` string to big.Int
	var maxFeePerGas *big.Int
	if tx.Type == "2" {
		if tx.MaxFee == "" {
			maxFeePerGas = big.NewInt(0)
		} else {
			maxFeePerGas, err = StrToBigIntBase(tx.MaxFee, 10)
			if err != nil {
				return nil, fmt.Errorf("invalid MaxFeePerGas: %s", tx.MaxFee)
			}
		}
	}

	// 12. Convert `V` string to big.Int
	var v *big.Int
	v, err = StrToBigIntBase(tx.V, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid V: %s", tx.V)
	}

	// 13. Convert `R` string to big.Int
	// Convert string to *big.Int with error handling
	var r *big.Int
	r, err = StrToBigIntBase(tx.R, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid R: %s", tx.R)
	}

	// 14. Convert `S` string to big.Int
	// Convert string to *big.Int with error handling
	var s *big.Int
	s, err = StrToBigIntBase(tx.S, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid S: %s", tx.S)
	}

	return &Transaction{
		From:                 &from,
		ChainID:              chainID,
		Nonce:                nonce,
		To:                   &to,
		Value:                value,
		Data:                 data,
		GasLimit:             gasLimit,
		GasPrice:             gasPrice,
		MaxPriorityFeePerGas: maxPriorityFeePerGas,
		MaxFeePerGas:         maxFeePerGas,
		AccessList:           tx.AccessList,
		V:                    v,
		R:                    r,
		S:                    s,
	}, nil
}
