package Block

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "gossipnode/Mempool/proto"
	"gossipnode/config"
)

// MempoolClient provides methods to interact with the mempool service
type MempoolClient struct {
	client pb.MempoolServiceClient
	conn   *grpc.ClientConn
}

// NewMempoolClient creates a new mempool client connection
func NewMempoolClient(address string) (*MempoolClient, error) {
	// Create a gRPC connection to the mempool service
	conn, err := grpc.Dial(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mempool service: %v", err)
	}

	client := pb.NewMempoolServiceClient(conn)

	return &MempoolClient{
		client: client,
		conn:   conn,
	}, nil
}

// Close closes the gRPC connection
func (m *MempoolClient) Close() error {
	if m.conn != nil {
		return m.conn.Close()
	}
	return nil
}

// SubmitTransaction submits a transaction to the mempool
func (m *MempoolClient) SubmitTransaction(tx *config.ZKBlockTransaction, txHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Convert the transaction to the protobuf format
	pbTx := convertToPbTransaction(tx, txHash)
	log.Printf("Submitting transaction to mempool: %+v", pbTx)

	// Submit the transaction to the mempool
	resp, err := m.client.SubmitTransaction(ctx, pbTx)
	if err != nil {
		log.Printf("Failed to submit transaction to mempool: %v", err)
		return fmt.Errorf("failed to submit transaction to mempool: %v", err)
	}

	if !resp.Success {
		log.Printf("Mempool rejected transaction %s: %s", resp.Hash, resp.Error)
		return fmt.Errorf("mempool rejected transaction: %s", resp.Error)
	}

	log.Printf("Transaction %s successfully submitted to mempool", resp.Hash)
	return nil
}

// SubmitTransactions submits a batch of transactions to the mempool
func (m *MempoolClient) SubmitTransactions(txs []*config.ZKBlockTransaction) (*pb.BatchSubmitResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // Longer timeout for batches
	defer cancel()

	pbTxs := make([]*pb.Transaction, len(txs))
	for i, tx := range txs {
		// The ZKBlockTransaction should have a pre-computed hash.
		if tx.Hash == "" {
			return nil, fmt.Errorf("transaction at index %d has no hash", i)
		}
		pbTxs[i] = convertToPbTransaction(tx, tx.Hash)
	}

	batch := &pb.TransactionBatch{
		Transactions: pbTxs,
	}

	resp, err := m.client.SubmitTransactions(ctx, batch)
	if err != nil {
		return nil, fmt.Errorf("failed to submit transaction batch: %v", err)
	}

	if !resp.Success {
		// The response itself is returned to allow the caller to inspect partial successes if applicable.
		return resp, fmt.Errorf("mempool rejected transaction batch: %s", resp.Error)
	}

	log.Printf("%d transactions successfully submitted to mempool", resp.Count)
	return resp, nil
}

// GetTransaction retrieves a specific transaction from the mempool by its hash
func (m *MempoolClient) GetTransaction(hash string) (*pb.Transaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &pb.GetTransactionRequest{Hash: hash}
	tx, err := m.client.GetTransaction(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction %s: %v", hash, err)
	}

	return tx, nil
}

// GetPendingTransactions retrieves a list of pending transactions from the mempool
func (m *MempoolClient) GetPendingTransactions(limit int32) (*pb.TransactionBatch, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &pb.GetPendingRequest{Limit: limit}
	batch, err := m.client.GetPendingTransactions(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending transactions: %v", err)
	}

	return batch, nil
}

// GetMempoolStats gets the current mempool statistics
func (m *MempoolClient) GetMempoolStats() (*pb.MempoolStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use the empty.Empty type directly
	stats, err := m.client.GetMempoolStats(ctx, &empty.Empty{})
	if err != nil {
		return nil, fmt.Errorf("failed to get mempool stats: %v", err)
	}

	return stats, nil
}

type GasFeeStats struct {
	MaxFee          uint64
	MinFee          uint64
	MedianFee       uint64
	MeanFee         uint64
	RecommendedFees *pb.RecommendedFees
}

// GetFeeStatistics gets detailed fee statistics from the mempool
func (m *MempoolClient) GetFeeStatistics() (*pb.FeeStatistics, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stats, err := m.client.GetFeeStatistics(ctx, &empty.Empty{})
	if err != nil {
		return nil, fmt.Errorf("failed to get fee statistics: %v", err)
	}

	return stats, nil
}

// Wrapper function for getting FeeStatistics from mempool service
func (m *MempoolClient) WrapperGetFeeStatistics() (*GasFeeStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stats, err := m.client.GetFeeStatistics(ctx, &empty.Empty{})
	if err != nil {
		return nil, fmt.Errorf("failed to get fee statistics: %v", err)
	}

	return &GasFeeStats{
		MaxFee:          stats.MaxFee,
		MinFee:          stats.MinFee,
		MedianFee:       stats.MedianFee,
		MeanFee:         stats.MeanFee,
		RecommendedFees: stats.RecommendedFees,
	}, nil
}

// Helper function to convert a config.ZKBlockTransaction to pb.Transaction
func convertToPbTransaction(tx *config.ZKBlockTransaction, txHash string) *pb.Transaction {
	pbTx := &pb.Transaction{
		Hash:           txHash,
		From:           tx.From,
		To:             tx.To,
		Value:          tx.Value,
		Type:           tx.Type,
		Timestamp:      tx.Timestamp, // Temporarily assign, will be formatted below
		ChainId:        tx.ChainID,
		Nonce:          tx.Nonce,
		GasLimit:       tx.GasLimit,
		MaxFee:         tx.MaxFee,
		MaxPriorityFee: tx.MaxPriorityFee,
		Data:           tx.Data,
		V:              tx.V,
		R:              tx.R,
		S:              tx.S,
	}

	if tx.Timestamp != "" {
		i, err := strconv.ParseInt(tx.Timestamp, 10, 64)
		if err == nil {
			tm := time.Unix(i, 0)
			pbTx.Timestamp = tm.Format(time.RFC3339)
		} else {
			// If parsing fails, it might already be in a different format or invalid.
			// For now, we'll default to the current time as a fallback.
			pbTx.Timestamp = time.Now().Format(time.RFC3339)
		}
	} else {
		pbTx.Timestamp = time.Now().Format(time.RFC3339)
	}

	// Handle transaction fee fields based on type
	if tx.Type == "EIP-1559" || (tx.MaxFee != "" && tx.MaxPriorityFee != "") {
		pbTx.Type = "EIP-1559"
	} else {
		pbTx.Type = "Legacy"
		// For legacy transactions, use GasPrice as MaxFee if MaxFee is not set.
		if pbTx.MaxFee == "" && tx.GasPrice != "" {
			pbTx.MaxFee = tx.GasPrice
		}
	}

	return pbTx
}

// Global mempool client instance
var globalMempoolClient *MempoolClient

// InitMempoolClient initializes the global mempool client
func InitMempoolClient(address string) error {
	client, err := NewMempoolClient(address)
	if err != nil {
		return err
	}

	globalMempoolClient = client
	stats, err := globalMempoolClient.GetMempoolStats()
	if err != nil {
		fmt.Println("Mempool stats failed:", err)
	} else {
		fmt.Println("Mempool stats:", stats)
	}
	return nil
}

// CloseMempoolClient closes the global mempool client
func CloseMempoolClient() {
	if globalMempoolClient != nil {
		globalMempoolClient.Close()
		globalMempoolClient = nil
	}
}

// SubmitToMempool submits a transaction to the mempool instead of propagating it directly
func SubmitToMempool(tx *config.ZKBlockTransaction, txHash string) error {
	if globalMempoolClient == nil {
		return fmt.Errorf("mempool client not initialized")
	}

	return globalMempoolClient.SubmitTransaction(tx, txHash)
}

func ReturnMempoolObject() (*MempoolClient, error) {
	if globalMempoolClient == nil {
		return nil, fmt.Errorf("mempool client not initialized. Call InitMempoolClient() first")
	}
	return globalMempoolClient, nil
}
