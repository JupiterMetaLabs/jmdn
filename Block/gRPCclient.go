package Block

import (
	"context"
	"fmt"
	"log"

	// "math/big"
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
func (m *MempoolClient) SubmitTransaction(tx *config.Transaction, txHash string) error {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    // Convert the transaction to the protobuf format
    pbTx := convertToPbTransaction(tx, txHash)
    
    // Submit the transaction to the mempool
    resp, err := m.client.SubmitTransaction(ctx, pbTx)
    if err != nil {
        return fmt.Errorf("failed to submit transaction to mempool: %v", err)
    }
    
    if !resp.Success {
        return fmt.Errorf("mempool rejected transaction: %s", resp.Error)
    }
    
    log.Printf("Transaction %s successfully submitted to mempool", resp.Hash)
    return nil
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

// Helper function to convert a config.Transaction to pb.Transaction
func convertToPbTransaction(tx *config.Transaction, txHash string) *pb.Transaction {
    pbTx := &pb.Transaction{
        Hash:      txHash,
        From:     tx.From.Hex(),
        ChainId:   tx.ChainID.String(),
        Nonce:     strconv.FormatUint(tx.Nonce, 10),
        GasLimit:  strconv.FormatUint(tx.GasLimit, 10),
        Timestamp: time.Now().Format(time.RFC3339),
        Data:      string(tx.Data),
    }
    
    // Set the from address - we'll need the transaction sender
    // For now we're skipping it as the server would have to extract it
    
    // Set the to address
    if tx.To != nil {
        pbTx.To = tx.To.Hex()
    }
    
    // Set the value
    if tx.Value != nil {
        pbTx.Value = tx.Value.String()
    }
    
    // Set transaction type and type-specific fields
    if tx.MaxFeePerGas != nil {
        pbTx.Type = "EIP-1559"
        pbTx.MaxFee = tx.MaxFeePerGas.String()
        pbTx.MaxPriorityFee = tx.MaxPriorityFeePerGas.String()
    } else {
        pbTx.Type = "Legacy"
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
func SubmitToMempool(tx *config.Transaction, txHash string) error {
    if globalMempoolClient == nil {
        return fmt.Errorf("mempool client not initialized")
    }
    
    return globalMempoolClient.SubmitTransaction(tx, txHash)
}