package DID

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/logging"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"gossipnode/DB_OPs"
	pb "gossipnode/DID/proto"
	"gossipnode/config"
	"gossipnode/messaging"

	"go.uber.org/zap"
)

const (
	LOG_FILE = "DID.log"
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
	return DB_OPs.GetAccountsConnection()
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
		log.Warn().Err(err).Msg("Failed to initialize Account server database. Running in standalone mode.")
		AccountServer.standalone = true
		return AccountServer
	} else {
		// Return the test connection immediately
		AccountServer.db.PutAccountsConnection(&conn)
	}

	AccountServer.accountsClient = &conn
	conn.Client.Logger.Logger.Info("Successfully connected to the Accounts Pool",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "DID.NewAccountServer"),
	)
	return AccountServer
}

func (s *AccountServer) Initialize() (config.PooledConnection, error) {
	// Just test the connection to verify we can connect
	conn, err := s.db.GetAccountsConnection()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get accounts database connection. Running in standalone mode.")
		s.standalone = true
		return config.PooledConnection{}, err
	}
	conn.Client.Logger.Logger.Info("Successfully connected to the Accounts Pool",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "DID.Initialize"),
	)
	return *conn, nil
}

// Close releases resources used by the server
func (s *AccountServer) Close() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.accountsClient.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "DID.Close"),
	)
	s.db.PutAccountsConnection(s.accountsClient)
	s.accountsClient = nil
	s.standalone = true
}

// storeAccount stores a Account either in the database or in memory
func (s *AccountServer) storeAccount(DIDAddress string, Address common.Address, metadata map[string]interface{}) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Store in the database via our interface
	err := s.db.CreateAccount(s.accountsClient, DIDAddress, Address, metadata)
	if err != nil {
		s.accountsClient.Client.Logger.Logger.Error("Failed to store Account",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "DID.storeAccount"),
		)
		return err
	}
	s.accountsClient.Client.Logger.Logger.Info("Account stored successfully",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "DID.storeAccount"),
	)
	return nil
}

// getAccount retrieves a Account either from the database or memory
func (s *AccountServer) getAccount(AccountID string) (*DB_OPs.Account, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	// Check if AccountID is a DID (starts with "did:") or an address
	if strings.HasPrefix(AccountID, "did:") {
		// It's a DID, use GetAccountByDID
		s.accountsClient.Client.Logger.Logger.Info("Retrieving account by DID",
			zap.String("did", AccountID),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "DID.getAccount"),
		)
		return DB_OPs.GetAccountByDID(s.accountsClient, AccountID)
	} else {
		// It's an address, use GetAccount
		TempAccountAddress := common.HexToAddress(AccountID)
		s.accountsClient.Client.Logger.Logger.Info("Retrieving account by address",
			zap.String("address", AccountID),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "DID.getAccount"),
		)
		return s.db.GetAccount(s.accountsClient, TempAccountAddress)
	}
}

// listAccounts retrieves all Accounts either from the database or memory
func (s *AccountServer) listAccounts(limit int) ([]*DB_OPs.Account, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	// Otherwise get from the database
	data, err := s.db.ListAllAccounts(s.accountsClient, limit)
	if err != nil {
		s.accountsClient.Client.Logger.Logger.Error("Failed to list Accounts",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "DID.listAccounts"),
		)
		return nil, err
	}
	s.accountsClient.Client.Logger.Logger.Info("Accounts listed successfully",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "DID.listAccounts"),
	)
	return data, nil
}

// RegisterDID registers a new DID
func (s *AccountServer) RegisterDID(ctx context.Context, req *pb.RegisterDIDRequest) (*pb.RegisterDIDResponse, error) {
	if req.Did == "" || req.PublicKey == "" {
		return nil, status.Error(codes.InvalidArgument, "DID and public key are required")
	}

	// Check if account already exists
	existingAccount, err := s.getAccount(req.PublicKey)
	if err == nil && existingAccount != nil {
		var Tempmetadata []byte
		Tempmetadata, err = json.Marshal(existingAccount.Metadata)
		if err != nil {
			Tempmetadata = []byte("{}")
		}
		s.accountsClient.Client.Logger.Logger.Info("Given account already exists",
			zap.String(logging.Address, existingAccount.Address.Hex()),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "DID.RegisterDID"),
		)
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
		s.accountsClient.Client.Logger.Logger.Error("Failed to store account",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "DID.RegisterDID"),
		)
		return nil, status.Errorf(codes.Internal, "Failed to store account: %v", err)
	}

	// Retrive the account from the DB
	account, err := s.getAccount(req.PublicKey)
	if err != nil {
		s.accountsClient.Client.Logger.Logger.Error("Failed to retrieve account",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "DID.RegisterDID"),
		)
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
			s.accountsClient.Client.Logger.Logger.Warn("Failed to propagate DID to network",
				zap.Error(err),
				zap.String("did", req.Did),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "DID.RegisterDID"),
			)
		}
		s.accountsClient.Client.Logger.Logger.Info("Successfully propagated DID to network",
			zap.String("did", req.Did),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "DID.RegisterDID"),
		)
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

	// Retrieve DID
	didDoc, err := s.getAccount(req.Did)
	if err != nil {
		s.accountsClient.Client.Logger.Logger.Error("Failed to retrieve account",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "DID.GetDID"),
		)

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
		s.accountsClient.Client.Logger.Logger.Error("Failed to convert metadata to string",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "DID.GetDID"),
		)
		Tempmetadata = []byte("{}")
	}
	s.accountsClient.Client.Logger.Logger.Info("Successfully converted metadata to string",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "DID.GetDID"),
	)
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
		s.accountsClient.Client.Logger.Logger.Error("Failed to list accounts",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "DID.ListDIDs"),
		)
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
	s.accountsClient.Client.Logger.Logger.Info("Successfully listed accounts",
		zap.String("count", fmt.Sprintf("%d", len(pbDids))),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "DID.ListDIDs"),
	)

	return &pb.ListDIDsResponse{
		Dids:       pbDids,
		TotalCount: int32(len(pbDids)),
	}, nil
}

// GetAccountStats retrieves current Account system statistics
func (s *AccountServer) GetAccountStats(ctx context.Context, _ *emptypb.Empty) (*pb.DIDStats, error) {
	s.mutex.RLock()
	statsAge := s.statsAge
	stats := s.stats
	s.mutex.RUnlock()

	// Always refresh stats on first call (statsAge == 0) or if older than 60 seconds
	if statsAge == 0 || time.Now().UTC().Unix()-statsAge > 60 {
		// Force refresh stats synchronously if they're empty
		s.refreshStats()

		// Read the refreshed stats
		s.mutex.RLock()
		stats = s.stats
		s.mutex.RUnlock()

		// If still empty after refresh, there might be a database issue
		if stats.TotalDids == 0 {
			log.Warn().Msg("DID stats still empty after refresh - possible database connection issue")

			// Try to diagnose the problem
			if s.standalone {
				log.Info().Msg("Running in standalone mode, expected empty stats if no DIDs created locally")
			} else if s.accountsClient == nil {
				log.Warn().Msg("Account client is nil despite not being in standalone mode")
			}
		}
	}

	return stats, nil
}

// refreshStats refreshes the DID statistics
func (s *AccountServer) refreshStats() {
	log.Debug().Msg("Refreshing DID statistics...")

	// Get all DIDs
	dids, err := s.listAccounts(10000)
	if err != nil {
		log.Error().
			Err(err).
			Bool("standalone", s.standalone).
			Bool("hasAccountsClient", s.accountsClient != nil).
			Msg("Failed to get DIDs for stats calculation")
		return
	}

	log.Debug().Int("count", len(dids)).Msg("Successfully retrieved DIDs for stats")

	// Calculate total balance
	totalBalance := big.NewInt(0)
	for _, did := range dids {
		if did.Balance != "" {
			balance, ok := new(big.Int).SetString(did.Balance, 10)
			if ok {
				totalBalance.Add(totalBalance, balance)
			}
		}
	}

	// Update stats
	s.mutex.Lock()
	s.stats = &pb.DIDStats{
		TotalDids:    int32(len(dids)),
		TotalBalance: totalBalance.String(),
		LastUpdate:   time.Now().UTC().Unix(),
	}
	s.statsAge = time.Now().UTC().Unix()
	s.mutex.Unlock()

	log.Info().
		Int32("total_dids", s.stats.TotalDids).
		Str("total_balance", s.stats.TotalBalance).
		Msg("DID statistics updated successfully")
}

// StartDIDServer starts the DID gRPC server
func StartDIDServer(h host.Host, address string, existingClient *config.PooledConnection) error {
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
			log.Warn().Err(err).Msg("Failed to initialize DID server database. Running in standalone mode.")
			return err
		}
		server.accountsClient = &conn
	}

	// Create gRPC server
	grpcServer := grpc.NewServer()
	pb.RegisterDIDServiceServer(grpcServer, server)

	// Register reflection service
	reflection.Register(grpcServer)

	log.Info().
		Str("address", address).
		Bool("standalone", server.standalone).
		Msg("Starting DID gRPC server")

	// In StartDIDServer function, add after server initialization:
	go func() {
		// Give the server a moment to fully initialize
		time.Sleep(2 * time.Second)
		server.refreshStats()
		log.Info().Msg("Initial DID statistics refresh completed")
	}()

	return grpcServer.Serve(lis)
}
