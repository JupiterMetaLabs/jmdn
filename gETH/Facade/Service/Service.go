package Service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	block "gossipnode/Block"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	Utils "gossipnode/gETH/Facade/Service/utils"
	"math/big"
	"strings"
	"time"

	"gossipnode/SmartContract/pkg/client"
	scTracer "gossipnode/SmartContract/pkg/tracer"
	smartcontractpb "gossipnode/SmartContract/proto"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// ServiceImpl implements the Service interface
type ServiceImpl struct {
	ChainIDValue      int
	SmartContractPort int
	scClient          *client.Client
}

// NewService creates a new service implementation
func NewService(chainID int, smartRPC int) Service {
	scClient, err := client.NewClient(fmt.Sprintf("localhost:%d", smartRPC))
	if err != nil {
		logger().Error(context.Background(), "Failed to connect to SmartContract gRPC server", err)
	}
	return &ServiceImpl{
		ChainIDValue:      chainID,
		SmartContractPort: smartRPC,
		scClient:          scClient,
	}
}

func (s *ServiceImpl) GetChainIDValue() *big.Int {
	return big.NewInt(int64(s.ChainIDValue))
}

func (s *ServiceImpl) ChainID(ctx context.Context) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Log the operation
	if err := Logger.LogData(opCtx, "ChainID returned to the client", "ChainID", 1); err != nil {
		// Log error but don't fail the operation
		logger().Error(opCtx, "Failed to log ChainID operation", err)
	}

	return big.NewInt(int64(s.ChainIDValue)), nil
}

func (s *ServiceImpl) CompileSolidity(ctx context.Context, source string, optimize bool, runs uint32) (*SolcCompileResult, error) {
	resp, err := s.scClient.CompileContract(ctx, &smartcontractpb.CompileRequest{
		SourceCode:   source,
		Optimize:     optimize,
		OptimizeRuns: runs,
	})
	if err != nil {
		return nil, err
	}

	// If the compiler returned errors in the contract object, return them
	if resp.Contract != nil && len(resp.Contract.Errors) > 0 {
		return &SolcCompileResult{
			Errors: resp.Contract.Errors,
		}, nil
	}

	// Check if top-level error exists
	if resp.Error != "" {
		return &SolcCompileResult{
			Errors: []string{resp.Error},
		}, nil
	}

	if resp.Contract == nil {
		return nil, fmt.Errorf("compilation failed: no contract produced")
	}

	return &SolcCompileResult{
		ABI:              resp.Contract.Abi,
		Bytecode:         resp.Contract.Bytecode,
		DeployedBytecode: resp.Contract.DeployedBytecode,
		Errors:           resp.Contract.Errors,
		// Warnings would be added if available in proto
	}, nil
}

func (s *ServiceImpl) ClientVersion(ctx context.Context) (string, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ClientVersion := "JMDT/v1.0.0"

	// Log the operation
	if err := Logger.LogData(opCtx, "ClientVersion returned to the client", "ClientVersion", 1); err != nil {
		// Log error but don't fail the operation
		logger().Error(opCtx, "Failed to log ClientVersion operation", err)
	}

	return ClientVersion, nil
}

func (s *ServiceImpl) Accounts(ctx context.Context) ([]string, error) {
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	accounts, err := DB_OPs.ListAllAccounts(nil, 0)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("Accounts failed: %v", err), "Accounts", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log Accounts error", logErr)
		}
		return nil, err
	}

	addresses := make([]string, 0, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		addresses = append(addresses, account.Address.Hex())
	}

	if logErr := Logger.LogData(opCtx, fmt.Sprintf("Accounts returned to the client: %d", len(addresses)), "Accounts", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log Accounts success", logErr)
	}

	return addresses, nil
}

func (s *ServiceImpl) BlockNumber(ctx context.Context) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Pass the context to the database operation
	BlockNumber, err := DB_OPs.GetLatestBlockNumber(opCtx, nil)
	if err != nil {
		// Log error
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockNumber failed: %v", err), "BlockNumber", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log BlockNumber error", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockNumber returned to the client: %d", BlockNumber), "BlockNumber", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log BlockNumber success", logErr)
	}

	return big.NewInt(int64(BlockNumber)), nil
}

func (s *ServiceImpl) GetTransactionCount(ctx context.Context, addr string, block string) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Normalize the address (handles mixed-case / checksum addresses)
	convertedAddr := Utils.ConvertAddressCaseInsensitive(addr)

	// TxNonce on the Account record is the authoritative Ethereum nonce.
	// It is maintained by block processing as: account.TxNonce = tx.Nonce + 1
	// CountTransactionsByAccount is NOT used here because it counts both FROM and TO
	// transactions, which would inflate the nonce for recipient addresses.
	account, err := DB_OPs.GetAccount(nil, convertedAddr)
	if err != nil {
		if DB_OPs.IsNotFound(err) {
			// Address has no transactions yet — nonce is 0. IsNotFound also matches
			// the SQL-backed "no rows in result set" shape a never-seen address now
			// returns (the old "not found"/"does not exist" check missed it).
			return big.NewInt(0), nil
		}
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("GetTransactionCount failed: %v", err), "GetTransactionCount", -1); logErr != nil {
			fmt.Printf("Failed to log GetTransactionCount error: %v\n", logErr)
		}
		return nil, err
	}

	if logErr := Logger.LogData(opCtx, fmt.Sprintf("GetTransactionCount returned nonce %d for %s", account.TxNonce, addr), "GetTransactionCount", 1); logErr != nil {
		fmt.Printf("Failed to log GetTransactionCount: %v\n", logErr)
	}

	return new(big.Int).SetUint64(account.TxNonce), nil
}

func (s *ServiceImpl) BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Types.Block, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	ZKBlock, err := DB_OPs.ReadZKBlockByNumber(opCtx, nil, num.Uint64())
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber failed: %v", err), "BlockByNumber", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log BlockByNumber error", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber returned to the client: %d", ZKBlock.BlockNumber), "BlockByNumber", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log BlockByNumber success", logErr)
	}

	// Convert the ZKBlock from GetZKBlockByNumber to Block
	block := Utils.ConvertZKBlockToBlock(ZKBlock)
	if block == nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber failed: %v", err), "BlockByNumber", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log BlockByNumber error", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber returned to the client: %d", ZKBlock.BlockNumber), "BlockByNumber", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log BlockByNumber success", logErr)
	}

	return block, nil
}

// BlockByHash resolves a block by its 0x-prefixed hash (eth_getBlockByHash).
// Mirrors BlockByNumber but keyed on hash. Required by wallets (MetaMask fetches
// the block by receipt.blockHash to finalize a mined tx — without this the tx can
// stay perpetually "pending" despite a valid receipt).
func (s *ServiceImpl) BlockByHash(ctx context.Context, hash string, fullTx bool) (*Types.Block, error) {
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	ZKBlock, err := DB_OPs.GetZKBlockByHash(nil, hash)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByHash failed: %v", err), "BlockByHash", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log BlockByHash error", logErr)
		}
		return nil, err
	}

	block := Utils.ConvertZKBlockToBlock(ZKBlock)
	if block == nil {
		return nil, fmt.Errorf("BlockByHash: failed to convert block %s", hash)
	}

	if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByHash returned to the client: %d", ZKBlock.BlockNumber), "BlockByHash", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log BlockByHash success", logErr)
	}
	return block, nil
}

// LatestL1CommitBlock returns the most recent block that has L1 commit data.
//
// Fast path (O(1)): Block/Server.go maintains an atomic cache of the latest
// committed block number, updated on every /api/l1-commit* call. If the cache
// is warm we do a single point-lookup.
//
// Cold path (first call after restart before any commit arrives): scans
// backwards up to 10 000 blocks — exits immediately on the first hit.
func (s *ServiceImpl) LatestL1CommitBlock(ctx context.Context) (*Types.Block, error) {
	const maxScan = 10_000

	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Fast path — cache is warm.
	if cached := block.GetLatestL1CommitBlockNum(); cached > 0 {
		zkBlock, err := DB_OPs.ReadZKBlockByNumber(opCtx, nil, cached)
		if err == nil && zkBlock != nil && zkBlock.L1TxHash != "" {
			return Utils.ConvertZKBlockToBlock(zkBlock), nil
		}
		// Cache pointed at a block without L1 data (shouldn't happen, but fall through).
	}

	// Cold path — scan backwards from chain tip.
	latest, err := DB_OPs.GetLatestBlockNumber(opCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("LatestL1CommitBlock: get latest block: %w", err)
	}

	start := int64(latest)
	end := start - maxScan
	if end < 0 {
		end = 0
	}

	for n := start; n >= end; n-- {
		zkBlock, err := DB_OPs.ReadZKBlockByNumber(opCtx, nil, uint64(n))
		if err != nil || zkBlock == nil {
			continue
		}
		if zkBlock.L1TxHash != "" {
			return Utils.ConvertZKBlockToBlock(zkBlock), nil
		}
	}

	return nil, nil // no committed block found in window
}

// Need to add more functionality to this
func (s *ServiceImpl) Balance(ctx context.Context, addr string, block *big.Int, network string) (*big.Int, error) {

	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	convertedAddr := Utils.ConvertAddressCaseInsensitive(addr)

	// Historical balance: when a concrete past block is requested, reconstruct
	// via reverse-delta replay (DB_OPs.GetBalanceAtBlock). "latest"/"pending"
	// resolve to the chain tip in parseBlockTag and fall through to the fast path.
	if block != nil && block.IsUint64() {
		if tip, tipErr := DB_OPs.GetLatestBlockNumber(opCtx, nil); tipErr == nil {
			requested := block.Uint64()
			if requested > tip {
				return nil, fmt.Errorf("block %d not found (tip %d)", requested, tip)
			}
			if requested < tip {
				bal, histErr := DB_OPs.GetBalanceAtBlock(convertedAddr, requested)
				if histErr != nil {
					logger().Error(opCtx, "Historical balance reconstruction failed", histErr,
						ion.String("address", convertedAddr.Hex()),
						ion.Uint64("block", requested))
					return nil, histErr
				}
				return bal, nil
			}
		}
	}

	logger().Debug(opCtx, "Address conversion", ion.String("original", addr), ion.String("converted", convertedAddr.Hex()))
	AccountDetails, err := DB_OPs.GetAccount(nil, convertedAddr)
	if err != nil {
		// A missing account is NOT an error (normal Ethereum semantics — a
		// never-funded address, e.g. a not-yet-credited reward address, has
		// balance 0). Only genuine read errors are logged at ERROR below.
		if DB_OPs.IsNotFound(err) {
			// IsNotFound also matches the SQL-backed "no rows in result set" shape a
			// never-seen address now returns (the old "not found"/"does not exist"
			// check missed it, erroring instead of returning 0).
			// Ordinary Ethereum semantics: an unknown address simply has balance 0.
			// REGISTER-ON-READ IS GONE — eth_getBalance used to auto-create and
			// propagate the account here, which brought accounts into existence on
			// SOME nodes only (with a locally minted ART nonce) and was the trigger
			// for the receiver-not-found consensus failures. Accounts are now
			// created exclusively at block apply, from the block-carried identity
			// stamped by the sequencer (DB_OPs.EnrichBlockAccountNonces), so a read
			// must never write state.
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance returned zero for non-existent address: %s", addr), "Balance", 1); logErr != nil {
				fmt.Printf("Failed to log Balance success: %v\n", logErr)
			}
			return big.NewInt(0), nil
		}

		// Genuine error (not a missing account): log at ERROR and return.
		logger().Error(opCtx, "GetAccount error", err)
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance failed: %v", err), "Balance", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log Balance error", logErr)
		}
		return nil, err
	}

	// Debug: Print account details
	logger().Debug(opCtx, "Account found", ion.String("balance", AccountDetails.Balance), ion.String("address", AccountDetails.Address.Hex()), ion.String("did", AccountDetails.DIDAddress))

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance returned to the client: %s", AccountDetails.Balance), "Balance", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log Balance success", logErr)
	}

	// Convert the balance from string to big.Int
	balance, err := Utils.ConvertBalance(AccountDetails.Balance)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance failed: %v", err), "Balance", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log Balance error", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance returned to the client: %s", AccountDetails.Balance), "Balance", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log Balance success", logErr)
	}

	return balance, nil
}

func (s *ServiceImpl) SendRawTx(ctx context.Context, rawHex string) (string, error) {
	// Debugging
	// fmt.Println(">>>>>> SendRawTx received: ", rawHex)
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Remove 0x prefix if present
	rawHex = strings.TrimPrefix(rawHex, "0x")

	// Decode hex string to bytes
	rawBytes, err := hex.DecodeString(rawHex)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx failed to decode hex: %v", err), "SendRawTx", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log SendRawTx hex decode error", logErr)
		}
		return "", fmt.Errorf("failed to decode hex string: %w", err)
	}

	// Try to parse as JSON first (for test compatibility)
	var tx config.Transaction
	err = json.Unmarshal(rawBytes, &tx)
	if err != nil {
		// If JSON parsing fails, try to parse as RLP-encoded transaction
		logger().Debug(opCtx, "JSON parsing failed, trying RLP parsing")

		// Parse transaction — UnmarshalBinary handles all types:
		// legacy (no prefix), EIP-2930 (0x01), EIP-1559 (0x02).
		// rlp.DecodeBytes cannot handle typed transactions (0x01/0x02 prefix).
		var ethTx ethtypes.Transaction
		err = ethTx.UnmarshalBinary(rawBytes)
		if err != nil {
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx failed to parse RLP transaction: %v", err), "SendRawTx", -1); logErr != nil {
				logger().Error(opCtx, "Failed to log SendRawTx RLP parse error", logErr)
			}
			return "", fmt.Errorf("failed to parse RLP transaction: %w", err)
		}

		// Convert Ethereum transaction to our config.Transaction format
		tx = convertEthTxToConfigTx(&ethTx)
		logger().Debug(opCtx, "Converted RLP transaction")
	} else {
		logger().Debug(opCtx, "JSON transaction parsed")
	}

	// Public eth JSON-RPC surface: never eligible for the unsigned-deployment
	// bypass (audit SEC-02). A legitimately signed tx still passes AllChecks.
	hash, err := block.SubmitRawTransaction(context.Background(), &tx, block.OriginUntrusted("grpc"))
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx failed: %v", err), "SendRawTx", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log SendRawTx error", logErr)
		}
		// Debugging
		logger().Error(opCtx, "SubmitRawTransaction failed", err)
		return "", err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx returned to the client: %s", hash), "SendRawTx", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log SendRawTx success", logErr)
	}
	// Debugging
	logger().Info(opCtx, "SubmitRawTransaction success", ion.String("hash", hash))

	return hash, nil
}

// convertEthTxToConfigTx converts an Ethereum transaction to our config.Transaction format
func convertEthTxToConfigTx(ethTx *ethtypes.Transaction) config.Transaction {
	// Get the sender address
	from, _ := ethtypes.Sender(ethtypes.LatestSignerForChainID(ethTx.ChainId()), ethTx)

	// Convert to our transaction format
	tx := config.Transaction{
		Hash:      ethTx.Hash(),
		From:      &from,
		To:        ethTx.To(),
		Value:     ethTx.Value(),
		Type:      uint8(ethTx.Type()),
		Timestamp: uint64(time.Now().UTC().Unix()),
		ChainID:   ethTx.ChainId(),
		Nonce:     ethTx.Nonce(),
		GasLimit:  ethTx.Gas(),
		Data:      ethTx.Data(),
	}

	// Set gas price based on transaction type
	if ethTx.Type() == ethtypes.LegacyTxType {
		tx.GasPrice = ethTx.GasPrice()
	} else if ethTx.Type() == ethtypes.AccessListTxType {
		tx.GasPrice = ethTx.GasPrice()
	} else if ethTx.Type() == ethtypes.DynamicFeeTxType {
		tx.MaxFee = ethTx.GasFeeCap()
		tx.MaxPriorityFee = ethTx.GasTipCap()
	}

	// Set signature components
	v, r, s := ethTx.RawSignatureValues()
	tx.V = v
	tx.R = r
	tx.S = s

	// Debugging
	logger().Debug(context.Background(), "Transaction details", ion.String("hash", tx.Hash.Hex()))
	logger().Debug(context.Background(), "Transaction sender", ion.String("from", tx.From.Hex()))
	if tx.To != nil {
		logger().Debug(context.Background(), "Transaction recipient", ion.String("to", tx.To.Hex()))
	} else {
		logger().Debug(context.Background(), "Transaction recipient", ion.String("to", "nil (contract creation)"))
	}
	if tx.Value != nil {
		logger().Debug(context.Background(), "Transaction value", ion.String("value", tx.Value.String()))
	}
	logger().Debug(context.Background(), "Transaction type", ion.Int("type", int(tx.Type)))
	logger().Debug(context.Background(), "Transaction timestamp", ion.Int("timestamp", int(tx.Timestamp)))
	if tx.ChainID != nil {
		logger().Debug(context.Background(), "Chain ID", ion.String("chain_id", tx.ChainID.String()))
	}
	logger().Debug(context.Background(), "Transaction nonce", ion.Int("nonce", int(tx.Nonce)))
	logger().Debug(context.Background(), "Gas limit", ion.Int("gas_limit", int(tx.GasLimit)))
	if tx.GasPrice != nil {
		logger().Debug(context.Background(), "Gas price", ion.String("gas_price", tx.GasPrice.String()))
	}
	if tx.MaxFee != nil {
		logger().Debug(context.Background(), "Max fee", ion.String("max_fee", tx.MaxFee.String()))
	}
	if tx.MaxPriorityFee != nil {
		logger().Debug(context.Background(), "Max priority fee", ion.String("max_priority_fee", tx.MaxPriorityFee.String()))
	}
	logger().Debug(context.Background(), "Transaction data length", ion.Int("data_len", len(tx.Data)))
	logger().Debug(context.Background(), "Access list present")
	if tx.V != nil {
		logger().Debug(context.Background(), "Transaction V", ion.String("v", tx.V.String()))
	}
	if tx.R != nil {
		logger().Debug(context.Background(), "Transaction R", ion.String("r", tx.R.String()))
	}
	if tx.S != nil {
		logger().Debug(context.Background(), "Transaction S", ion.String("s", tx.S.String()))
	}

	return tx
}

func (s *ServiceImpl) TxByHash(ctx context.Context, hash string) (*Types.Tx, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Normalize hash - ensure it has 0x prefix (keys are stored with 0x prefix)
	normalizedHash := hash
	if !strings.HasPrefix(strings.ToLower(hash), "0x") {
		normalizedHash = "0x" + hash
	}

	// Get the block containing this transaction
	block, err := DB_OPs.GetTransactionBlock(opCtx, nil, normalizedHash)
	if err != nil {
		// Chain-store miss. With pending lookup enabled, the transaction may
		// still be queued in the mempool, in which case it is returned with nil
		// block fields (the standard Ethereum pending representation) — and a
		// hash we know nothing about returns (nil, nil) so the RPC layer answers
		// null, which is what eth_getTransactionByHash is specified to do for an
		// unknown hash.
		//
		// This branch is inert unless tx_status.pending_tx_by_hash is enabled:
		// PendingTxByHash returns (nil, nil) when the feature is off, so the
		// original error is returned exactly as before.
		if pending, perr := s.PendingTxByHash(opCtx, normalizedHash); perr == nil && pending != nil {
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash served pending tx from mempool: %s", hash), "TxByHash", 1); logErr != nil {
				logger().Error(opCtx, "Failed to log TxByHash pending result", logErr)
			}
			return pending, nil
		} else if pendingTxByHashEnabled() {
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash: hash not in a block and not queued (returning null): %s", hash), "TxByHash", -1); logErr != nil {
				logger().Error(opCtx, "Failed to log TxByHash miss", logErr)
			}
			return nil, nil
		}

		if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash failed to get block: %v", err), "TxByHash", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log TxByHash error", logErr)
		}
		return nil, err
	}

	// Get the transaction
	ZKTx, err := DB_OPs.GetTransactionByHash(nil, normalizedHash)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash failed: %v", err), "TxByHash", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log TxByHash error", logErr)
		}
		return nil, err
	}

	// Convert the ZKTx from GetTransactionByHash to Tx
	tx := Utils.ConvertTrabsactionToTx(ZKTx)
	if tx == nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash failed: %v", err), "TxByHash", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log TxByHash error", logErr)
		}
		return nil, err
	}

	// Find the transaction index in the block
	var txIndex *uint64
	for i := range block.Transactions {
		TempBlockHash := block.Transactions[i].Hash.Hex() // Hex() returns with 0x prefix
		if TempBlockHash == normalizedHash {
			idx := uint64(i)
			txIndex = &idx
			break
		}
	}

	// Populate block information
	blockNumber := block.BlockNumber
	tx.BlockNumber = &blockNumber
	tx.BlockHash = block.BlockHash.Bytes()
	tx.TransactionIndex = txIndex

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash returned to the client: %s", hash), "TxByHash", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log TxByHash success", logErr)
	}

	return tx, nil
}

func (s *ServiceImpl) ReceiptByHash(ctx context.Context, hash string) (map[string]any, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get the receipt from the database
	receipt, err := DB_OPs.GetReceiptByHash(nil, hash)
	if err != nil {
		// Check if error is "transaction not found"
		// Per EIP-1474: eth_getTransactionReceipt MUST return result:null (not a JSON-RPC error)
		// when the tx is not yet mined. An error response causes MetaMask to stop polling
		// and permanently show the tx as "submitted".
		if err.Error() == "transaction not found" {
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash: tx not yet mined (returning null): %s", hash), "ReceiptByHash", -1); logErr != nil {
				logger().Error(opCtx, "Failed to log ReceiptByHash error", logErr)
			}
			return nil, nil
		}
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash failed: %v", err), "ReceiptByHash", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log ReceiptByHash error", logErr)
		}
		return nil, err
	}

	// If receipt is nil and no error, it means tx_processing was -1
	// Return nil to indicate result should be null in JSON-RPC response
	if receipt == nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash: tx_processing=-1 for %s, returning null", hash), "ReceiptByHash", 1); logErr != nil {
			logger().Error(opCtx, "Failed to log ReceiptByHash", logErr)
		}
		return nil, nil
	}

	// Get the transaction to extract from and to addresses
	tx, txErr := DB_OPs.GetTransactionByHash(nil, hash)
	if txErr != nil {
		// Log but don't fail - we can still return receipt without from/to
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash: failed to get transaction for from/to: %v", txErr), "ReceiptByHash", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log", logErr)
		}
	}

	// Convert logs to JSON-RPC format
	logs := make([]map[string]any, len(receipt.Logs))
	for i, log := range receipt.Logs {
		topics := make([]string, len(log.Topics))
		for j, topic := range log.Topics {
			topics[j] = topic.Hex() // Already has 0x prefix
		}

		logs[i] = map[string]any{
			"address":          log.Address.Hex(), // Already has 0x prefix
			"topics":           topics,
			"data":             "0x" + fmt.Sprintf("%x", log.Data),
			"blockNumber":      "0x" + fmt.Sprintf("%x", log.BlockNumber),
			"transactionHash":  log.TxHash.Hex(), // Already has 0x prefix
			"transactionIndex": "0x" + fmt.Sprintf("%x", log.TxIndex),
			"blockHash":        log.BlockHash.Hex(), // Already has 0x prefix
			"logIndex":         "0x" + fmt.Sprintf("%x", log.LogIndex),
			"removed":          log.Removed,
		}
	}

	// Convert the receipt to a map for JSON serialization in JSON-RPC format
	receiptMap := map[string]any{
		"transactionHash":   receipt.TxHash.Hex(), // Already has 0x prefix
		"transactionIndex":  "0x" + fmt.Sprintf("%x", receipt.TransactionIndex),
		"blockHash":         receipt.BlockHash.Hex(), // Already has 0x prefix
		"blockNumber":       "0x" + fmt.Sprintf("%x", receipt.BlockNumber),
		"cumulativeGasUsed": "0x" + fmt.Sprintf("%x", receipt.CumulativeGasUsed),
		"gasUsed":           "0x" + fmt.Sprintf("%x", receipt.GasUsed),
		"contractAddress":   nil,
		"logs":              logs,
		"logsBloom":         "0x" + fmt.Sprintf("%x", receipt.LogsBloom),
		"status":            "0x" + fmt.Sprintf("%x", receipt.Status),
	}

	// Add transaction type (from receipt or transaction, default to "0x0")
	txType := receipt.Type
	if txType == 0 && tx != nil {
		txType = uint8(tx.Type)
	}
	receiptMap["type"] = "0x" + fmt.Sprintf("%x", txType)

	// Add effectiveGasPrice from transaction.
	// EIP-1559: effectiveGasPrice = min(maxFeePerGas, baseFee + maxPriorityFeePerGas)
	// baseFee is the network constant (35 gwei) used across all RPC responses.
	{
		var effectiveGasPrice *big.Int
		if tx != nil && tx.GasPrice != nil {
			// Legacy (type 0) and EIP-2930 (type 1)
			effectiveGasPrice = tx.GasPrice
		} else if tx != nil && tx.MaxFee != nil {
			tip := tx.MaxPriorityFee
			if tip == nil {
				tip = big.NewInt(0)
			}
			basePlusTip := new(big.Int).Add(big.NewInt(config.BaseFeeWei), tip)
			if tx.MaxFee.Cmp(basePlusTip) < 0 {
				effectiveGasPrice = new(big.Int).Set(tx.MaxFee)
			} else {
				effectiveGasPrice = basePlusTip
			}
		} else {
			// Fallback: always emit effectiveGasPrice (EIP-1559 requires it)
			effectiveGasPrice = big.NewInt(config.BaseFeeWei)
		}
		receiptMap["effectiveGasPrice"] = "0x" + effectiveGasPrice.Text(16)
	}

	// Add from and to addresses from transaction
	if tx != nil {
		if tx.From != nil {
			receiptMap["from"] = tx.From.Hex() // Already has 0x prefix
		}
		if tx.To != nil {
			receiptMap["to"] = tx.To.Hex() // Already has 0x prefix
		} else {
			receiptMap["to"] = nil
		}
	}

	// Add contract address if present
	if receipt.ContractAddress != nil {
		receiptMap["contractAddress"] = receipt.ContractAddress.Hex() // Already has 0x prefix
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash returned to the client: %s", hash), "ReceiptByHash", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log ReceiptByHash success", logErr)
	}

	return receiptMap, nil
}

func (s *ServiceImpl) GetLogs(ctx context.Context, q Types.FilterQuery) ([]Types.Log, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get the logs from the database
	logs, err := DB_OPs.GetLogs(nil, q)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("GetLogs failed: %v", err), "GetLogs", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log GetLogs error", logErr)
		}
		return nil, err
	}

	return logs, nil
}

// Call implements the Service interface - calls smart contract via gRPC
func (s *ServiceImpl) Call(ctx context.Context, msg Types.CallMsg, block *big.Int) ([]byte, error) {
	if s.scClient == nil {
		return nil, fmt.Errorf("SmartContract client not initialized")
	}

	caller := common.FromHex(msg.From)
	contractAddr := common.FromHex(msg.To)

	resp, err := s.scClient.CallContract(ctx, caller, contractAddr, msg.Data)
	if err != nil {
		return nil, fmt.Errorf("smart contract call failed: %v", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("smart contract execution error: %s", resp.Error)
	}

	return common.FromHex(resp.ReturnData), nil
}

// EstimateGas UNITS!! implements the Service interface - estimates gas needed for a transaction
func (s *ServiceImpl) EstimateGas(ctx context.Context, msg Types.CallMsg) (uint64, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	const blockGasLimit = uint64(30_000_000)

	// Intrinsic base: 21000 + contract-creation (32000) + EIP-2028 calldata cost.
	// The EVM's execution gas does NOT include this, so it is always added.
	intrinsic := uint64(21000)
	if msg.To == "" {
		intrinsic += 32000
	}
	for _, b := range msg.Data {
		if b == 0 {
			intrinsic += 4
		} else {
			intrinsic += 16
		}
	}

	isCreate := msg.To == ""
	isContractCall := false
	if !isCreate {
		if code, cerr := s.GetCode(opCtx, msg.To, nil); cerr == nil && code != "" && code != "0x" {
			isContractCall = true
		}
	}

	// Plain value transfer (no code at To) — no EVM execution; intrinsic only.
	// This is the old behaviour and is exact (~21000 for a bare transfer).
	if !isCreate && !isContractCall {
		est := intrinsic + intrinsic*5/100
		if est > blockGasLimit {
			est = blockGasLimit
		}
		_ = Logger.LogData(opCtx, fmt.Sprintf("EstimateGas (plain transfer) returned: %d", est), "EstimateGas", 1)
		return est, nil
	}

	// Contract call/creation: run it through the EVM (side-effect-free, the same
	// read-only path eth_call uses) to get REAL execution gas — the old code
	// returned intrinsic-only, so MetaMask under-funded contract calls and they
	// execute-failed.
	if s.scClient == nil {
		return 0, fmt.Errorf("SmartContract client not initialized")
	}
	caller := common.FromHex(msg.From)
	var contractAddr []byte
	if !isCreate {
		contractAddr = common.FromHex(msg.To)
	}
	resp, err := s.scClient.EstimateGas(opCtx, caller, contractAddr, msg.Data)
	if err != nil {
		return 0, fmt.Errorf("gas estimation failed: %w", err)
	}
	if resp.Error != "" {
		// EVM reverted during estimation — surface it (standard eth_estimateGas
		// behaviour) so MetaMask shows the failure instead of sending a tx that
		// reverts on-chain and burns the fee.
		return 0, fmt.Errorf("gas estimation reverted: %s", resp.Error)
	}

	// Total = intrinsic + executor estimate (execution gas + the router's 20%),
	// then ~30% headroom, capped at the block gas limit.
	total := intrinsic + resp.GasEstimate
	total += total * 30 / 100
	if total > blockGasLimit {
		total = blockGasLimit
	}
	_ = Logger.LogData(opCtx, fmt.Sprintf("EstimateGas (evm) intrinsic=%d exec=%d returned: %d", intrinsic, resp.GasEstimate, total), "EstimateGas", 1)
	return total, nil
}

// GasPrice implements the Service interface - gets gas price from routing service
func (s *ServiceImpl) GasPrice(ctx context.Context) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get fee statistics directly from routing service
	feeStats, err := block.GetFeeStatisticsFromRouting()
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("GasPrice failed to get fee statistics: %v", err), "GasPrice", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log GasPrice error", logErr)
		}
		// Return fallback value on error (use 35 gwei minimum)
		return big.NewInt(config.BaseFeeWei), nil
	}

	// Get standard recommended fee (wei)
	gasPrice := big.NewInt(int64(feeStats.RecommendedFees.Standard))

	// Enforce minimum gas price: use BaseFeeWei (35 gwei) as the floor.
	twentyGwei := big.NewInt(20_000_000_000)
	if gasPrice.Sign() <= 0 || gasPrice.Cmp(twentyGwei) < 0 {
		gasPrice = big.NewInt(config.BaseFeeWei)
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("GasPrice returned to client: %s", gasPrice.String()), "GasPrice", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log GasPrice success", logErr)
	}

	return gasPrice, nil
}

// GetCode implements the Service interface - retrieves contract code at a specific address and block
func (s *ServiceImpl) GetCode(ctx context.Context, addr string, block *big.Int) (string, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Log the operation
	if err := Logger.LogData(opCtx, fmt.Sprintf("GetCode called for address: %s, block: %s", addr, block.String()), "GetCode", 1); err != nil {
		logger().Error(opCtx, "Failed to log GetCode operation", err)
	}

	if s.scClient == nil {
		return "0x", fmt.Errorf("SmartContract client not initialized")
	}

	contractAddr := common.FromHex(addr)
	resp, err := s.scClient.GetContractCode(opCtx, contractAddr)
	if err != nil {
		// Just return 0x for now if it fails
		return "0x", nil
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("GetCode returned for address: %s", addr), "GetCode", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log GetCode success", logErr)
	}

	if resp.Code == "" {
		return "0x", nil
	}

	return resp.Code, nil
}

// FeeHistory implements the Service interface - retrieves fee history for the last N blocks
func (s *ServiceImpl) FeeHistory(ctx context.Context, blockCount uint64, newest *big.Int, perc []float64) (map[string]any, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Determine the newest block number
	var newestNum *big.Int
	if newest != nil {
		newestNum = newest
	} else {
		// Get latest block if newest not specified
		latest, err := s.BlockNumber(ctx)
		if err != nil {
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("FeeHistory failed to get latest block: %v", err), "FeeHistory", -1); logErr != nil {
				logger().Error(opCtx, "Failed to log FeeHistory error", logErr)
			}
			return nil, err
		}
		newestNum = latest
	}

	// Calculate oldest block number
	// We need blockCount + 1 blocks (newest block + blockCount blocks before it)
	oldestNum := new(big.Int).Sub(newestNum, big.NewInt(int64(blockCount)))
	if oldestNum.Sign() < 0 {
		oldestNum = big.NewInt(0)
	}

	// JMDN does not implement variable base fees — return the same 35 gwei constant
	// used by eth_getBlockByNumber. No DB reads needed.
	baseFeeConstant := "0x" + big.NewInt(config.BaseFeeWei).Text(16)

	count := newestNum.Uint64() - oldestNum.Uint64() + 1
	baseFeePerGas := make([]string, count)
	gasUsedRatio := make([]float64, count)
	for i := range baseFeePerGas {
		baseFeePerGas[i] = baseFeeConstant
		gasUsedRatio[i] = 0.5 // neutral placeholder
	}

	var rewards [][]string
	if len(perc) > 0 {
		rewards = make([][]string, count)
		for i := range rewards {
			row := make([]string, len(perc))
			for j := range row {
				row[j] = "0x0"
			}
			rewards[i] = row
		}
	}

	// Build result map
	result := map[string]any{
		"oldestBlock":   fmt.Sprintf("0x%x", oldestNum.Uint64()),
		"baseFeePerGas": baseFeePerGas,
		"gasUsedRatio":  gasUsedRatio,
	}

	// Add rewards if provided
	if len(perc) > 0 && len(rewards) > 0 {
		result["reward"] = rewards
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("FeeHistory returned for blockCount: %d, newest: %s", blockCount, newestNum.String()), "FeeHistory", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log FeeHistory success", logErr)
	}

	return result, nil
}

func (s *ServiceImpl) GetStorageAt(ctx context.Context, address string, slot string, blockNum string) (string, error) {
	if s.scClient == nil {
		return "0x0000000000000000000000000000000000000000000000000000000000000000", nil
	}
	resp, err := s.scClient.GetStorage(ctx, common.HexToAddress(address).Bytes(), common.HexToHash(slot).Bytes())
	if err != nil {
		return "0x0000000000000000000000000000000000000000000000000000000000000000", nil
	}
	return resp.Value, nil
}

func (s *ServiceImpl) GetGasPrice(ctx context.Context) (string, error) {
	return hexutil.EncodeBig(config.DefaultGasPrice), nil
}

func (s *ServiceImpl) GetFeeHistory(ctx context.Context, blockCount int, newestBlock string, rewardPercentiles []float64) (interface{}, error) {
	history, err := s.FeeHistory(ctx, uint64(blockCount), nil, rewardPercentiles)
	if err != nil || len(history) == 0 {
		return map[string]interface{}{
			"oldestBlock":   "0x0",
			"baseFeePerGas": []string{hexutil.EncodeBig(config.DefaultGasPrice)},
			"gasUsedRatio":  []float64{0.0},
			"reward":        [][]string{},
		}, nil
	}
	return history, nil
}

func (s *ServiceImpl) GetMaxPriorityFeePerGas(ctx context.Context) (string, error) {
	return hexutil.EncodeBig(config.DefaultPriorityFeePerGas), nil
}

func (s *ServiceImpl) IsListening(ctx context.Context) (bool, error) {
	return true, nil
}

func (s *ServiceImpl) GetPeerCount(ctx context.Context) (string, error) {
	return "0x1", nil
}

// TraceTransaction implements debug_traceTransaction.
//
// KNOWN LIMITATION (Phase 5): This implementation re-executes the call
// against the CURRENT StateDB, not a historical snapshot of the pre-execution
// state.  For read-only / view calls the gas usage and return value are
// accurate.  For state-mutating calls the opcode trace may differ from the
// original execution if storage has changed since the transaction landed.
//
// Full historical tracing (fetching the Pebble snapshot at the parent block's
// stateRoot) is deferred to Phase 5.  Until then, Foundry users should pass
// --no-storage-caching to forge script/test when replay accuracy is required.
func (s *ServiceImpl) TraceTransaction(ctx context.Context, txHash string) (json.RawMessage, error) {
	_, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Normalise hash
	if !strings.HasPrefix(strings.ToLower(txHash), "0x") {
		txHash = "0x" + txHash
	}

	// Fetch the original transaction from ThebeDB
	zkTx, err := DB_OPs.GetTransactionByHash(nil, txHash)
	if err != nil {
		return nil, fmt.Errorf("TraceTransaction: tx not found: %w", err)
	}
	if zkTx == nil {
		return nil, fmt.Errorf("TraceTransaction: tx not found")
	}

	// Derive call parameters
	var from common.Address
	if zkTx.From != nil {
		from = *zkTx.From
	}

	var to *common.Address
	if zkTx.To != nil {
		addr := *zkTx.To
		to = &addr
	}

	value := zkTx.Value
	if value == nil {
		value = new(big.Int)
	}

	gasLimit := zkTx.GasLimit
	if gasLimit == 0 {
		gasLimit = 3_000_000 // sensible default
	}

	// Initialise a best-effort current StateDB
	// NOTE: This uses the live state, not the historical pre-tx snapshot.
	traceResult, err := scTracer.TraceTransaction(
		from,
		to,
		zkTx.Data,
		value,
		gasLimit,
		s.ChainIDValue,
	)
	if err != nil {
		return nil, err
	}

	return traceResult, nil
}

func (s *ServiceImpl) TxPoolContent(ctx context.Context) (map[string]any, error) {
	routingClient, err := block.GetRoutingClient()
	if err != nil {
		return nil, fmt.Errorf("txpool_content: routing client unavailable: %w", err)
	}

	// Cap at 5000. MRE rejects limit <= 0, and pre-allocates make([]*Tx, 0, limit),
	// so MaxInt32 would be a memory bomb. True "unbounded" semantics would need an
	// MRE-side change to treat limit=0 as capped-prealloc; use this constant until then.
	batch, err := routingClient.PeekPendingTransactions(ctx, 5000)
	if err != nil {
		return nil, fmt.Errorf("txpool_content: %w", err)
	}

	// Group by sender → nonce → tx object (standard txpool_content shape).
	pending := make(map[string]map[string]any)
	for _, tx := range batch.GetTransactions() {
		from := strings.ToLower(tx.GetFrom())
		if from == "" {
			continue
		}
		if pending[from] == nil {
			pending[from] = make(map[string]any)
		}
		nonce := fmt.Sprintf("%d", tx.GetNonce())
		pending[from][nonce] = mempoolTxToRPCObject(tx)
	}

	return map[string]any{
		"pending": pending,
		"queued":  map[string]any{}, // JMDN mempool has no nonce-gap staging
	}, nil
}

// mempoolTxToRPCObject converts a proto Transaction to the Ethereum JSON-RPC tx object shape.
func mempoolTxToRPCObject(tx interface {
	GetHash() string
	GetFrom() string
	GetTo() string
	GetValue() string
	GetNonce() uint64
	GetGasLimit() string
	GetGasPrice() string
	GetMaxFee() string
	GetMaxPriorityFee() string
	GetData() []byte
	GetType() uint32
	GetV() string
	GetR() string
	GetS() string
}) map[string]any {
	decToHex := func(dec string) string {
		if dec == "" {
			return "0x0"
		}
		n, ok := new(big.Int).SetString(dec, 10)
		if !ok || n == nil {
			return "0x0"
		}
		return "0x" + n.Text(16)
	}

	// Contract creations have an empty "to" field; geth renders those as null.
	var to any
	if t := tx.GetTo(); t != "" {
		to = strings.ToLower(t)
	}

	obj := map[string]any{
		"blockHash":        nil,
		"blockNumber":      nil,
		"transactionIndex": nil,
		"hash":             tx.GetHash(),
		"from":             strings.ToLower(tx.GetFrom()),
		"to":               to,
		"nonce":            fmt.Sprintf("0x%x", tx.GetNonce()),
		"gas":              decToHex(tx.GetGasLimit()),
		"value":            decToHex(tx.GetValue()),
		"input":            "0x" + hex.EncodeToString(tx.GetData()),
		"type":             fmt.Sprintf("0x%x", tx.GetType()),
		"v":                tx.GetV(), // already "0x…" hex — see getSignatureString, gRPCclient.go:756
		"r":                tx.GetR(),
		"s":                tx.GetS(),
	}

	if tx.GetType() == 2 {
		obj["maxFeePerGas"] = decToHex(tx.GetMaxFee())
		obj["maxPriorityFeePerGas"] = decToHex(tx.GetMaxPriorityFee())
	} else {
		obj["gasPrice"] = decToHex(tx.GetGasPrice())
	}

	return obj
}
