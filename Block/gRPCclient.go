package Block

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	pb "gossipnode/Mempool/proto"
	"gossipnode/config"
	"gossipnode/logging"

	"github.com/ethereum/go-ethereum/common"
	"github.com/golang/protobuf/ptypes/empty"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	FILENAME   = "mempool.log"
	TOPIC      = "mempool"
	BLOCKTOPIC = "block"
	KEEP_LOGS  = true
	BATCH_SIZE = 100
	BATCH_WAIT = 10 * time.Second
	TIMEOUT    = 5 * time.Second
)

// MempoolClient provides methods to interact with the mempool service
type MempoolClient struct {
	client pb.MempoolServiceClient
	conn   *grpc.ClientConn
	logger *logging.AsyncLogger
}

// NewMempoolClient creates a new mempool client connection
func NewMempoolClient(address string) (*MempoolClient, error) {
	// Create a gRPC connection to the mempool service
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mempool service: %v", err)
	}

	client := pb.NewMempoolServiceClient(conn)

	// Make logging client
	Logger, err := logging.NewAsyncLogger(
		&logging.Logging{
			FileName: FILENAME,
			URL:      "", // Disable Loki by default
			Metadata: logging.LoggingMetadata{
				DIR:       config.LOG_DIR,
				BatchSize: BATCH_SIZE,
				BatchWait: BATCH_WAIT,
				Timeout:   TIMEOUT,
				KeepLogs:  true,
			},
			Topic: TOPIC,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %v", err)
	}

	return &MempoolClient{
		client: client,
		conn:   conn,
		logger: Logger,
	}, nil
}

// Close closes the gRPC connection
func (m *MempoolClient) Close() error {
	// Close the Logger first
	if m.logger != nil {
		m.logger.Close()
	}

	// Close the gRPC connection
	if m.conn != nil {
		return m.conn.Close()
	}
	return nil
}

// SubmitTransaction submits a transaction to the mempool
func (m *MempoolClient) SubmitTransaction(tx *config.Transaction, txHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Convert the transaction to the protobuf format
	pbTx := convertToPbTransaction(tx, txHash)
	log.Printf("Submitting transaction to mempool: %+v", pbTx)

	// Submit the transaction to the routing service
	log.Printf("Getting routing client...")
	RoutingClient, err := GetRoutingClient()
	if err != nil {
		log.Printf("Failed to get routing client: %v", err)
		return fmt.Errorf("routing client connection failed: %v", err)
	}
	log.Printf("Routing client obtained successfully")
	log.Printf("Calling SubmitTransaction on routing client (timeout: 10s)...")
	start := time.Now()
	resp, err := RoutingClient.client.SubmitTransaction(ctx, pbTx)
	duration := time.Since(start)

	if err != nil {
		log.Printf("Failed to submit transaction to mempool after %v: %v", duration, err)
		return fmt.Errorf("failed to submit transaction to mempool: %v", err)
	}
	log.Printf("SubmitTransaction call completed successfully in %v", duration)

	// Log the full response
	log.Printf("Mempool response: success=%t, hash=%s, error=%s, mempool_node=%s, total_replicas=%d",
		resp.Success, resp.Hash, resp.Error, resp.MempoolNode, resp.TotalReplicas)

	if len(resp.ReplicaMempools) > 0 {
		log.Printf("Replica mempools: %v", resp.ReplicaMempools)
	}

	if !resp.Success {
		log.Printf("Mempool rejected transaction %s: %s", resp.Hash, resp.Error)
		return fmt.Errorf("mempool rejected transaction: %s", resp.Error)
	}

	log.Printf("Transaction %s successfully submitted to mempool", resp.Hash)
	return nil
}

// SubmitTransactions submits a batch of transactions to the mempool
func (m *MempoolClient) SubmitTransactions(txs []*config.Transaction) (*pb.BatchSubmitResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Longer timeout for batches
	defer cancel()

	// Log batch submission
	fmt.Printf("Submitting %d transactions to mempool\n", len(txs))

	pbTxs := make([]*pb.Transaction, len(txs))
	for i, tx := range txs {
		// The ZKBlockTransaction should have a pre-computed hash.
		if tx.Hash == (common.Hash{}) {
			return nil, fmt.Errorf("transaction at index %d has no hash", i)
		}
		pbTxs[i] = convertToPbTransaction(tx, tx.Hash.Hex())
	}

	batch := &pb.TransactionBatch{
		Transactions: pbTxs,
	}

	RoutingClient, err := GetRoutingClient()
	if err != nil {
		return nil, err
	}

	log.Printf("Calling SubmitTransactions on routing client (timeout: 15s)...")
	start := time.Now()
	resp, err := RoutingClient.client.SubmitTransactions(ctx, batch)
	duration := time.Since(start)

	if err != nil {
		log.Printf("Failed to submit transactions to mempool after %v: %v", duration, err)
		return nil, fmt.Errorf("routing client could not submit transactions: %s", err)
	}
	log.Printf("SubmitTransactions call completed successfully in %v", duration)

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
	RoutingClient, err := GetRoutingClient()
	if err != nil {
		return nil, err
	}
	tx, err := RoutingClient.client.GetTransaction(ctx, req)
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
	RoutingClient, err := GetRoutingClient()
	if err != nil {
		return nil, err
	}
	batch, err := RoutingClient.client.GetPendingTransactions(ctx, req)
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

// Helper function to convert a config.Transaction to pb.Transaction
func convertToPbTransaction(tx *config.Transaction, txHash string) *pb.Transaction {
	// Helper function to safely convert big.Int to decimal string
	getBigIntString := func(b *big.Int) string {
		if b == nil {
			return "0"
		}
		return b.String()
	}

	// Helper function to safely convert signature fields
	getSignatureString := func(b *big.Int) string {
		if b == nil || b.Cmp(big.NewInt(0)) == 0 {
			return "0x0"
		}
		return "0x" + b.Text(16)
	}

	// Helper function to safely convert data field
	getDataBytes := func(data []byte) []byte {
		if data == nil {
			return []byte{}
		}
		return data
	}

	// Helper function to safely convert address to string
	addrToString := func(addr *common.Address) string {
		if addr == nil {
			return ""
		}
		return addr.Hex()
	}

	pbTx := &pb.Transaction{
		Hash:           txHash,
		From:           addrToString(tx.From),
		To:             addrToString(tx.To),
		Value:          getBigIntString(tx.Value),
		Type:           0, // Will be set correctly below
		Timestamp:      uint64(tx.Timestamp),
		ChainId:        getBigIntString(tx.ChainID),
		Nonce:          uint64(tx.Nonce),
		GasLimit:       fmt.Sprintf("%d", tx.GasLimit),
		GasPrice:       getBigIntString(tx.GasPrice),
		MaxFee:         getBigIntString(tx.MaxFee),
		MaxPriorityFee: getBigIntString(tx.MaxPriorityFee),
		Data:           getDataBytes(tx.Data),
		AccessList:     convertAccessListToPb(tx.AccessList),
		V:              getSignatureString(tx.V),
		R:              getSignatureString(tx.R),
		S:              getSignatureString(tx.S),
	}

	if tx.Timestamp != 0 {
		// Assuming tx.Timestamp is already a Unix timestamp (uint64)
		pbTx.Timestamp = uint64(tx.Timestamp)
	}
	// Handle transaction fee fields based on type
	if tx.Type == 1 || (tx.MaxFee != nil && tx.MaxPriorityFee != nil) {
		pbTx.Type = 2 // EIP-1559
	} else {
		pbTx.Type = 0 // Legacy
		// For legacy transactions, use GasPrice as MaxFee if MaxFee is not set.
		if pbTx.MaxFee == "0" && tx.GasPrice != nil {
			pbTx.MaxFee = tx.GasPrice.String()
		}
	}

	return pbTx
}

// Helper function to convert config AccessList to []*pb.AccessTuple
func convertAccessListToPb(accessList config.AccessList) []*pb.AccessTuple {
	if len(accessList) == 0 {
		return nil
	}

	pbAccessList := make([]*pb.AccessTuple, len(accessList))
	for i, access := range accessList {
		storageKeys := make([]string, len(access.StorageKeys))
		for j, key := range access.StorageKeys {
			storageKeys[j] = key.Hex()
		}
		pbAccessList[i] = &pb.AccessTuple{
			Address:     access.Address.Hex(),
			StorageKeys: storageKeys,
		}
	}
	return pbAccessList
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

func ReturnMempoolObject() (*MempoolClient, error) {
	if globalMempoolClient == nil {
		return nil, fmt.Errorf("mempool client not initialized. Call InitMempoolClient() first")
	}
	return globalMempoolClient, nil
}
