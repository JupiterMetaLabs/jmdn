package DID

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"gossipnode/DB_OPs"
	pb "gossipnode/DID/proto"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/logging"
	"gossipnode/messaging"
	"gossipnode/pkg/gatekeeper"
)

// Returns nil if the logger system is not yet initialized (safe: callers must nil-check
// or use this only after logger initialization).
func logger() *ion.Ion {
	logInstance, err := logging.NewAsyncLogger().Get().NamedLogger(logging.DID, "")
	if err != nil {
		return nil
	}
	return logInstance.NamedLogger
}

const (
	LOG_FILE = ""
	TOPIC    = "DID"
)

// dbOps defines the interface for database operations that AccountServer depends on.
// This allows for mocking the database in tests.
type dbOps interface {
	GetAccount(conn *config.PooledConnection, address common.Address) (*DB_OPs.Account, error)
	ListAllAccounts(conn *config.PooledConnection, limit int) ([]*DB_OPs.Account, error)
	CreateAccount(conn *config.PooledConnection, DIDAddress string, Address common.Address, metadata map[string]interface{}) error
	GetAccountsConnection() (*config.PooledConnection, error)
	PutAccountsConnection(conn *config.PooledConnection)
}

// realDbOps is the implementation of dbOps that calls the actual DB_OPs functions.
type realDbOps struct{}

func (r *realDbOps) GetAccount(conn *config.PooledConnection, address common.Address) (*DB_OPs.Account, error) {
	return DB_OPs.GetAccount(conn, address)
}

func (r *realDbOps) ListAllAccounts(conn *config.PooledConnection, limit int) ([]*DB_OPs.Account, error) {
	return DB_OPs.ListAllAccounts(conn, limit)
}

func (r *realDbOps) CreateAccount(conn *config.PooledConnection, DIDAddress string, Address common.Address, metadata map[string]interface{}) error {
	return DB_OPs.CreateAccount(conn, DIDAddress, Address, metadata)
}

func (r *realDbOps) GetAccountsConnection() (*config.PooledConnection, error) {
	return DB_OPs.GetAccountConnectionandPutBack(context.Background())
}

func (r *realDbOps) PutAccountsConnection(conn *config.PooledConnection) {
	DB_OPs.PutAccountsConnection(conn)
}

// Account and DID Server implements the DIDService
type AccountServer struct {
	pb.UnimplementedDIDServiceServer
	host           host.Host
	mutex          sync.RWMutex
	statsAge       int64
	stats          *pb.DIDStats
	accountsClient *config.PooledConnection
	standalone     bool
	db             dbOps // New field for testability
}

// NewAccountServer creates a new Account server
func NewAccountServer(h host.Host) *AccountServer {
	// Create server without holding connections indefinitely
	AccountServer := &AccountServer{
		host:       h,
		statsAge:   0,
		stats:      &pb.DIDStats{},
		standalone: false,
		db:         &realDbOps{}, // Use the real implementation
	}

	// Test connection but don't hold it
	conn, err := AccountServer.Initialize()
	if err != nil {
		logger().Warn(context.Background(), "Failed to initialize Account server database. Running in standalone mode.", ion.Err(err))
		AccountServer.standalone = true
		return AccountServer
	} else {
		// Return the test connection immediately
		AccountServer.db.PutAccountsConnection(&conn)
	}

	AccountServer.accountsClient = &conn
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn.Client.Logger.Info(loggerCtx, "Successfully connected to the Accounts Pool",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DID.NewAccountServer"))
	return AccountServer
}

func (s *AccountServer) Initialize() (config.PooledConnection, error) {
	// Just test the connection to verify we can connect
	conn, err := s.db.GetAccountsConnection()
	if err != nil {
		logger().Warn(context.Background(), "Failed to get accounts database connection. Running in standalone mode.", ion.Err(err))
		s.standalone = true
		return config.PooledConnection{}, err
	}
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn.Client.Logger.Info(loggerCtx, "Successfully connected to the Accounts Pool",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DID.Initialize"))
	return *conn, nil
}

// Close releases resources used by the server
func (s *AccountServer) Close() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.accountsClient.Client.Logger.Info(loggerCtx, "Client Connection is returned to the Pool",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DID.Close"))
	s.db.PutAccountsConnection(s.accountsClient)
	s.accountsClient = nil
	s.standalone = true
}

// storeAccount stores a Account either in the database or in memory
func (s *AccountServer) storeAccount(DIDAddress string, Address common.Address, metadata map[string]interface{}) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Store in the database via our interface
	err := s.db.CreateAccount(s.accountsClient, DIDAddress, Address, metadata)
	if err != nil {

		s.accountsClient.Client.Logger.Error(loggerCtx, "Failed to store Account",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.storeAccount"))
		return err
	}

	s.accountsClient.Client.Logger.Info(loggerCtx, "Account stored successfully",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DID.storeAccount"))
	return nil
}

// getAccount retrieves a Account either from the database or memory
func (s *AccountServer) getAccount(AccountID string) (*DB_OPs.Account, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Check if AccountID is a DID (starts with "did:") or an address
	if strings.HasPrefix(AccountID, "did:") {
		// It's a DID, use GetAccountByDID

		s.accountsClient.Client.Logger.Info(loggerCtx, "Retrieving account by DID",
			ion.String("did", AccountID),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.getAccount"))
		return DB_OPs.GetAccountByDID(s.accountsClient, AccountID)
	} else {
		// It's an address, use GetAccount
		TempAccountAddress := common.HexToAddress(AccountID)

		s.accountsClient.Client.Logger.Info(loggerCtx, "Retrieving account by address",
			ion.String("address", AccountID),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.getAccount"))
		return s.db.GetAccount(s.accountsClient, TempAccountAddress)
	}
}

// listAccounts retrieves all Accounts either from the database or memory
func (s *AccountServer) listAccounts(limit int) ([]*DB_OPs.Account, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Otherwise get from the database
	data, err := s.db.ListAllAccounts(s.accountsClient, limit)
	if err != nil {

		s.accountsClient.Client.Logger.Error(loggerCtx, "Failed to list Accounts",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.listAccounts"))
		return nil, err
	}

	s.accountsClient.Client.Logger.Info(loggerCtx, "Accounts listed successfully",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DID.listAccounts"))
	return data, nil
}

// RegisterDID registers a new DID
func (s *AccountServer) RegisterDID(ctx context.Context, req *pb.RegisterDIDRequest) (*pb.RegisterDIDResponse, error) {
	if req.Did == "" || req.PublicKey == "" {
		return nil, status.Error(codes.InvalidArgument, "DID and public key are required")
	}
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Check if account already exists
	existingAccount, err := s.getAccount(req.PublicKey)
	if err == nil && existingAccount != nil {
		var Tempmetadata []byte
		Tempmetadata, err = json.Marshal(existingAccount.Metadata)
		if err != nil {
			Tempmetadata = []byte("{}")
		}

		s.accountsClient.Client.Logger.Info(loggerCtx, "Given account already exists",
			ion.String("address", existingAccount.Address.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.RegisterDID"))
		return &pb.RegisterDIDResponse{
			Success: false,
			Message: fmt.Sprintf("Account with public key %s already exists", req.PublicKey),
			DidInfo: &pb.DIDInfo{
				Did:         existingAccount.DIDAddress,
				PublicKey:   existingAccount.Address.Hex(),
				Balance:     existingAccount.Balance,
				CreatedAt:   existingAccount.CreatedAt,
				UpdatedAt:   existingAccount.UpdatedAt,
				AccountType: existingAccount.AccountType,
				Nonce:       fmt.Sprintf("%d", existingAccount.Nonce),
				Metadata:    string(Tempmetadata),
			},
		}, nil
	}

	// Store the account using our helper method
	err = s.storeAccount(req.Did, common.HexToAddress(req.PublicKey), nil)
	if err != nil {

		s.accountsClient.Client.Logger.Error(loggerCtx, "Failed to store account",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.RegisterDID"))
		return nil, status.Errorf(codes.Internal, "Failed to store account: %v", err)
	}

	// Retrive the account from the DB
	account, err := s.getAccount(req.PublicKey)
	if err != nil {

		s.accountsClient.Client.Logger.Error(loggerCtx, "Failed to retrieve account",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.RegisterDID"))
		return nil, status.Errorf(codes.Internal, "Failed to retrieve account: %v", err)
	}

	// If not in standalone mode, propagate the DID
	if !s.standalone && s.accountsClient != nil {
		// Try to propagate to network, but don't fail if it doesn't work
		err = messaging.PropagateDID(s.host, &DB_OPs.Account{
			Address:     account.Address,
			Balance:     account.Balance,
			Nonce:       account.Nonce,
			CreatedAt:   account.CreatedAt,
			UpdatedAt:   account.UpdatedAt,
			DIDAddress:  account.DIDAddress,
			AccountType: account.AccountType,
			Metadata:    account.Metadata,
		})
		if err != nil {

			s.accountsClient.Client.Logger.Warn(loggerCtx, "Failed to propagate DID to network",
				ion.String("error", err.Error()),
				ion.String("did", req.Did),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DID.RegisterDID"))
		}

		s.accountsClient.Client.Logger.Info(loggerCtx, "Successfully propagated DID to network",
			ion.String("did", req.Did),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.RegisterDID"))
	}

	// Return success response
	return &pb.RegisterDIDResponse{
		Success: true,
		Message: "DID registered successfully",
		DidInfo: &pb.DIDInfo{
			Did:       req.Did,
			PublicKey: req.PublicKey,
			Balance:   "0",
			CreatedAt: time.Now().UTC().Unix(),
			UpdatedAt: time.Now().UTC().Unix(),
		},
	}, nil
}

// GetDID retrieves information about a specific DID
func (s *AccountServer) GetDID(ctx context.Context, req *pb.GetDIDRequest) (*pb.DIDResponse, error) {
	if req.Did == "" {
		return nil, status.Error(codes.InvalidArgument, "DID is required")
	}
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Retrieve DID
	didDoc, err := s.getAccount(req.Did)
	if err != nil {

		s.accountsClient.Client.Logger.Error(loggerCtx, "Failed to retrieve account",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.GetDID"))

		return &pb.DIDResponse{
			Exists: false,
			DidInfo: &pb.DIDInfo{
				Did: req.Did,
			},
		}, nil
	}

	// First convert the metadata into string
	var Tempmetadata []byte
	Tempmetadata, err = json.Marshal(didDoc.Metadata)
	if err != nil {

		s.accountsClient.Client.Logger.Error(loggerCtx, "Failed to convert metadata to string",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.GetDID"))
		Tempmetadata = []byte("{}")
	}

	s.accountsClient.Client.Logger.Info(loggerCtx, "Successfully converted metadata to string",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DID.GetDID"))
	// Return DID information
	return &pb.DIDResponse{
		Exists: true,
		DidInfo: &pb.DIDInfo{
			Did:         didDoc.DIDAddress,
			PublicKey:   didDoc.Address.Hex(),
			Balance:     didDoc.Balance,
			CreatedAt:   didDoc.CreatedAt,
			UpdatedAt:   didDoc.UpdatedAt,
			AccountType: didDoc.AccountType,
			Nonce:       fmt.Sprintf("%d", didDoc.Nonce),
			Metadata:    string(Tempmetadata),
		},
	}, nil
}

// ListDIDs lists all DIDs with pagination
func (s *AccountServer) ListDIDs(ctx context.Context, req *pb.ListDIDsRequest) (*pb.ListDIDsResponse, error) {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 100 // Default limit
	}

	// Get DIDs
	dids, err := s.listAccounts(limit)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.accountsClient.Client.Logger.Error(loggerCtx, "Failed to list accounts",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DID.ListDIDs"))
		return nil, status.Errorf(codes.Internal, "Failed to list accounts: %v", err)
	}

	// Convert to proto format
	var pbDids []*pb.DIDInfo
	for _, did := range dids {
		pbDids = append(pbDids, &pb.DIDInfo{
			Did:         did.DIDAddress,
			PublicKey:   did.Address.Hex(),
			Balance:     did.Balance,
			CreatedAt:   did.CreatedAt,
			UpdatedAt:   did.UpdatedAt,
			AccountType: did.AccountType,
			Nonce:       fmt.Sprintf("%d", did.Nonce),
		})
	}
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.accountsClient.Client.Logger.Info(loggerCtx, "Successfully listed accounts",
		ion.String("count", fmt.Sprintf("%d", len(pbDids))),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DID.ListDIDs"))

	return &pb.ListDIDsResponse{
		Dids:       pbDids,
		TotalCount: int32(len(pbDids)),
	}, nil
}

// StartDIDServer starts the DID gRPC server
func StartDIDServer(h host.Host, address string, existingClient *config.PooledConnection) error {
	return StartDIDServerWithContext(context.Background(), h, address, existingClient)
}

// StartDIDServerWithContext starts the DID gRPC server and gracefully stops it when ctx is cancelled.
func StartDIDServerWithContext(ctx context.Context, h host.Host, address string, existingClient *config.PooledConnection) error {
	lis, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", address, err)
	}

	// Create DID server with existing client
	server := NewAccountServer(h)

	// Set the existing client if provided
	if existingClient != nil {
		server.accountsClient = existingClient
	} else {
		// Try to initialize a new client
		conn, err := server.Initialize()
		if err != nil {
			logger().Warn(ctx, "Failed to initialize DID server database. Running in standalone mode.", ion.Err(err))
			return err
		}
		server.accountsClient = &conn
	}

	// Initialize Logger
	var ionLogger *ion.Ion
	// We try to use existing client logger if available, else create async logger
	if remainingClient := existingClient; remainingClient != nil {
		ionLogger = remainingClient.Client.Logger
	} else {
		ionLogger = logger()
	}

	// Create secure gRPC server via gatekeeper helper
	secCfg := &settings.Get().Security
	grpcServer, serverTLS, err := gatekeeper.NewSecureGRPCServer(
		settings.ServiceDID, secCfg, ionLogger, false,
	)
	if err != nil {
		return fmt.Errorf("failed to create secure gRPC server: %w", err)
	}
	if serverTLS != nil && ionLogger != nil {
		ionLogger.Info(ctx, "TLS Enabled for DID Service")
	}
	pb.RegisterDIDServiceServer(grpcServer, server)

	// Register reflection service
	reflection.Register(grpcServer)

	logger().Info(ctx, "Starting DID gRPC server",
		ion.String("address", address),
		ion.Bool("standalone", server.standalone))

	errCh := make(chan error, 1)
	go func() {
		errCh <- grpcServer.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		// Stop accepting new connections and unblock Serve().
		go func() {
			grpcServer.GracefulStop()
		}()
		_ = lis.Close()
		return nil
	case err := <-errCh:
		return err
	}
}
