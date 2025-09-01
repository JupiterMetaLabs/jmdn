package Security

import (
	"errors"
	"fmt"
	"gossipnode/config"
	"gossipnode/logging"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"go.uber.org/zap"

	"gossipnode/DB_OPs"
)

const(
	LOG_FILE        = "SecurityModule.log"
	TOPIC           = "SecurityModule"
	LOKI_BATCH_SIZE = 128 * 1024
	LOKI_BATCH_WAIT = 1 * time.Second
	LOKI_TIMEOUT    = 5 * time.Second
	KEEP_LOGS       = true
)

func ThreeChecks(tx *config.Transaction) (bool, error) {
	// Initilize the Accounts DB connection pool
	Conn, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		return false, err
	}

	// First Check Accounts exist
	status, err := CheckAddressExist(tx, Conn)
	if err != nil {
		Conn.Client.Logger.Logger.Error("Failed to check Address Exist",
			zap.Error(err),
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Security.ThreeChecks"),
		)
		return false, fmt.Errorf("DID check failed with DB error: %w", err)
	}
	if !status {
		Conn.Client.Logger.Logger.Error("Sender or receiver DID not found",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Security.ThreeChecks"),
		)
		return false, errors.New("sender or receiver DID not found")
	}

	Conn.Client.Logger.Logger.Info("DID Check: ",
		zap.Bool("DID Check", status),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.ThreeChecks"),
	)

	// Debugging
	fmt.Println("DID Check: ", status)

	// Second Check Signature
	status, err = CheckSignature(tx)
	if err != nil {
		Conn.Client.Logger.Logger.Error("Failed to check Signature",
			zap.Error(err),
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Security.ThreeChecks"),
		)
		return false, fmt.Errorf("signature recovery failed: %w", err)
	}
	if !status {
		Conn.Client.Logger.Logger.Error("Invalid Signature",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Security.ThreeChecks"),
		)
		return false, errors.New("invalid signature")
	}

	// Third Check Balance
	status, err = CheckBalance(tx, Conn)
	if err != nil {
		Conn.Client.Logger.Logger.Error("Failed to check Balance",
			zap.Error(err),
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Security.ThreeChecks"),
		)
		return false, fmt.Errorf("balance check failed with error: %w", err)
	}
	if !status {
		Conn.Client.Logger.Logger.Error("Insufficient Funds",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Security.ThreeChecks"),
		)
		return false, errors.New("insufficient funds for transaction")
	}

	Conn.Client.Logger.Logger.Info("Transaction is valid",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.ThreeChecks"),
	)
	return true, nil
}

// CheckSignature verifies if the transaction signature is valid
func CheckSignature(tx *config.Transaction) (bool, error) {
	if tx == nil {
		return false, errors.New("transaction cannot be nil")
	}

	if tx.From == nil || tx.To == nil || tx.V == nil || tx.R == nil || tx.S == nil {
		return false, nil
	}

	var ethTx *types.Transaction
	var signer types.Signer

	// Determine transaction type based on fields
	switch {
	case tx.MaxFee != nil && tx.MaxPriorityFee != nil:
		// EIP-1559 (Type 2)
		inner := &types.DynamicFeeTx{
			ChainID:    tx.ChainID,
			Nonce:      tx.Nonce,
			To:         tx.To,
			Value:      tx.Value,
			GasTipCap:  tx.MaxPriorityFee,
			GasFeeCap:  tx.MaxFee,
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
	// debugging
	fmt.Println("Signer: ", signer)

	// Recover the sender address from the signature
	from, err := types.Sender(signer, ethTx)
	if err != nil {
		return false, errors.New("failed to recover sender address from signature -> " + err.Error())
	}

	// debugging
	fmt.Println("Recovered Address: ", from)
	fmt.Println("From Address: ", tx.From)

	// Compare the recovered address with the From address
	isMatch := from == *tx.From
	return isMatch, nil
}

// CheckAddressExist verifies if both sender and receiver DIDs exist in the database
func CheckAddressExist(tx *config.Transaction, Conn *config.PooledConnection) (bool, error) {
	if tx == nil {
		Conn.Client.Logger.Logger.Error("Transaction is empty",
		zap.Error(errors.New("transaction cannot be nil")),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckAddressExist"),
	)
		return false, errors.New("transaction cannot be nil")
	}
	if tx.From == nil || tx.To == nil {
		Conn.Client.Logger.Logger.Error("From or To address is empty",
		zap.Error(errors.New("From or To address is empty")),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckAddressExist"),
	)
		return false, nil
	}

	// check if the db have From DID and To DID
	From, err := DB_OPs.GetAccount(Conn, *tx.From)
	if err != nil {
		Conn.Client.Logger.Logger.Error("Failed to get From DID from DB",
		zap.Error(errors.New("failed to get From DID from DB -> " + err.Error())),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckAddressExist"),
	)
		return false, errors.New("failed to get From DID from DB -> " + err.Error())
	}


	To, err := DB_OPs.GetAccount(Conn, *tx.To)
	if err != nil {
		Conn.Client.Logger.Logger.Error("Failed to get the Account",
		zap.Error(errors.New("failed to get To DID from DB -> " + err.Error())),
		zap.String("Target Function","DB_OPs.GetAccount"),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckAddressExist"),
	)
		return false, errors.New("failed to get To DID from DB -> " + err.Error())
	}

	if From == nil || To == nil {
		Conn.Client.Logger.Logger.Error("From or To address is empty",
		zap.Error(errors.New("From or To address is empty")),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckAddressExist"),
	)
		return false, nil
	}

	Conn.Client.Logger.Logger.Info("Successfully checked the From and To address",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckAddressExist"),
	)

	return true, nil
}

// Function that helps to check if the From DID have sufficient balance to make a transaction
func CheckBalance(tx *config.Transaction, Conn *config.PooledConnection) (bool, error) {
	if tx == nil {
		Conn.Client.Logger.Logger.Error("Transaction is empty",
		zap.Error(errors.New("transaction cannot be nil")),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)
		return false, errors.New("transaction cannot be nil")
	}

	if tx.From == nil {
		Conn.Client.Logger.Logger.Error("From address is empty",
		zap.Error(errors.New("From address is empty")),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)
		return false, nil
	}

	// check if the db have From DID
	From, err := DB_OPs.GetAccount(Conn, *tx.From)
	if err != nil {
		Conn.Client.Logger.Logger.Error("Failed to get From DID from DB",
		zap.Error(errors.New("failed to get From DID from DB -> " + err.Error())),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)
		return false, errors.New("failed to get From DID from DB -> " + err.Error())
	}

	if From == nil {
		Conn.Client.Logger.Logger.Error("From address is empty",
		zap.Error(errors.New("From address is empty")),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)
		return false, nil
	}

	// Convert From.balance from string to big.Int
	if strings.HasPrefix(From.Balance, "[") {
		Conn.Client.Logger.Logger.Info("Have Prefix [",
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)
		balanceStr := strings.Trim(From.Balance, "[]\"") // Remove any JSON array or string quotes
		From.Balance = balanceStr
	}

	FromBalance, ok := new(big.Int).SetString(From.Balance, 10)
	if !ok {
		return false, fmt.Errorf("failed to convert From balance from string to big.Int: invalid big.Int %q (base 10)", From.Balance)
	}

	// Multiply by 10^9 to convert to wei if needed
	multiplier := new(big.Int).Exp(big.NewInt(10), big.NewInt(9), nil)
	FromBalance = new(big.Int).Mul(FromBalance, multiplier)

	gasLimit, ok := new(big.Int).SetString(strconv.FormatUint(tx.GasLimit, 10), 10)
	if !ok {
		return false, fmt.Errorf("failed to parse gasLimit: invalid big.Int %q (base 10)", tx.GasLimit)
	}

	// Calculate gas cost based on transaction type
	var gasCost *big.Int
	switch {
	case tx.MaxFee != nil && tx.MaxPriorityFee != nil:
		Conn.Client.Logger.Logger.Info("Have MaxFee and MaxPriorityFee",
		zap.String("MaxFee", tx.MaxFee.String()),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)
		// EIP-1559 transaction: gas cost is gasLimit * maxFeePerGas (worst case)
		gasCost = new(big.Int).Mul(gasLimit, tx.MaxFee)

	case tx.GasPrice != nil:
		Conn.Client.Logger.Logger.Info("Have GasPrice",
		zap.String("GasPrice", tx.GasPrice.String()),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)
		// Legacy or EIP-2930 transaction: gas cost is gasLimit * gasPrice
		gasCost = new(big.Int).Mul(gasLimit, tx.GasPrice)
	default:
		Conn.Client.Logger.Logger.Info("Invalid gas pricing parameters",
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)
		return false, errors.New("invalid gas pricing parameters")
	}

	tx.Value.Add(tx.Value, gasCost)

	// Debugging
	fmt.Println("Total Cost: ", tx.Value)
	fmt.Println("From Balance: ", FromBalance)

	Conn.Client.Logger.Logger.Info("Total Cost: ",
		zap.String("Total Cost", tx.Value.String()),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)
	// Check if balance is sufficient for total cost
	if FromBalance.Cmp(tx.Value) < 0 {
		Conn.Client.Logger.Logger.Info("From Balance is less than Total Cost",
		zap.String("From Balance", FromBalance.String()),
		zap.String("Total Cost", tx.Value.String()),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)
		return false, nil
	}

	Conn.Client.Logger.Logger.Info("From Balance is sufficient for total cost",
		zap.String("From Balance", FromBalance.String()),
		zap.String("Total Cost", tx.Value.String()),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Security.CheckBalance"),
	)

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
