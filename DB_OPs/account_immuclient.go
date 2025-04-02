package DB_OPs

import (
	"context"
	"fmt"
	"gossipnode/config"
	"os"
	"sync"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/metadata"
)

// DIDDocument represents a DID document
type DIDDocument struct {
    DID       string `json:"did"`
    PublicKey string `json:"public_key"`
    Balance   string `json:"balance,omitempty"`
    CreatedAt int64  `json:"created_at"`
    UpdatedAt int64  `json:"updated_at"`
}

// Global lock to ensure only one client initialization happens at a time
var clientInitMutex sync.Mutex

// NewAccountsClient creates a dedicated client for the accounts database
func NewAccountsClient() (*config.ImmuClient, error) {
    // Ensure only one client initialization happens at a time
    clientInitMutex.Lock()
    defer clientInitMutex.Unlock()
    
    // Create a default async logger
    defaultLogger, err := NewAsyncLogger()
    if err != nil {
        return nil, fmt.Errorf("failed to create default logger: %w", err)
    }
    
    // Create a default client
    ic := &config.ImmuClient{
        BaseCtx:     context.Background(),
        RetryLimit:  3,
        Logger:      defaultLogger,
        IsConnected: false,
    }
	if err := os.MkdirAll(config.State_Path_Hidden, 0755); err != nil {
		return nil, fmt.Errorf("failed to create ImmuDB state directory: %w", err)
	}
	
    // Connect to ImmuDB with a longer timeout for database operations
    opts := client.DefaultOptions().
        WithAddress(config.DBAddress).
        WithPort(config.DBPort).
		WithDir(config.State_Path_Hidden).
        WithMaxRecvMsgSize(1024 * 1024 * 20) // 20MB message size

    c, err := client.NewImmuClient(opts)
    if err != nil {
        config.Close(ic.Logger)
        return nil, fmt.Errorf("failed to create client: %w", err)
    }

    // Create context with longer timeout for database operations
    ctx, cancel := context.WithTimeout(ic.BaseCtx, 30*time.Second)
    defer func() {
        if !ic.IsConnected {
            cancel()
            c.Disconnect()
        }
    }()
    
    // Step 1: Login to immudb with default credentials
    log.Info().Msg("Authenticating with ImmuDB for accounts database")
    lr, err := c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
    if err != nil {
        config.Close(ic.Logger)
        return nil, fmt.Errorf("login failed: %w", err)
    }

    // Store initial token for reconnection
    ic.Token = lr.Token
    
    // Add auth token to context
    md := metadata.Pairs("authorization", lr.Token)
    ctx = metadata.NewOutgoingContext(ctx, md)
    
    // Step 2: Check if accounts database exists
    log.Info().Str("database", config.AccountsDBName).Msg("Checking if accounts database exists")
    
    databaseList, err := c.DatabaseList(ctx)
    if err != nil {
        config.Close(ic.Logger)
        return nil, fmt.Errorf("failed to get database list: %w", err)
    }
    
    // Check if accounts database exists
    databaseExists := false
    for _, db := range databaseList.Databases {
        if db.DatabaseName == config.AccountsDBName {
            databaseExists = true
            break
        }
    }
    
    // Step 3: Create accounts database if it doesn't exist
    if !databaseExists {
        log.Info().Str("database", config.AccountsDBName).Msg("Creating accounts database")
        
        // Create the database using the initial token
        err = c.CreateDatabase(ctx, &schema.DatabaseSettings{
            DatabaseName: config.AccountsDBName,
        })
        if err != nil {
            config.Close(ic.Logger)
            return nil, fmt.Errorf("failed to create accounts database: %w", err)
        }
        log.Info().Str("database", config.AccountsDBName).Msg("Accounts database created successfully")
    } else {
        log.Info().Str("database", config.AccountsDBName).Msg("Accounts database already exists")
    }
    
    // Step 4: Re-login to get a fresh token
    // This is critical as tokens are database-specific
    lr, err = c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
    if err != nil {
        config.Close(ic.Logger)
        return nil, fmt.Errorf("failed to refresh login: %w", err)
    }
    
    // Update the token and context
    ic.Token = lr.Token
    md = metadata.Pairs("authorization", lr.Token)
    ctx = metadata.NewOutgoingContext(ctx, md)
    
    // Step 5: Select the accounts database - critical step!
    log.Info().Str("database", config.AccountsDBName).Msg("Selecting accounts database")
    dbResp, err := c.UseDatabase(ctx, &schema.Database{DatabaseName: config.AccountsDBName})
    if err != nil {
        config.Close(ic.Logger)
        return nil, fmt.Errorf("failed to use accounts database: %w", err)
    }
    
    // Step 6: Update token again with the database-specific token
    ic.Token = dbResp.Token
    
    // Keep the context with authentication and database selection
    ic.Client = c
    ic.Ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", ic.Token))
    ic.Cancel = cancel
    ic.IsConnected = true
	ic.Database = config.AccountsDBName
    
    log.Info().Str("database", config.AccountsDBName).Msg("Successfully connected to accounts database")
    return ic, nil
}

// StoreDID stores a DID document in the accounts database
func StoreDID(ic *config.ImmuClient, didDoc *DIDDocument) error {
    if didDoc == nil {
        return fmt.Errorf("DID document cannot be nil")
    }
    
    // Set creation and update timestamps
    if didDoc.CreatedAt == 0 {
        didDoc.CreatedAt = time.Now().Unix()
    }
    didDoc.UpdatedAt = time.Now().Unix()
    
    // Ensure we're using the accounts database
    if err := ensureAccountsDBSelected(ic); err != nil {
        return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
    }
    
    // Store using SafeCreate for cryptographic verification
    key := fmt.Sprintf("did:%s", didDoc.DID)
    return SafeCreate(ic, key, didDoc)
}

// GetDID retrieves a DID document from the accounts database
func GetDID(ic *config.ImmuClient, did string) (*DIDDocument, error) {
    // Ensure we're using the accounts database
    if err := ensureAccountsDBSelected(ic); err != nil {
        return nil, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
    }
    
    key := fmt.Sprintf("did:%s", did)
    
    var doc DIDDocument
    err := SafeReadJSON(ic, key, &doc)
    if err != nil {
        return nil, err
    }
    
    return &doc, nil
}

// UpdateDIDBalance updates the balance for a DID
func UpdateDIDBalance(ic *config.ImmuClient, did string, newBalance string) error {
    // Ensure we're using the accounts database
    if err := ensureAccountsDBSelected(ic); err != nil {
        return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
    }
    
    doc, err := GetDID(ic, did)
    if err != nil {
        return err
    }
    
    doc.Balance = newBalance
    doc.UpdatedAt = time.Now().Unix()
    
    return StoreDID(ic, doc)
}

// ListAllDIDs retrieves all DIDs with a limit
func ListAllDIDs(ic *config.ImmuClient, limit int) ([]*DIDDocument, error) {
    // Ensure we're using the accounts database
    if err := ensureAccountsDBSelected(ic); err != nil {
        return nil, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
    }
    
    keys, err := GetKeys(ic, "did:", limit)
    if err != nil {
        return nil, err
    }
    
    docs := make([]*DIDDocument, 0, len(keys))
    for _, key := range keys {
        var doc DIDDocument
        if err := SafeReadJSON(ic, key, &doc); err != nil {
            config.Warning(ic.Logger, "Error reading DID %s: %v", key, err)
            continue
        }
        docs = append(docs, &doc)
    }
    
    return docs, nil
}

// ensureAccountsDBSelected makes sure we're using the accounts database
// This helps prevent the "please select a database first" error
func ensureAccountsDBSelected(ic *config.ImmuClient) error {
    if ic == nil || ic.Client == nil || !ic.IsConnected {
        return fmt.Errorf("client not connected")
    }

    // Try a simple operation to see if the database selection is still valid
    ctx, cancel := context.WithTimeout(ic.BaseCtx, 5*time.Second)
    defer cancel()
    
    // Use the stored token
    md := metadata.Pairs("authorization", ic.Token)
    ctx = metadata.NewOutgoingContext(ctx, md)
    
    // Try to execute a simple operation to check if we're still connected
    _, err := ic.Client.CurrentState(ctx)
    if err != nil {
        log.Warn().Err(err).Msg("Database state check failed, reconnecting...")
        return reconnectToAccountsDB(ic)
    }
    
    return nil
}

// reconnectToAccountsDB attempts to reestablish a lost connection to the accounts database
func reconnectToAccountsDB(ic *config.ImmuClient) error {
    config.Warning(ic.Logger, "Attempting to reconnect to ImmuDB accounts database")
    
    // Clean up existing connection if any
    if ic.Cancel != nil {
        ic.Cancel()
    }
    
    if ic.Client != nil {
        ic.Client.Disconnect()
    }
    
    ic.IsConnected = false
    
    // Create a new client
    opts := client.DefaultOptions().
        WithAddress(config.DBAddress).
        WithPort(config.DBPort).
        WithMaxRecvMsgSize(1024 * 1024 * 20) // 20MB message size

    c, err := client.NewImmuClient(opts)
    if err != nil {
        return fmt.Errorf("failed to create client during reconnect: %w", err)
    }

    // Create context
    ctx, cancel := context.WithTimeout(ic.BaseCtx, 30*time.Second)
    
    // Login to immudb
    config.Info(ic.Logger, "Authenticating with ImmuDB during reconnect")
    lr, err := c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
    if err != nil {
        cancel()
        c.Disconnect()
        return fmt.Errorf("login failed during reconnect: %w", err)
    }

    // Update token
    ic.Token = lr.Token
    
    // Add auth token to context
    md := metadata.Pairs("authorization", lr.Token)
    ctx = metadata.NewOutgoingContext(ctx, md)

    // Select the accounts database
    log.Info().Str("database", config.AccountsDBName).Msg("Selecting accounts database during reconnection")
    dbResp, err := c.UseDatabase(ctx, &schema.Database{DatabaseName: config.AccountsDBName})
    if err != nil {
        cancel()
        c.Disconnect()
        return fmt.Errorf("failed to select accounts database during reconnect: %w", err)
    }
    
    // Update token with database-specific token
    ic.Token = dbResp.Token
    ic.Client = c
    ic.Ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", ic.Token))
    ic.Cancel = cancel
    ic.IsConnected = true

    config.Info(ic.Logger, "Successfully reconnected to ImmuDB accounts database")
    return nil
}