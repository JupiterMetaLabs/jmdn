package DB_OPs

import (
	"context"
	"fmt"
	"gossipnode/config"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
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



// NewAccountsClient creates a new ImmuDB client connected to the accounts database
func NewAccountsClient(options ...ImmuClientOption) (*config.ImmuClient, error) {
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

    // Apply custom options
    for _, option := range options {
        option(ic)
    }

    // Establish connection to accounts database
    err = connectToAccountsDB(ic)
    if err != nil {
        config.Close(ic.Logger)
        return nil, err
    }

    return ic, nil
}

// connectToAccountsDB establishes a connection to the accounts database in ImmuDB
func connectToAccountsDB(ic *config.ImmuClient) error {
    config.Info(ic.Logger, "Connecting to ImmuDB at %s:%d", config.DBAddress, config.DBPort)
    
    opts := client.DefaultOptions().
        WithAddress(config.DBAddress).
        WithPort(config.DBPort)

    c, err := client.NewImmuClient(opts)
    if err != nil {
        return fmt.Errorf("failed to create client: %w", err)
    }

    // Create context with timeout
    ctx, cancel := context.WithTimeout(ic.BaseCtx, config.RequestTimeout)
    defer func() {
        if !ic.IsConnected {
            cancel()
            c.Disconnect()
        }
    }()
    
    // Login to immudb
    config.Info(ic.Logger, "Authenticating with ImmuDB")
    lr, err := c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
    if err != nil {
        return fmt.Errorf("login failed: %w", err)
    }

    // Store token for reconnection
    ic.Token = lr.Token
    
    // Add auth token to context
    md := metadata.Pairs("authorization", lr.Token)
    ctx = metadata.NewOutgoingContext(ctx, md)
    
    // Check if accounts database exists
    config.Info(ic.Logger, "Checking if database exists: %s", config.AccountsDBName)
    databaseList, err := c.DatabaseList(ctx)
    if err != nil {
        return fmt.Errorf("failed to get database list: %w", err)
    }
    
    // Check if accounts database exists in the list
    databaseExists := false
    for _, db := range databaseList.Databases {
        if db.DatabaseName == config.AccountsDBName {
            databaseExists = true
            break
        }
    }
    
    // Create accounts database if it doesn't exist
    if !databaseExists {
		config.Info(ic.Logger, "Creating database: %s", config.AccountsDBName)
        err = c.CreateDatabase(ctx, &schema.DatabaseSettings{
			DatabaseName: config.AccountsDBName,
		})
        if err != nil {
            return fmt.Errorf("failed to create database %s: %w", config.AccountsDBName, err)
        }
        config.Info(ic.Logger, "Database created successfully: %s", config.AccountsDBName)
    } else {
        config.Info(ic.Logger, "Database already exists: %s", config.AccountsDBName)
    }
    
    // Select accounts database
    config.Info(ic.Logger, "Selecting database: %s", config.AccountsDBName)
    _, err = c.UseDatabase(ctx, &schema.Database{DatabaseName: config.AccountsDBName})
    if err != nil {
        return fmt.Errorf("failed to use database %s: %w", config.AccountsDBName, err)
    }

    ic.Client = c
    ic.Ctx = ctx
    ic.Cancel = cancel
    ic.IsConnected = true
    config.Info(ic.Logger, "Successfully connected to ImmuDB accounts database")

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
    
    // Attempt to connect again
    return connectToAccountsDB(ic)
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
    
    // Store using SafeCreate for cryptographic verification
    key := fmt.Sprintf("did:%s", didDoc.DID)
    return SafeCreate(ic, key, didDoc)
}

// GetDID retrieves a DID document from the accounts database
func GetDID(ic *config.ImmuClient, did string) (*DIDDocument, error) {
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