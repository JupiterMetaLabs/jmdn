package Security

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"gossipnode/config"

	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"go.opentelemetry.io/otel/attribute"

	"gossipnode/DB_OPs"
)

const (
	LOG_FILE        = ""
	TOPIC           = "SecurityModule"
	LOKI_BATCH_SIZE = 128 * 1024
	LOKI_BATCH_WAIT = 1 * time.Second
	LOKI_TIMEOUT    = 5 * time.Second
	KEEP_LOGS       = true
)

// expectedChainID holds the node's configured chain ID for validation.
// Set this at startup using SetExpectedChainID/SetExpectedChainIDBig.
var expectedChainID *big.Int

// SetExpectedChainID sets the expected chain ID used to validate incoming transactions.
func SetExpectedChainID(id int) {
	expectedChainID = big.NewInt(int64(id))
}

// SetExpectedChainIDBig sets the expected chain ID from a big.Int safely.
func SetExpectedChainIDBig(id *big.Int) {
	if id == nil {
		expectedChainID = nil
		return
	}

	expectedChainID = new(big.Int).Set(id)

	// Convert to uint64 safely
	chainIDUint := expectedChainID.Uint64()

	// Convert to binary (big-endian) representation
	chainIDBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(chainIDBytes, chainIDUint)
}

func CheckZKBlockValidation(zkBlock *config.ZKBlock) (bool, error) {
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize Security Cache and Load Accounts
	security_cache := NewSecurityCache()
	defer security_cache.Close()

	tracer := logger().Tracer("Security")
	traceCtx, span := tracer.Start(loggerCtx, "Security.CheckZKBlockValidation")
	defer span.End()

	startTime := time.Now().UTC()

	// Check the ZKBlock nil or not
	if zkBlock == nil {
		err := errors.New("zkBlock cannot be nil")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(traceCtx, "ZKBlock is nil", err,
			ion.String("function", "Security.CheckZKBlockValidation"))
		return false, err
	}

	// Early return if no transactions
	if len(zkBlock.Transactions) == 0 {
		err := errors.New("zkBlock has no transactions")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(traceCtx, "ZKBlock has no transactions", err,
			ion.String("function", "Security.CheckZKBlockValidation"))
		return false, err
	}

	span.SetAttributes(
		attribute.Int("transaction_count", len(zkBlock.Transactions)),
		attribute.String("block_hash", zkBlock.BlockHash.Hex()),
	)

	// Get connections ONCE for all transaction validations
	// This reduces connection usage from N×2 to just 2 per block validation
	ctx, cancelConn := context.WithTimeout(traceCtx, 60*time.Second)
	defer cancelConn()

	accountsConn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "connection_failed"))
		logger().Error(traceCtx, "Failed to get accounts connection for ZKBlock validation", err,
			ion.String("function", "Security.CheckZKBlockValidation"))
		return false, fmt.Errorf("failed to get accounts connection for ZKBlock validation: %w", err)
	}
	defer DB_OPs.PutAccountsConnection(accountsConn)

	mainDBConn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "connection_failed"))
		logger().Error(traceCtx, "Failed to get main DB connection for ZKBlock validation", err,
			ion.String("function", "Security.CheckZKBlockValidation"))
		return false, fmt.Errorf("failed to get main DB connection for ZKBlock validation: %w", err)
	}
	defer DB_OPs.PutMainDBConnection(mainDBConn)

	/*
		// Load all the accounts into the cache
		// Query and update the cache with the accounts directly then give confirmation of true or false
		// This is done to avoid double spends by changing the balances in cache in realtime rather than commiting in db.
		// Along with this major security check, it will also helps in faster convergence.
	*/

	// 1. Check the ZKBlock validation for Transactions in the ZKBlock
	// Reuse the same connections/cache for all transactions
	txValidationCtx, txValidationSpan := tracer.Start(traceCtx, "Security.CheckZKBlockValidation.validateTransactions")
	txValidationSpan.SetAttributes(attribute.Int("transaction_count", len(zkBlock.Transactions)))

	// Collect all unique addresses from transactions
	accountsSet := DB_OPs.NewAccountsSet()
	for _, tx := range zkBlock.Transactions {
		accountsSet.Add(*tx.From)
		if tx.To != nil {
			accountsSet.Add(*tx.To)
		}
	}

	// Load all accounts into the cache at once
	security_cache.LoadAccounts(txValidationCtx, accountsConn, accountsSet)

	validatedCount := 0
	for i, tx := range zkBlock.Transactions {
		txSpanCtx, txSpan := tracer.Start(txValidationCtx, fmt.Sprintf("Security.CheckZKBlockValidation.validateTransaction[%d]", i))
		txSpan.SetAttributes(
			attribute.Int("transaction_index", i),
			attribute.String("tx_hash", tx.Hash.Hex()),
		)

		// Pass SecurityCache instead of accountsConn
		status, err := allChecksWithConn(&tx, security_cache, mainDBConn, txSpanCtx)
		if err != nil {
			txSpan.RecordError(err)
			txSpan.SetAttributes(attribute.String("status", "validation_failed"))
			txSpan.End()
			txValidationSpan.RecordError(err)
			txValidationSpan.SetAttributes(attribute.String("status", "validation_failed"))
			txValidationSpan.End()
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "validation_failed"))
			logger().Error(traceCtx, "Transaction validation failed", err,
				ion.Int("transaction_index", i),
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.String("function", "Security.CheckZKBlockValidation"))
			return false, err
		}
		if !status {
			err := errors.New("zkBlock validation failed")
			txSpan.SetAttributes(attribute.String("status", "validation_failed"))
			txSpan.End()
			txValidationSpan.RecordError(err)
			txValidationSpan.SetAttributes(attribute.String("status", "validation_failed"))
			txValidationSpan.End()
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "validation_failed"))
			logger().Error(traceCtx, "Transaction validation returned false", err,
				ion.Int("transaction_index", i),
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.String("function", "Security.CheckZKBlockValidation"))
			return false, err
		}
		validatedCount++
		txSpan.SetAttributes(attribute.String("status", "success"))
		txSpan.End()
	}
	txValidationSpan.SetAttributes(
		attribute.Int("validated_count", validatedCount),
		attribute.String("status", "success"),
	)
	txValidationSpan.End()

	// 2. Check the ZKBlock.Hash validation - this is the hash of all transaction's hashes
	_, hashCheckSpan := tracer.Start(traceCtx, "Security.CheckZKBlockValidation.validateBlockHash")
	// First compute the hash of all transaction's hashes
	transactionHashes := make([][]byte, len(zkBlock.Transactions))
	for i, tx := range zkBlock.Transactions {
		transactionHashes[i] = tx.Hash.Bytes()
	}
	transactionHashesHash := crypto.Keccak256Hash(bytes.Join(transactionHashes, []byte{}))
	hashCheckSpan.SetAttributes(
		attribute.String("computed_hash", transactionHashesHash.Hex()),
		attribute.String("provided_hash", zkBlock.BlockHash.Hex()),
	)

	if transactionHashesHash != zkBlock.BlockHash {
		err := errors.New("zkBlock hash validation failed")
		hashCheckSpan.RecordError(err)
		hashCheckSpan.SetAttributes(attribute.String("status", "validation_failed"))
		hashCheckSpan.End()
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(traceCtx, "ZKBlock hash validation failed", err,
			ion.String("computed_hash", transactionHashesHash.Hex()),
			ion.String("provided_hash", zkBlock.BlockHash.Hex()),
			ion.String("function", "Security.CheckZKBlockValidation"))
		return false, err
	}
	hashCheckSpan.SetAttributes(attribute.String("status", "success"))
	hashCheckSpan.End()

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
		attribute.Int("validated_transactions", validatedCount),
	)
	logger().Info(traceCtx, "ZKBlock validation completed successfully",
		ion.Int("transaction_count", len(zkBlock.Transactions)),
		ion.Int("validated_transactions", validatedCount),
		ion.Float64("duration", duration),
		ion.String("function", "Security.CheckZKBlockValidation"))
	return true, nil
}

// AllChecks validates a single transaction by acquiring its own connections.
// This is a backward-compatible wrapper for standalone transaction validation.
// For batch validation (e.g., ZKBlock), use allChecksWithConn for better performance.
func AllChecks(tx *config.Transaction) (bool, error) {
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	security_cache := NewSecurityCache()
	defer security_cache.Close()

	// get the to and from addresses
	toAddress := tx.To
	fromAddress := tx.From

	tracer := logger().Tracer("Security")
	traceCtx, span := tracer.Start(loggerCtx, "Security.AllChecks")
	defer span.End()

	startTime := time.Now().UTC()

	if tx != nil {
		toAttr := "<contract creation>"
		if tx.To != nil {
			toAttr = tx.To.Hex()
		}
		span.SetAttributes(
			attribute.String("tx_hash", tx.Hash.Hex()),
			attribute.String("from_address", tx.From.Hex()),
			attribute.String("to_address", toAttr),
			attribute.Int64("nonce", int64(tx.Nonce)),
		)
	}

	ctx, cancelConn := context.WithTimeout(traceCtx, 30*time.Second)
	defer cancelConn()

	// Get connections for single transaction validation
	accountsConn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "connection_failed"))
		logger().Error(traceCtx, "Failed to get accounts connection", err,
			ion.String("function", "Security.AllChecks"))
		return false, fmt.Errorf("failed to get accounts connection: %w", err)
	}
	defer DB_OPs.PutAccountsConnection(accountsConn)

	mainDBConn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "connection_failed"))
		logger().Error(traceCtx, "Failed to get main DB connection", err,
			ion.String("function", "Security.AllChecks"))
		return false, fmt.Errorf("failed to get main DB connection: %w", err)
	}
	defer DB_OPs.PutMainDBConnection(mainDBConn)

	// Collect all unique addresses from transactions
	accountsSet := DB_OPs.NewAccountsSet()
	accountsSet.Add(*fromAddress)
	if toAddress != nil {
		accountsSet.Add(*toAddress)
	}

	security_cache.LoadAccounts(loggerCtx, accountsConn, accountsSet)

	result, err := allChecksWithConn(tx, security_cache, mainDBConn, traceCtx)

	duration := time.Since(startTime).Seconds()
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(
			attribute.String("status", "validation_failed"),
			attribute.Float64("duration", duration),
		)
	} else {
		span.SetAttributes(
			attribute.String("status", "success"),
			attribute.Bool("valid", result),
			attribute.Float64("duration", duration),
		)
	}

	return result, err
}

// allChecksWithConn validates a transaction using provided database connections.
// This internal function enables connection reuse for batch validation (e.g., ZKBlock).
// Connection lifecycle is managed by the caller.
func allChecksWithConn(tx *config.Transaction, security_cache *SecurityCache, mainDBConn *config.PooledConnection, traceCtx context.Context) (bool, error) {
	loggerCtx, cancel := context.WithCancel(traceCtx)
	defer cancel()

	tracer := logger().Tracer("Security")
	spanCtx, span := tracer.Start(loggerCtx, "Security.allChecksWithCache")
	defer span.End()

	startTime := time.Now().UTC()

	// Validate inputs
	if security_cache == nil {
		err := errors.New("SecurityCache is nil")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(spanCtx, "SecurityCache is nil", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}
	if mainDBConn == nil || mainDBConn.Client == nil {
		err := errors.New("main DB connection is nil or invalid")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(spanCtx, "Main DB connection is nil or invalid", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}

	if tx != nil {
		toAttr := "<contract creation>"
		if tx.To != nil {
			toAttr = tx.To.Hex()
		}
		span.SetAttributes(
			attribute.String("tx_hash", tx.Hash.Hex()),
			attribute.String("from_address", tx.From.Hex()),
			attribute.String("to_address", toAttr),
			attribute.Int64("nonce", int64(tx.Nonce)),
		)
	}

	// ------------------------------------------------------------
	// 1. ChainID validation
	_, chainIDSpan := tracer.Start(spanCtx, "Security.allChecksWithCache.validateChainID")
	// 1.1. ChainID validation: expected chain ID must be configured first
	if expectedChainID == nil {
		err := errors.New("expected chain ID is not configured")
		chainIDSpan.RecordError(err)
		chainIDSpan.SetAttributes(attribute.String("status", "validation_failed"))
		chainIDSpan.End()
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(spanCtx, "Expected chain ID not configured", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}

	// 1.2. Transaction and its ChainID must be present
	if tx == nil || tx.ChainID == nil {
		err := errors.New("transaction or chain ID is missing")
		chainIDSpan.RecordError(err)
		chainIDSpan.SetAttributes(attribute.String("status", "validation_failed"))
		chainIDSpan.End()
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(spanCtx, "Transaction or ChainID is missing", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}

	// 1.3. Transaction ChainID must match expected ChainID
	if tx.ChainID.Cmp(expectedChainID) != 0 {
		err := fmt.Errorf("chain ID mismatch: got %s (uint64: %d), expected %s (uint64: %d)",
			tx.ChainID.String(), tx.ChainID.Uint64(), expectedChainID.String(), expectedChainID.Uint64())
		chainIDSpan.RecordError(err)
		chainIDSpan.SetAttributes(
			attribute.String("status", "validation_failed"),
			attribute.String("tx_chain_id", tx.ChainID.String()),
			attribute.Int64("tx_chain_id_uint64", int64(tx.ChainID.Uint64())),
			attribute.String("expected_chain_id", expectedChainID.String()),
			attribute.Int64("expected_chain_id_uint64", int64(expectedChainID.Uint64())),
		)
		chainIDSpan.End()
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(spanCtx, "Chain ID mismatch", err,
			ion.String("tx_chain_id", tx.ChainID.String()),
			ion.Int64("tx_chain_id_uint64", int64(tx.ChainID.Uint64())),
			ion.String("expected_chain_id", expectedChainID.String()),
			ion.Int64("expected_chain_id_uint64", int64(expectedChainID.Uint64())),
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}
	chainIDSpan.SetAttributes(attribute.String("status", "success"))
	chainIDSpan.End()

	// ------------------------------------------------------------
	// 2. Transaction hash validation
	hashCtx, hashSpan := tracer.Start(spanCtx, "Security.allChecksWithCache.validateHash")
	status, err := checkTransactionHash(tx, hashCtx)
	if err != nil {
		hashSpan.RecordError(err)
		hashSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Failed to verify transaction hash", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, fmt.Errorf("transaction hash validation failed: %w", err)
	}
	if !status {
		err := errors.New("transaction hash mismatch")
		hashSpan.RecordError(err)
		hashSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Transaction hash mismatch", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}
	hashSpan.End()

	// ------------------------------------------------------------
	// 3. Signature validation
	sigCtx, sigSpan := tracer.Start(spanCtx, "Security.allChecksWithCache.validateSignature")
	status, err = CheckSignature(tx, sigCtx)
	if err != nil {
		sigSpan.RecordError(err)
		sigSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Failed to check Signature", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, fmt.Errorf("signature recovery failed: %w", err)
	}
	if !status {
		err := errors.New("invalid signature")
		sigSpan.RecordError(err)
		sigSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Invalid Signature", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}
	sigSpan.End()

	// ------------------------------------------------------------
	// 4. Accounts exist (USING CACHE)
	addrCtx, addrSpan := tracer.Start(spanCtx, "Security.allChecksWithCache.validateAddressExist")

	// We need CheckAddressExistWithCache
	status, err = security_cache.CheckAddressExistWithCache(tx, addrCtx)
	if err != nil {
		addrSpan.RecordError(err)
		addrSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Failed to check Address Exist with Cache", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}
	if !status {
		err := errors.New("sender or receiver DID not found")
		addrSpan.RecordError(err)
		addrSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Sender or receiver DID not found in cache", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}
	addrSpan.End()

	// ------------------------------------------------------------
	// 5. Balance validation (USING CACHE)
	balanceCtx, balanceSpan := tracer.Start(spanCtx, "Security.allChecksWithCache.validateBalance")

	// We need CheckBalanceWithCache
	status, err = security_cache.CheckBalanceWithCache(tx, balanceCtx)
	if err != nil {
		balanceSpan.RecordError(err)
		balanceSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Failed to check Balance with Cache", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}
	if !status {
		err := errors.New("insufficient funds for transaction")
		balanceSpan.RecordError(err)
		balanceSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Insufficient Funds", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}
	balanceSpan.End()

	// ------------------------------------------------------------
	// 6. Nonce validation (USING CACHE)
	_, nonceSpan := tracer.Start(spanCtx, "Security.allChecksWithCache.validateNonce")
	hasDuplicate, latestNonce, hasAnyTransactions, err := DB_OPs.CheckNonceAndGetLatest(mainDBConn, tx.From, tx.Nonce)
	if err != nil {
		nonceSpan.RecordError(err)
		nonceSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Failed to check nonce", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, fmt.Errorf("nonce check failed with error: %w", err)
	}

	nonceSpan.SetAttributes(
		attribute.Bool("has_duplicate", hasDuplicate),
		attribute.Int64("latest_nonce", int64(latestNonce)),
	)

	if hasDuplicate {
		err := fmt.Errorf("transaction with same nonce already exists")
		nonceSpan.RecordError(err)
		nonceSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Duplicate nonce detected", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}

	var minAllowedNonce uint64
	if !hasAnyTransactions {
		minAllowedNonce = 0
	} else {
		minAllowedNonce = latestNonce + 1
	}

	if tx.Nonce < minAllowedNonce {
		err := fmt.Errorf("submitted nonce %d is too low, must be >= %d", tx.Nonce, minAllowedNonce)
		nonceSpan.RecordError(err)
		nonceSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Nonce is too low", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}
	nonceSpan.End()

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
	)
	logger().Info(spanCtx, "Transaction is valid (Cached)",
		ion.Float64("duration", duration),
		ion.String("function", "Security.allChecksWithCache"))
	return true, nil
}

// CheckSignature verifies if the transaction signature is valid
func CheckSignature(tx *config.Transaction, traceCtx context.Context) (bool, error) {
	loggerCtx, cancel := context.WithCancel(traceCtx)
	defer cancel()

	tracer := logger().Tracer("Security")
	spanCtx, span := tracer.Start(loggerCtx, "Security.CheckSignature")
	defer span.End()

	startTime := time.Now().UTC()

	if tx == nil {
		err := errors.New("transaction cannot be nil")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(spanCtx, "Transaction is nil", err,
			ion.String("function", "Security.CheckSignature"))
		return false, err
	}

	if tx.From != nil {
		span.SetAttributes(attribute.String("from_address", tx.From.Hex()))
	}
	if tx.To != nil {
		span.SetAttributes(attribute.String("to_address", tx.To.Hex()))
	}

	// tx.To is intentionally nil for contract creation transactions; do not require it here.
	if tx.From == nil || tx.V == nil || tx.R == nil || tx.S == nil {
		err := errors.New("transaction missing required signature fields (From, V, R, or S)")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(spanCtx, "Transaction missing required signature fields", err,
			ion.String("function", "Security.CheckSignature"))
		return false, err
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
		span.SetAttributes(attribute.String("tx_type", "EIP-1559"))

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
		span.SetAttributes(attribute.String("tx_type", "EIP-2930"))

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
		span.SetAttributes(attribute.String("tx_type", "Legacy"))
	}

	// 👇 Smart signer detection with fallback for MetaMask compatibility
	v := tx.V.Uint64()
	var from common.Address
	var err error

	if tx.ChainID != nil {
		span.SetAttributes(
			attribute.Int64("v_value", int64(v)),
			attribute.String("chain_id", tx.ChainID.String()),
		)
	}

	logger().Info(spanCtx, "Starting signature check",
		ion.Int64("v_value", int64(v)),
		ion.String("chain_id", tx.ChainID.String()),
		ion.String("function", "Security.CheckSignature"))

	// Strategy: MetaMask signs legacy transactions with V=27/28 (pre-EIP-155)
	// Even when ChainID is present, we need to try both signers
	if v == 27 || v == 28 {
		// First try HomesteadSigner (pre-EIP-155) - this is what MetaMask uses
		signer = types.HomesteadSigner{}
		logger().Info(spanCtx, "Trying HomesteadSigner (pre-EIP-155, MetaMask standard)",
			ion.String("function", "Security.CheckSignature"))
		from, err = types.Sender(signer, ethTx)
		if err == nil && from == *tx.From {
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(
				attribute.String("status", "success"),
				attribute.String("signer_type", "HomesteadSigner"),
				attribute.Float64("duration", duration),
			)
			logger().Info(spanCtx, "Signature verified with HomesteadSigner",
				ion.Float64("duration", duration),
				ion.String("function", "Security.CheckSignature"))
			return true, nil
		}

		// If that failed and ChainID is present, try EIP155Signer as fallback
		if tx.ChainID != nil && err != nil {
			signer = types.NewEIP155Signer(tx.ChainID)
			logger().Info(spanCtx, "HomesteadSigner failed, trying EIP155Signer",
				ion.String("error", err.Error()),
				ion.String("chain_id", tx.ChainID.String()),
				ion.String("function", "Security.CheckSignature"))
			from, err = types.Sender(signer, ethTx)
			if err == nil && from == *tx.From {
				duration := time.Since(startTime).Seconds()
				span.SetAttributes(
					attribute.String("status", "success"),
					attribute.String("signer_type", "EIP155Signer"),
					attribute.Float64("duration", duration),
				)
				logger().Info(spanCtx, "Signature verified with EIP155Signer",
					ion.Float64("duration", duration),
					ion.String("function", "Security.CheckSignature"))
				return true, nil
			}
		}
	} else {
		// V != 27/28 means EIP-155 encoded (V = chainID*2 + 35 or chainID*2 + 36)
		signer = types.NewEIP155Signer(tx.ChainID)
		logger().Info(spanCtx, "Trying EIP155Signer (EIP-155 encoded V value)",
			ion.String("function", "Security.CheckSignature"))
		from, err = types.Sender(signer, ethTx)
		if err == nil && from == *tx.From {
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(
				attribute.String("status", "success"),
				attribute.String("signer_type", "EIP155Signer"),
				attribute.Float64("duration", duration),
			)
			logger().Info(spanCtx, "Signature verified with EIP155Signer",
				ion.Float64("duration", duration),
				ion.String("function", "Security.CheckSignature"))
			return true, nil
		}
	}

	// If we get here, signature verification failed
	if err != nil {
		duration := time.Since(startTime).Seconds()
		span.RecordError(err)
		span.SetAttributes(
			attribute.String("status", "validation_failed"),
			attribute.Float64("duration", duration),
		)
		logger().Error(spanCtx, "Signature recovery failed with all signers", err,
			ion.Float64("duration", duration),
			ion.String("function", "Security.CheckSignature"))
		return false, fmt.Errorf("failed to recover sender address from signature -> %w", err)
	}

	// Signature recovered but address doesn't match
	duration := time.Since(startTime).Seconds()
	err = errors.New("signature address does not match transaction From address")
	span.RecordError(err)
	span.SetAttributes(
		attribute.String("status", "validation_failed"),
		attribute.String("recovered_address", from.Hex()),
		attribute.String("expected_address", tx.From.Hex()),
		attribute.Float64("duration", duration),
	)
	logger().Error(spanCtx, "Signature recovered but address mismatch", err,
		ion.String("recovered_address", from.Hex()),
		ion.String("expected_address", tx.From.Hex()),
		ion.Float64("duration", duration),
		ion.String("function", "Security.CheckSignature"))
	return false, err
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

// ------------------------------------------------------------
// 2. Transaction hash validation
// 2.1. Recompute transaction hash and verify it matches the provided hash
func checkTransactionHash(tx *config.Transaction, traceCtx context.Context) (bool, error) {
	loggerCtx, cancel := context.WithCancel(traceCtx)
	defer cancel()

	tracer := logger().Tracer("Security")
	spanCtx, span := tracer.Start(loggerCtx, "Security.checkTransactionHash")
	defer span.End()

	startTime := time.Now().UTC()
	if tx == nil {
		err := errors.New("transaction cannot be nil")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(spanCtx, "Transaction is nil", err,
			ion.String("function", "Security.checkTransactionHash"))
		return false, err
	}

	span.SetAttributes(attribute.String("tx_hash", tx.Hash.Hex()))

	// Construct the transaction the same way as CheckSignature does
	var ethTx *types.Transaction

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
	}

	// Compute the hash from the constructed transaction
	computedHash := ethTx.Hash()

	// Compare with the provided hash
	span.SetAttributes(
		attribute.String("computed_hash", computedHash.Hex()),
		attribute.String("provided_hash", tx.Hash.Hex()),
	)

	if computedHash != tx.Hash {
		err := fmt.Errorf("transaction hash mismatch: computed %s, provided %s",
			computedHash.Hex(), tx.Hash.Hex())
		duration := time.Since(startTime).Seconds()
		span.RecordError(err)
		span.SetAttributes(
			attribute.String("status", "validation_failed"),
			attribute.Float64("duration", duration),
		)
		logger().Error(spanCtx, "Transaction hash mismatch", err,
			ion.String("computed_hash", computedHash.Hex()),
			ion.String("provided_hash", tx.Hash.Hex()),
			ion.Float64("duration", duration),
			ion.String("function", "Security.checkTransactionHash"))
		return false, err
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Float64("duration", duration),
	)
	logger().Info(spanCtx, "Transaction hash validated successfully",
		ion.Float64("duration", duration),
		ion.String("function", "Security.checkTransactionHash"))
	return true, nil
}
