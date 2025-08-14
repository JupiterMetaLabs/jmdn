package Security

import (
	"errors"
	"fmt"
	"gossipnode/config"
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"

	"gossipnode/DB_OPs"
)

func ThreeChecks(tx *config.Transaction) (bool, error) {
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
func CheckSignature(tx *config.Transaction) (bool, error) {
	if tx == nil {
		return false, errors.New("transaction cannot be nil")
	}

	if tx.From == nil {
		return false, nil
	}

	var ethTx *types.Transaction
	var signer types.Signer

	// Determine transaction type based on fields
	switch {
	case tx.MaxFeePerGas != nil && tx.MaxPriorityFeePerGas != nil:
		// EIP-1559 (Type 2)
		inner := &types.DynamicFeeTx{
			ChainID:    tx.ChainID,
			Nonce:      tx.Nonce,
			To:         tx.To,
			Value:      tx.Value,
			GasTipCap:  tx.MaxPriorityFeePerGas,
			GasFeeCap:  tx.MaxFeePerGas,
			Gas:        tx.GasLimit,
			Data:       tx.Data,
			AccessList: toGethAccessList(tx.AccessList),
			V:          tx.V,
			R:          tx.R,
			S:          tx.S,
		}
		ethTx = types.NewTx(inner)
		signer = types.NewLondonSigner(tx.ChainID)

	case len(tx.AccessList) > 0:
		// EIP-2930 (Type 1)
		inner := &types.AccessListTx{
			ChainID:    tx.ChainID,
			Nonce:      tx.Nonce,
			To:         tx.To,
			Value:      tx.Value,
			GasPrice:   tx.GasPrice,
			Gas:        tx.GasLimit,
			Data:       tx.Data,
			AccessList: toGethAccessList(tx.AccessList),
			V:          tx.V,
			R:          tx.R,
			S:          tx.S,
		}
		ethTx = types.NewTx(inner)
		signer = types.NewEIP2930Signer(tx.ChainID)

	default:
		// Legacy (Type 0)
		inner := &types.LegacyTx{
			Nonce:    tx.Nonce,
			To:       tx.To,
			Value:    tx.Value,
			GasPrice: tx.GasPrice,
			Gas:      tx.GasLimit,
			Data:     tx.Data,
			V:        tx.V,
			R:        tx.R,
			S:        tx.S,
		}
		ethTx = types.NewTx(inner)
		signer = types.NewEIP155Signer(tx.ChainID)
	}

	// Recover the sender address from the signature
	from, err := types.Sender(signer, ethTx)
	if err != nil {
		return false, err
	}

    fromDID, err := DB_OPs.ExtractAddressFromDID(tx.From.String())
    if err != nil {
        return false, fmt.Errorf("invalid sender DID: %v", err)
    }

	// Compare the recovered address with the From address
	isMatch := from == fromDID
	return isMatch, nil
}

// // extractAddressFromDID extracts the Ethereum address from a DID string
// // Format: did:jmdt:superj:0x123...
// func extractAddressFromDID(did string) (common.Address, error) {
    
//     // Check if we get the Correct Addr instead of DID then directly return the addr
//     if common.IsHexAddress(did) {
//         return common.HexToAddress(did), nil
//     }

// 	parts := strings.Split(did, ":")
// 	if len(parts) < 4 {
// 		return common.Address{}, fmt.Errorf("invalid DID format: %s", did)
// 	}
// 	// The last part should be the Ethereum address
// 	addrStr := parts[len(parts)-1]
// 	if !common.IsHexAddress(addrStr) {
// 		return common.Address{}, fmt.Errorf("invalid Ethereum address in DID: %s", addrStr)
// 	}
// 	return common.HexToAddress(addrStr), nil
// }

// CheckAddressExist verifies if both sender and receiver DIDs exist in the database
func CheckAddressExist(tx *config.Transaction, Conn *config.ImmuClient) (bool, error) {
	if tx == nil {
		return false, errors.New("transaction cannot be nil")
	}
	if tx.From == nil || tx.To == nil {
		return false, nil
	}

	// check if the db have From DID and To DID
	From, err := DB_OPs.GetDID(Conn, tx.From.String())
	if err != nil {
		return false, err
	}

	To, err := DB_OPs.GetDID(Conn, tx.To.String())
	if err != nil {
		return false, err
	}

	if From == nil || To == nil {
		return false, nil
	}

	return true, nil
}

// Function that helps to check if the From DID have sufficient balance to make a transaction
func CheckBalance(tx *config.Transaction, Conn *config.ImmuClient) (bool, error) {
	if tx == nil {
		return false, errors.New("transaction cannot be nil")
	}
	if tx.From == nil {
		return false, nil
	}

	// check if the db have From DID
	From, err := DB_OPs.GetDID(Conn, tx.From.String())
	if err != nil {
		return false, err
	}

	if From == nil {
		return false, nil
	}

	// Convert From.balance from string to big.Int
	FromBalance := new(big.Int)
	_, ok := FromBalance.SetString(From.Balance, 10)
	if !ok {
		return false, errors.New("invalid balance format in From account")
	}

	// Calculate total cost: value + (gasLimit * gasPrice)
	totalCost := new(big.Int).Set(tx.Value)

	// Calculate gas cost based on transaction type
	var gasCost *big.Int
	switch {
	case tx.MaxFeePerGas != nil && tx.MaxPriorityFeePerGas != nil:
		// EIP-1559 transaction
		gasCost = new(big.Int).Mul(big.NewInt(int64(tx.GasLimit)), tx.MaxFeePerGas)
	case tx.GasPrice != nil:
		// Legacy or EIP-2930 transaction
		gasCost = new(big.Int).Mul(big.NewInt(int64(tx.GasLimit)), tx.GasPrice)
	default:
		return false, errors.New("invalid gas pricing parameters")
	}

	totalCost.Add(totalCost, gasCost)

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
