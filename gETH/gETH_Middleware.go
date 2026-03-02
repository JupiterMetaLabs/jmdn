package gETH

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	block "jmdn/Block"
	"jmdn/DB_OPs"
	"jmdn/config"
	"jmdn/gETH/proto"

	"github.com/ethereum/go-ethereum/common"
)

type ServiceInterface interface {
	GetBlockByNumber(req *proto.GetBlockByNumberReq) (*proto.Block, error)
	GetBlockByHash(req *proto.GetBlockByHashReq) (*proto.Block, error)
	GetTransactionByHash(req *proto.GetByHashReq) (*proto.Transaction, error)
	GetReceiptByHash(req *proto.GetByHashReq) (*proto.Receipt, error)
	GetAccountState(req *proto.GetAccountStateReq) (*proto.AccountState, error)
	GetLogs(req *proto.GetLogsReq) (*proto.GetLogsResp, error)
	Call(req *proto.CallReq) (*proto.CallResp, error)
	EstimateGas(req *proto.CallReq) (*proto.EstimateResp, error)
	SendRawTx(req *proto.SendRawTxReq) (*proto.SendRawTxResp, error)
	GetChainID(req *proto.Empty) (*proto.Quantity, error)
}

func _GetBlockByNumber(req *proto.GetBlockByNumberReq) (*proto.Block, error) {
	// Init DB
	// Conn, err := initDBs()
	// if err != nil {
	// 	return nil, err
	// }
	// First call the exisitng apis to get the block by the number
	zkblock, err := DB_OPs.GetZKBlockByNumber(nil, req.Number)
	if err != nil {
		return nil, err
	}

	// Now Convert the ZKBlock structure to the gETHConfig.Block structure
	block, err := ConvertZKBlockToETHBlock(zkblock)
	if err != nil {
		return nil, err
	}
	return block, nil
}

func _GetBlockByHash(req *proto.GetBlockByHashReq) (*proto.Block, error) {
	// Init DB
	// Conn, err := initDBs()
	// if err != nil {
	// 	return nil, err
	// }
	// Convert the hash to string
	reqHash := hex.EncodeToString(req.Hash)
	if reqHash[0:2] == "0x" {
		reqHash = reqHash[2:]
	}

	// First call the exisitng apis to get the block by the number
	zkblock, err := DB_OPs.GetZKBlockByHash(nil, reqHash)
	if err != nil {
		return nil, err
	}

	// Now Convert the ZKBlock structure to the gETHConfig.Block structure
	block, err := ConvertZKBlockToETHBlock(zkblock)
	if err != nil {
		return nil, err
	}
	return block, nil
}

func _GetTransactionByHash(req *proto.GetByHashReq) (*proto.Transaction, error) {
	// Init DB
	// Conn, err := initDBs()
	// if err != nil {
	// 	return nil, err
	// }
	// Convert the hash to string
	reqHash := hex.EncodeToString(req.Hash)
	if reqHash[0:2] == "0x" {
		reqHash = reqHash[2:]
	}

	Txn, err := DB_OPs.GetTransactionByHash(nil, reqHash)
	if err != nil {
		return nil, err
	}

	value, err := ConvertConfigTxnToETHTransaction(Txn)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func _GetReceiptByHash(req *proto.GetByHashReq) (*proto.Receipt, error) {

	Blockreq := &proto.GetBlockByHashReq{
		Hash: req.Hash,
	}
	// Get Block by hash first
	Block, err := _GetBlockByHash(Blockreq)
	if err != nil {
		return nil, err
	}

	return ConvertGETHBlocktoReceipt(Block)
}

func _GetAccountState(req *proto.GetAccountStateReq) (*proto.AccountState, error) {
	// Init DB
	// Conn, err := initDBs()
	// if err != nil {
	// 	return nil, err
	// }

	// Get Txns by DID
	// convert the req.Address from bytes to common.Address
	addr := common.Address(req.Address)
	Txns, err := DB_OPs.GetTransactionsByAccount(nil, &addr)
	if err != nil {
		return nil, err
	}

	// Sort the Txns by nonce
	Txns = SortTransactionsByNonce(Txns)
	// Now pick the last nonce
	nonce := Txns[len(Txns)-1].Nonce

	// Create hash of all transactions
	txHash, err := HashTransactions(Txns)
	if err != nil {
		return nil, fmt.Errorf("failed to hash transactions: %w", err)
	}

	// Get the DID Details to get the balance
	// Conver the req.Address bytes to common.Address
	DIDDetails, err := DB_OPs.GetAccount(nil, common.Address(req.Address))
	if err != nil {

		return nil, err
	}

	// Convert nonce to bytes
	nonceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(nonceBytes, nonce)

	// Create and return the account state
	return &proto.AccountState{
		Nonce:       nonceBytes,
		Balance:     []byte(DIDDetails.Balance),
		StorageRoot: []byte(txHash),
		CodeHash:    []byte{},
		Code:        []byte{},
	}, nil
}

func _SubmitRawTransaction(req *proto.SendRawTxReq) (*proto.SendRawTxResp, error) {
	// Convert Signed Transaction bytes to proper DS
	var tx config.Transaction
	err := json.Unmarshal(req.SignedTx, &tx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Debugging
	fmt.Println("Transaction: ", tx)
	fmt.Println("Transaction Type: ", tx.Type)
	fmt.Println("Gas Fee Type: ", tx.GasPrice)
	fmt.Println("Gas Fee: ", tx.GasPrice)
	hash, err := block.SubmitRawTransaction(ctx, &tx)
	if err != nil {
		return nil, err
	}

	return &proto.SendRawTxResp{TxHash: common.HexToHash(hash).Bytes()}, nil
}

/* UNUSED
func _EstimateGas(req *proto.CallReq) (*proto.EstimateResp, error) {
	// Get the Mempool Client
	RoutingClient, err := block.ReturnMempoolObject()
	if err != nil {
		return nil, fmt.Errorf("failed to get mempool client: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Get the Fee Stats
	feeStats, err := RoutingClient.WrapperGetFeeStatistics(ctx)
	if err != nil {
		return nil, err
	}

	return &proto.EstimateResp{
		GasEstimate: feeStats.RecommendedFees.Standard,
	}, nil
}
*/

func _GetChainID(req *proto.Empty, chainID int) (*proto.Quantity, error) {
	return &proto.Quantity{Value: uint64(chainID)}, nil
}
