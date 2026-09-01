package Security

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"go.opentelemetry.io/otel/attribute"
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
// signerMu guards expectedChainID and all cached signers.
// These are set once at startup (before serving begins), so contention is negligible
// in practice — the mutex exists purely to satisfy the Go memory model and race detector.
var (
	signerMu        sync.RWMutex
	expectedChainID *big.Int
)

// Cached signers — built once when expectedChainID is set; avoids per-tx allocation.
var (
	cachedLatestSigner    types.Signer
	cachedEIP155Signer    types.Signer
	cachedHomeSteadSigner types.Signer = types.HomesteadSigner{}
)

func rebuildSignerCache() {
	// caller must hold signerMu.Lock()
	if expectedChainID != nil {
		cachedLatestSigner = types.LatestSignerForChainID(expectedChainID)
		cachedEIP155Signer = types.NewEIP155Signer(expectedChainID)
	}
}

// SetExpectedChainID sets the expected chain ID used to validate incoming transactions.
func SetExpectedChainID(id int) {
	signerMu.Lock()
	defer signerMu.Unlock()
	expectedChainID = big.NewInt(int64(id))
	rebuildSignerCache()
}

// SetExpectedChainIDBig sets the expected chain ID from a big.Int safely.
func SetExpectedChainIDBig(id *big.Int) {
	signerMu.Lock()
	defer signerMu.Unlock()
	if id == nil {
		expectedChainID = nil
		return
	}
	expectedChainID = new(big.Int).Set(id)
	rebuildSignerCache()
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

	// Connections managed by global ThebeDB handle — no pool acquisition needed.

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

	// Load all accounts into the cache at once — fail-closed on error.
	if err := security_cache.LoadAccounts(txValidationCtx, nil, accountsSet); err != nil {
		txValidationSpan.RecordError(err)
		txValidationSpan.End()
		span.RecordError(err)
		logger().Error(traceCtx, "Failed to load accounts into security cache for ZKBlock validation", err,
			ion.String("function", "Security.CheckZKBlockValidation"))
		return false, fmt.Errorf("failed to load accounts for block validation: %w", err)
	}

	validatedCount := 0
	for i, tx := range zkBlock.Transactions {
		txSpanCtx, txSpan := tracer.Start(txValidationCtx, fmt.Sprintf("Security.CheckZKBlockValidation.validateTransaction[%d]", i))
		txSpan.SetAttributes(
			attribute.Int("transaction_index", i),
			attribute.String("tx_hash", tx.Hash.Hex()),
		)

		// Pass SecurityCache instead of accountsConn
		status, err := allChecksWithConn(&tx, security_cache, nil, txSpanCtx)
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

	// 2. Block-hash binding. Must be M2b-aware: when M2bHashEnabled is on (which
	// RewardSplit requires), the sequencer sets BlockHash =
	// RecomputeBlockHashWithConsensusFields, so the legacy transactions-only
	// Keccak below would mismatch and reject EVERY block. Compute `want` exactly
	// as the sequencer and the receive path (messaging.checkBodyBinding) do:
	//   - M2b on : RecomputeBlockHashWithConsensusFields (6 fields + tx contents)
	//   - M2b off: legacy Keccak256(concat tx.Hash) — byte-identical to before.
	_, hashCheckSpan := tracer.Start(traceCtx, "Security.CheckZKBlockValidation.validateBlockHash")
	var wantBlockHash common.Hash
	if M2bHashEnabled {
		wantBlockHash = RecomputeBlockHashWithConsensusFields(zkBlock)
	} else {
		transactionHashes := make([][]byte, len(zkBlock.Transactions))
		for i, tx := range zkBlock.Transactions {
			transactionHashes[i] = tx.Hash.Bytes()
		}
		wantBlockHash = crypto.Keccak256Hash(bytes.Join(transactionHashes, []byte{}))
	}
	hashCheckSpan.SetAttributes(
		attribute.String("computed_hash", wantBlockHash.Hex()),
		attribute.String("provided_hash", zkBlock.BlockHash.Hex()),
		attribute.Bool("m2b", M2bHashEnabled),
	)

	if wantBlockHash != zkBlock.BlockHash {
		err := errors.New("zkBlock hash validation failed")
		hashCheckSpan.RecordError(err)
		hashCheckSpan.SetAttributes(attribute.String("status", "validation_failed"))
		hashCheckSpan.End()
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(traceCtx, "ZKBlock hash validation failed", err,
			ion.String("computed_hash", wantBlockHash.Hex()),
			ion.String("provided_hash", zkBlock.BlockHash.Hex()),
			ion.Bool("m2b", M2bHashEnabled),
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

	// Reject negative numeric fields before any DB work. Cheap, and it
	// prevents balance inversion at the RPC ingress boundary.
	if ok, err := CheckTransactionValues(tx); !ok {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "negative_value_rejected"))
		return false, err
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

	// Fail-closed: a DB error here must not be mistaken for "account not found".
	if err := security_cache.LoadAccounts(loggerCtx, nil, accountsSet); err != nil {
		span.RecordError(err)
		logger().Error(traceCtx, "Failed to load accounts into security cache", err,
			ion.String("function", "Security.AllChecks"))
		return false, fmt.Errorf("failed to load accounts for validation: %w", err)
	}

	// NOTE: submit-time receiver auto-registration is GONE. It created the
	// receiver on THIS node only (with a locally minted ART nonce) and relied on
	// DID propagation to reach the committee before the vote — the race behind
	// the receiver-not-found consensus failures. An unknown receiver now passes
	// validation via AllowNewReceiverAccounts (CheckAddressExistWithCache) and is
	// created at BLOCK APPLY on every node from the block-carried identity
	// stamped by the sequencer (DB_OPs.EnrichBlockAccountNonces) — one canonical
	// identity, no propagation dependency, no DB write for rejected txs.
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
	signerMu.RLock()
	localExpectedChainID := expectedChainID
	signerMu.RUnlock()
	if localExpectedChainID == nil {
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
	if tx.ChainID.Cmp(localExpectedChainID) != 0 {
		err := fmt.Errorf("chain ID mismatch: got %s (uint64: %d), expected %s (uint64: %d)",
			tx.ChainID.String(), tx.ChainID.Uint64(), localExpectedChainID.String(), localExpectedChainID.Uint64())
		chainIDSpan.RecordError(err)
		chainIDSpan.SetAttributes(
			attribute.String("status", "validation_failed"),
			attribute.String("tx_chain_id", tx.ChainID.String()),
			attribute.Int64("tx_chain_id_uint64", int64(tx.ChainID.Uint64())),
			attribute.String("expected_chain_id", localExpectedChainID.String()),
			attribute.Int64("expected_chain_id_uint64", int64(localExpectedChainID.Uint64())),
		)
		chainIDSpan.End()
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(spanCtx, "Chain ID mismatch", err,
			ion.String("tx_chain_id", tx.ChainID.String()),
			ion.Int64("tx_chain_id_uint64", int64(tx.ChainID.Uint64())),
			ion.String("expected_chain_id", localExpectedChainID.String()),
			ion.Int64("expected_chain_id_uint64", int64(localExpectedChainID.Uint64())),
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

	account := security_cache.GetAccount(*tx.From)
	if account == nil {
		err := errors.New("sender account not found in cache")
		nonceSpan.RecordError(err)
		nonceSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Failed to get account for nonce check", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}
	expectedNonce := account.TxNonce

	nonceSpan.SetAttributes(
		attribute.Int64("expected_nonce", int64(expectedNonce)),
		attribute.Int64("submitted_nonce", int64(tx.Nonce)),
	)

	// TODO(nonce-gap): currently accepts future nonces (tx.Nonce > expectedNonce).
	// If such a tx is committed the account jumps to tx.Nonce+1, permanently orphaning
	// all nonces in between. Evaluate whether to enforce tx.Nonce == expectedNonce
	// (strict sequential, standard EVM) or keep >= for queued-tx support.
	if tx.Nonce < expectedNonce {
		err := fmt.Errorf("submitted nonce %d is too low, expected >= %d", tx.Nonce, expectedNonce)
		nonceSpan.RecordError(err)
		nonceSpan.End()
		span.RecordError(err)
		logger().Error(spanCtx, "Nonce is too low or duplicate", err,
			ion.String("function", "Security.allChecksWithCache"))
		return false, err
	}

	// Update cache so subsequent transactions from same sender see incremented nonce
	security_cache.UpdateTxNonce(*tx.From, tx.Nonce+1)

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

	// Use tx.Type directly — already set by convertEthTxToConfigTx at ingest time.
	// Field-presence heuristics are fragile and redundant.
	var ethTx *types.Transaction
	switch tx.Type {
	case types.DynamicFeeTxType: // 2 — EIP-1559
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

	case types.AccessListTxType: // 1 — EIP-2930
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

	default: // 0 — Legacy
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

	v := tx.V.Uint64()
	var from common.Address
	var err error

	chainIDStr := "legacy/none"
	if tx.ChainID != nil {
		chainIDStr = tx.ChainID.String()
	}
	span.SetAttributes(
		attribute.Int64("v_value", int64(v)),
		attribute.String("chain_id", chainIDStr),
	)

	logger().Info(spanCtx, "Starting signature check",
		ion.Int64("v_value", int64(v)),
		ion.String("chain_id", chainIDStr),
		ion.String("function", "Security.CheckSignature"))

	// Signer selection — use cached singletons (built at SetExpectedChainID time).
	// Typed txns (type 1/2): V is just the recovery bit (0/1); EIP155Signer rejects them.
	// Legacy txns: V encodes chain ID (EIP-155) or is 27/28 (Homestead/pre-EIP-155).
	signerMu.RLock()
	localLatest := cachedLatestSigner
	localEIP155 := cachedEIP155Signer
	signerMu.RUnlock()

	switch tx.Type {
	case types.DynamicFeeTxType, types.AccessListTxType:
		signer := localLatest
		if signer == nil {
			signer = types.LatestSignerForChainID(tx.ChainID)
		}
		logger().Info(spanCtx, "Trying LatestSignerForChainID (typed transaction)",
			ion.Int64("tx_type", int64(tx.Type)),
			ion.String("function", "Security.CheckSignature"))
		from, err = types.Sender(signer, ethTx)
		if err == nil && from == *tx.From {
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.String("status", "success"), attribute.String("signer_type", "LatestSignerForChainID"), attribute.Float64("duration", duration))
			logger().Info(spanCtx, "Signature verified with LatestSignerForChainID",
				ion.Float64("duration", duration),
				ion.String("function", "Security.CheckSignature"))
			return true, nil
		}

	default: // legacy
		if v == 27 || v == 28 {
			logger().Info(spanCtx, "Trying HomesteadSigner (pre-EIP-155)",
				ion.String("function", "Security.CheckSignature"))
			from, err = types.Sender(cachedHomeSteadSigner, ethTx)
			if err == nil && from == *tx.From {
				duration := time.Since(startTime).Seconds()
				span.SetAttributes(attribute.String("status", "success"), attribute.String("signer_type", "HomesteadSigner"), attribute.Float64("duration", duration))
				logger().Info(spanCtx, "Signature verified with HomesteadSigner",
					ion.Float64("duration", duration),
					ion.String("function", "Security.CheckSignature"))
				return true, nil
			}
			// Fall through to EIP155 below
		}

		// EIP-155 encoded V (V = chainID*2 + 35/36), or Homestead fallback
		eip155 := localEIP155
		if eip155 == nil {
			eip155 = types.NewEIP155Signer(tx.ChainID)
		}
		logger().Info(spanCtx, "Trying EIP155Signer",
			ion.String("function", "Security.CheckSignature"))
		from, err = types.Sender(eip155, ethTx)
		if err == nil && from == *tx.From {
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.String("status", "success"), attribute.String("signer_type", "EIP155Signer"), attribute.Float64("duration", duration))
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
// CheckTransactionHash verifies that tx.Hash equals the hash recomputed from the
// transaction's CONTENTS (ethTx.Hash()). It is the exported entry point for the
// block-receive path (messaging.validateRemoteBlock), where tx.Hash is an
// untrusted wire field: canonical body binding hashes over tx.Hash, so an
// unverified tx.Hash would let a mismatched body reproduce a captured BlockHash
// and re-present a valid certificate.
// Returns (true,nil) only when the wire hash matches the content hash.
func CheckTransactionHash(tx *config.Transaction, traceCtx context.Context) (bool, error) {
	return checkTransactionHash(tx, traceCtx)
}

// CheckTransactionValues rejects transactions carrying negative numeric fields.
// Canonical Ethereum RLP cannot encode a negative big.Int, but JMDN's
// internal config.Transaction is a plain struct that a JSON ingress path or a
// wire message can populate directly with a negative *big.Int. If such a
// value reaches execution, the balance arithmetic inverts:
//
//	sender:   balance - (-v) == balance + v   (sender is CREDITED)
//	receiver: balance + (-v) == balance - v   (receiver is DEBITED)
//
// letting a sender debit an arbitrary receiver without that receiver's
// signature. Negative gas fields similarly invert fee deductions. This is the
// fail-closed value gate applied at every trust boundary (RPC ingress via
// AllChecks, and remote-block admission via validateRemoteBlock); parseTransaction
// enforces it again at execution as defense in depth.
//
// Returns (true,nil) only when every present numeric field is non-negative and
// (for present EIP-1559 fields) MaxPriorityFee <= MaxFee.
func CheckTransactionValues(tx *config.Transaction) (bool, error) {
	if tx == nil {
		return false, fmt.Errorf("nil transaction")
	}
	// Value: nil is treated as zero elsewhere (parseTransaction); a present value
	// must be non-negative.
	if tx.Value != nil && tx.Value.Sign() < 0 {
		return false, fmt.Errorf("negative transaction value: %s", tx.Value.String())
	}
	if tx.GasPrice != nil && tx.GasPrice.Sign() < 0 {
		return false, fmt.Errorf("negative gas_price: %s", tx.GasPrice.String())
	}
	if tx.MaxFee != nil && tx.MaxFee.Sign() < 0 {
		return false, fmt.Errorf("negative max_fee: %s", tx.MaxFee.String())
	}
	if tx.MaxPriorityFee != nil && tx.MaxPriorityFee.Sign() < 0 {
		return false, fmt.Errorf("negative max_priority_fee: %s", tx.MaxPriorityFee.String())
	}
	// EIP-1559 invariant: the priority (tip) fee may never exceed the max fee.
	if tx.MaxFee != nil && tx.MaxPriorityFee != nil && tx.MaxPriorityFee.Cmp(tx.MaxFee) > 0 {
		return false, fmt.Errorf("max_priority_fee %s exceeds max_fee %s",
			tx.MaxPriorityFee.String(), tx.MaxFee.String())
	}
	// Ingress gas bound (anti-DoS): reject a declared GasLimit above the block gas
	// limit. Without this an attacker sets a huge GasLimit and burns CPU fleet-wide
	// for a trivial fee. Fixed constant → fleet-uniform validity (see config.MaxTxGasLimit).
	if tx.GasLimit > config.MaxTxGasLimit {
		return false, fmt.Errorf("gas_limit %d exceeds max %d", tx.GasLimit, config.MaxTxGasLimit)
	}
	return true, nil
}

// ethTxFromConfig reconstructs the go-ethereum transaction from a
// config.Transaction using tx.Type (same construction as CheckSignature /
// checkTransactionHash), so its content hash can be recomputed.
func ethTxFromConfig(tx *config.Transaction) *types.Transaction {
	switch tx.Type {
	case types.DynamicFeeTxType: // 2 — EIP-1559
		return types.NewTx(&types.DynamicFeeTx{
			ChainID: tx.ChainID, Nonce: tx.Nonce, To: tx.To, Value: tx.Value,
			GasTipCap: tx.MaxPriorityFee, GasFeeCap: tx.MaxFee, Gas: tx.GasLimit,
			Data: tx.Data, AccessList: toGethAccessList(tx.AccessList),
			V: tx.V, R: tx.R, S: tx.S,
		})
	case types.AccessListTxType: // 1 — EIP-2930
		return types.NewTx(&types.AccessListTx{
			ChainID: tx.ChainID, Nonce: tx.Nonce, To: tx.To, Value: tx.Value,
			GasPrice: tx.GasPrice, Gas: tx.GasLimit, Data: tx.Data,
			AccessList: toGethAccessList(tx.AccessList), V: tx.V, R: tx.R, S: tx.S,
		})
	default: // 0 — Legacy
		return types.NewTx(&types.LegacyTx{
			Nonce: tx.Nonce, To: tx.To, Value: tx.Value, GasPrice: tx.GasPrice,
			Gas: tx.GasLimit, Data: tx.Data, V: tx.V, R: tx.R, S: tx.S,
		})
	}
}

// RecomputeBlockHashFromContents recomputes the block hash from transaction
// CONTENTS — Keccak256 over the concatenation of each transaction's content hash
// (ethTx.Hash()), matching the block generator's
// generateBlockHashFromTransactions. Unlike a recompute over the wire tx.Hash
// field, this binds the block hash to what the transactions actually ARE, so it
// cannot be fooled by a mismatched wire tx.Hash.
func RecomputeBlockHashFromContents(txs []config.Transaction) common.Hash {
	if len(txs) == 0 {
		return common.Hash{}
	}
	buf := make([]byte, 0, len(txs)*32)
	for i := range txs {
		h := ethTxFromConfig(&txs[i]).Hash()
		buf = append(buf, h.Bytes()...)
	}
	return common.BytesToHash(crypto.Keccak256(buf))
}

// M2bHashEnabled gates whether CheckBlockHash (and messaging.checkBodyBinding,
// which reads this same flag) validates against the M2b six-field hash
// (RecomputeBlockHashWithConsensusFields) instead of the legacy
// transactions-only hash. Defaults FALSE, same rollout pattern as
// messaging.CommitteeV2Enabled (JMDN_COMMITTEE_V2): with the flag off,
// behavior is byte-identical to before M2b existed.
//
// Do NOT flip this until the block generator (JMDT-Sequencer-Orchestrator)
// also computes BlockHash via RecomputeBlockHashWithConsensusFields. Flipping
// only the validator side makes every real block fail this check - the
// generator and validator must agree on the hash formula, which is exactly
// why this is a flag (a coordinated flip) and not an automatic cutover.
var M2bHashEnabled = envOn("JMDN_M2B_HASH", false)

// CheckBlockHash recomputes the block hash and compares it to block.BlockHash.
// Returns (true,nil) only on match. Call on the block receive path so a block
// cannot claim a BlockHash that does not correspond to what it actually
// carries.
//
// With M2bHashEnabled off (default), this covers transaction CONTENTS only
// (Keccak256 over each transaction's content hash), independent of whether
// the per-transaction tx.Hash fields were pre-verified - unchanged from
// before M2b existed.
//
// With M2bHashEnabled on, this covers the six AVC consensus fields
// (Slot/Period/RandaoReveals/VdfProof/SeedEpoch/VotingSnapshotEpoch) plus
// transaction contents, via RecomputeBlockHashWithConsensusFields - see that
// function's doc for the exact preimage.
func CheckBlockHash(block *config.ZKBlock) (bool, error) {
	if block == nil {
		return false, errors.New("block is nil")
	}
	var want common.Hash
	if M2bHashEnabled {
		want = RecomputeBlockHashWithConsensusFields(block)
	} else {
		want = RecomputeBlockHashFromContents(block.Transactions)
	}
	if block.BlockHash != want {
		return false, fmt.Errorf("block hash mismatch: recomputed %s, block claims %s",
			want.Hex(), block.BlockHash.Hex())
	}
	return true, nil
}

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

	// Construct the transaction using tx.Type directly (same as CheckSignature).
	var ethTx *types.Transaction

	switch tx.Type {
	case types.DynamicFeeTxType: // 2 — EIP-1559
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

	case types.AccessListTxType: // 1 — EIP-2930
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

	default: // 0 — Legacy
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
