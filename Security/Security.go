package Security

import (
	"errors"
	"fmt"
	"gossipnode/config"
	"math/big"
	"strings"

	"gossipnode/config/utils"

	"github.com/ethereum/go-ethereum/core/types"

	"gossipnode/DB_OPs"
)

func ThreeChecks(tx *config.ZKBlockTransaction) (bool, error) {
	// Initilize the Accounts DB connection pool
	Conn, err := DB_OPs.NewAccountsClient()
	if err != nil {
		return false, err
	}
	defer Conn.Cancel()

	// First Check DID exist
	status, err := CheckAddressExist(tx, Conn)
	if err != nil {
		return false, fmt.Errorf("DID check failed with DB error: %w", err)
	}
	if !status {
		return false, errors.New("sender or receiver DID not found")
	}

	// Debugging
	fmt.Println("DID Check: ", status)

	// Second Check Signature
	status, err = CheckSignature(tx)
	if err != nil {
		return false, fmt.Errorf("signature recovery failed: %w", err)
	}
	if !status {
		return false, errors.New("invalid signature")
	}

	// Third Check Balance
	status, err = CheckBalance(tx, Conn)
	if err != nil {
		return false, fmt.Errorf("balance check failed with error: %w", err)
	}
	if !status {
		return false, errors.New("insufficient funds for transaction")
	}

	return true, nil
}

// CheckSignature verifies if the transaction signature is valid
func CheckSignature(tx *config.ZKBlockTransaction) (bool, error) {
	if tx == nil {
		return false, errors.New("transaction cannot be nil")
	}

	if tx.From == "" || tx.To == "" || tx.V == "" || tx.R == "" || tx.S == "" {
		return false, nil
	}

	var ethTx *types.Transaction
	var signer types.Signer

	// Convert config.ZKBlockTransaction to *types.Transaction
	temp, err := utils.ConvertZKBlockTransactionToTransaction(tx)
	if err != nil {
		return false, fmt.Errorf("failed to convert transaction: %v", err)
	}

	// Determine transaction type based on fields
	switch {
	case tx.MaxFee != "" && tx.MaxPriorityFee != "":
		// EIP-1559 (Type 2)
		inner := &types.DynamicFeeTx{
			ChainID:    temp.ChainID,
			Nonce:      temp.Nonce,
			To:         temp.To,
			Value:      temp.Value,
			GasTipCap:  temp.MaxPriorityFeePerGas,
			GasFeeCap:  temp.MaxFeePerGas,
			Gas:        temp.GasLimit,
			Data:       temp.Data,
			AccessList: toGethAccessList(temp.AccessList),
			V:          temp.V,
			R:          temp.R,
			S:          temp.S,
		}
		ethTx = types.NewTx(inner)
		signer = types.NewLondonSigner(temp.ChainID)

	case len(tx.AccessList) > 0:
		// EIP-2930 (Type 1)
		inner := &types.AccessListTx{
			ChainID:    temp.ChainID,
			Nonce:      temp.Nonce,
			To:         temp.To,
			Value:      temp.Value,
			GasPrice:   temp.GasPrice,
			Gas:        temp.GasLimit,
			Data:       temp.Data,
			AccessList: toGethAccessList(temp.AccessList),
			V:          temp.V,
			R:          temp.R,
			S:          temp.S,
		}
		ethTx = types.NewTx(inner)
		signer = types.NewEIP2930Signer(temp.ChainID)

	default:
		// Legacy (Type 0)
		inner := &types.LegacyTx{
			Nonce:    temp.Nonce,
			To:       temp.To,
			Value:    temp.Value,
			GasPrice: temp.GasPrice,
			Gas:      temp.GasLimit,
			Data:     temp.Data,
			V:        temp.V,
			R:        temp.R,
			S:        temp.S,
		}
		ethTx = types.NewTx(inner)
		signer = types.NewEIP155Signer(temp.ChainID)
	}
	// debugging
	fmt.Println("Signer: ", signer)
	// Recover the sender address from the signature
	from, err := types.Sender(signer, ethTx)
	if err != nil {
		return false, errors.New("failed to recover sender address from signature -> " + err.Error())
	}

	// debugging
	fmt.Println("Recovered Address: ", from)
	fmt.Println("From Address: ", *temp.From)

	// Compare the recovered address with the From address
	isMatch := from == *temp.From
	return isMatch, nil
}

// CheckAddressExist verifies if both sender and receiver DIDs exist in the database
func CheckAddressExist(tx *config.ZKBlockTransaction, Conn *config.ImmuClient) (bool, error) {
	if tx == nil {
		return false, errors.New("transaction cannot be nil")
	}
	if tx.From == "" || tx.To == "" {
		return false, nil
	}

	// check if the db have From DID and To DID
	From, err := DB_OPs.GetDID(Conn, tx.From)
	if err != nil {
		return false, errors.New("failed to get From DID from DB -> " + err.Error())
	}


	To, err := DB_OPs.GetDID(Conn, tx.To)
	if err != nil {
		return false, errors.New("failed to get To DID from DB -> " + err.Error())
	}

	if From == nil || To == nil {
		return false, nil
	}

	return true, nil
}

// Function that helps to check if the From DID have sufficient balance to make a transaction
func CheckBalance(tx *config.ZKBlockTransaction, Conn *config.ImmuClient) (bool, error) {
	if tx == nil {
		return false, errors.New("transaction cannot be nil")
	}
	if tx.From == "" {
		return false, nil
	}

	// check if the db have From DID
	From, err := DB_OPs.GetDID(Conn, tx.From)
	if err != nil {
		return false, errors.New("failed to get From DID from DB -> " + err.Error())
	}

	if From == nil {
		return false, nil
	}

	// Convert From.balance from string to big.Int
	balanceStr := strings.Trim(From.Balance, "[]\"") // Remove any JSON array or string quotes
	FromBalance, ok := new(big.Int).SetString(balanceStr, 10)
	if !ok {
		return false, fmt.Errorf("failed to convert From balance from string to big.Int: invalid big.Int %q (base 10)", From.Balance)
	}
	// Calculate total cost: value + (gasLimit * gasPrice)
	totalCost, ok := new(big.Int).SetString(tx.Value, 10)
	if !ok {
		return false, fmt.Errorf("failed to convert value from string to big.Int: invalid big.Int %q (base 10)", tx.Value)
	}

	gasLimit, ok := new(big.Int).SetString(tx.GasLimit, 10)
	if !ok {
		return false, fmt.Errorf("failed to parse gasLimit: invalid big.Int %q (base 10)", tx.GasLimit)
	}

	// Calculate gas cost based on transaction type
	var gasCost *big.Int
	switch {
	case tx.MaxFee != "" && tx.MaxPriorityFee != "":
		// EIP-1559 transaction: gas cost is gasLimit * maxFeePerGas (worst case)
		maxFee, ok := new(big.Int).SetString(tx.MaxFee, 10)
		if !ok {
			return false, fmt.Errorf("failed to parse maxFee: invalid big.Int %q (base 10)", tx.MaxFee)
		}
		gasCost = new(big.Int).Mul(gasLimit, maxFee)
	case tx.GasPrice != "":
		// Legacy or EIP-2930 transaction: gas cost is gasLimit * gasPrice
		gasPrice, ok := new(big.Int).SetString(tx.GasPrice, 10)
		if !ok {
			return false, fmt.Errorf("failed to parse gasPrice: invalid big.Int %q (base 10)", tx.GasPrice)
		}
		gasCost = new(big.Int).Mul(gasLimit, gasPrice)
	default:
		return false, errors.New("invalid gas pricing parameters")
	}

	totalCost.Add(totalCost, gasCost)
	// Debugging
	fmt.Println("Total Cost: ", totalCost)
	fmt.Println("From Balance: ", FromBalance)
	// Check if balance is sufficient for total cost
	if FromBalance.Cmp(totalCost) < 0 {
		return false, nil
	}

	return true, nil
}

// Helper function to convert our AccessList to go-ethereum's AccessList
func toGethAccessList(accessList config.AccessList) types.AccessList {
	var result types.AccessList
	for _, at := range accessList {
		result = append(result, types.AccessTuple{
			Address:     at.Address,
			StorageKeys: at.StorageKeys,
		})
	}
	return result
}
