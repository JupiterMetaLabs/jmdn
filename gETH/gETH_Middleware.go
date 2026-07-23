package gETH

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	block "gossipnode/Block"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/proto"

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

func _GetBlockByNumber(ctx context.Context, req *proto.GetBlockByNumberReq) (*proto.Block, error) {
	// First call the exisitng apis to get the block by the number
	zkblock, err := DB_OPs.ReadZKBlockByNumber(ctx, nil, req.Number)
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

func _GetBlockByHash(ctx context.Context, req *proto.GetBlockByHashReq) (*proto.Block, error) {
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

func _GetTransactionByHash(ctx context.Context, req *proto.GetByHashReq) (*proto.Transaction, error) {
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

func _GetReceiptByHash(ctx context.Context, req *proto.GetByHashReq) (*proto.Receipt, error) {
	Blockreq := &proto.GetBlockByHashReq{
		Hash: req.Hash,
	}
	// Get Block by hash first
	Block, err := _GetBlockByHash(ctx, Blockreq)
	if err != nil {
		return nil, err
	}

	return ConvertGETHBlocktoReceipt(Block)
}

func _GetAccountState(ctx context.Context, req *proto.GetAccountStateReq) (*proto.AccountState, error) {
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

func _SubmitRawTransaction(ctx context.Context, req *proto.SendRawTxReq) (*proto.SendRawTxResp, error) {
	// Convert Signed Transaction bytes to proper DS
	var tx config.Transaction
	err := json.Unmarshal(req.SignedTx, &tx)
	if err != nil {
		return nil, err
	}
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

func _GetChainID(ctx context.Context, req *proto.Empty, chainID int) (*proto.Quantity, error) {
	return &proto.Quantity{Value: uint64(chainID)}, nil
}
