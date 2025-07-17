package DB_OPs

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"time"

	"gossipnode/crdt/IBLT"

	"github.com/codenotary/immudb/pkg/api/schema"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Config holds the configuration for the backup process.
type Config struct {
	Address    string
	Username   string
	Password   string
	Database   string
	OutputPath string
	IbltM      int
	IbltK      int
}

func BackupFromIBLT(cfg Config, iblt *IBLT.IBLT) error {
	// 1. Establish gRPC connection
	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log.Printf("Connecting to immudb at %s...", cfg.Address)
	conn, err := grpc.DialContext(ctx, cfg.Address, dialOptions...)
	if err != nil {
		return fmt.Errorf("failed to connect to immudb: %w", err)
	}
	defer conn.Close()
	log.Println("Connection successful.")

	client := schema.NewImmuServiceClient(conn)
	apiCtx := context.Background()

	// 2. Login
	loginResp, err := client.Login(apiCtx, &schema.LoginRequest{
		User:     []byte(cfg.Username),
		Password: []byte(cfg.Password),
	})
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	log.Println("Login successful.")

	md := metadata.New(map[string]string{"authorization": "Bearer " + loginResp.GetToken()})
	apiCtx = metadata.NewOutgoingContext(apiCtx, md)

	// 3. Select database and get the new token
	useDbResp, err := client.UseDatabase(apiCtx, &schema.Database{
		DatabaseName: cfg.Database,
	})
	if err != nil {
		return fmt.Errorf("failed to select database '%s': %w", cfg.Database, err)
	}
	md = metadata.New(map[string]string{"authorization": "Bearer " + useDbResp.GetToken()})
	apiCtx = metadata.NewOutgoingContext(apiCtx, md)
	log.Printf("Successfully selected database: '%s'", cfg.Database)

	// 4. Create the output file
	file, err := os.Create(cfg.OutputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file '%s': %w", cfg.OutputPath, err)
	}
	defer file.Close()

	// 5. Try to peel the IBLT
	positiveKeys, _, peelErr := iblt.ListEntries()
	if peelErr != nil {
		log.Printf("[WARN] IBLT decode failed: %v. Falling back to O(n) scan.", peelErr)
		// Fallback: scan all transactions and match keys
		state, err := client.CurrentState(apiCtx, &emptypb.Empty{})
		if err != nil {
			return fmt.Errorf("failed to get current database state: %w", err)
		}
		lastTxID := state.GetTxId()
		if lastTxID == 0 {
			log.Println("Database is empty. No transactions to back up.")
			return nil
		}
		log.Printf("[Fallback] Exporting %d transactions and filtering by IBLT...", lastTxID)
		for txID := uint64(1); txID <= lastTxID; txID++ {
			tx, err := client.TxById(apiCtx, &schema.TxRequest{Tx: txID})
			if err != nil {
				return fmt.Errorf("failed to retrieve transaction %d by ID: %w", txID, err)
			}
			// Check if any key in this transaction is in the IBLT
			found := false
			for _, entry := range tx.Entries {
				if iblt.Exists(entry.Key) {
					found = true
					break
				}
			}
			if found {
				txData, err := proto.Marshal(tx)
				if err != nil {
					return fmt.Errorf("failed to marshal transaction %d: %w", txID, err)
				}
				if err := binary.Write(file, binary.BigEndian, uint64(len(txData))); err != nil {
					return fmt.Errorf("failed to write length for transaction %d: %w", txID, err)
				}
				if _, err := file.Write(txData); err != nil {
					return fmt.Errorf("failed to write data for transaction %d: %w", txID, err)
				}
			}
			if txID%10 == 0 || txID == lastTxID {
				log.Printf("  ... processed up to transaction ID %d", txID)
			}
		}
		return nil
	}

	log.Printf("Peeling IBLT succeeded. Exporting %d keys...", len(positiveKeys))
	// 6. For each key, try to get its transaction ID and fetch the transaction
	seenTxIDs := make(map[uint64]struct{})
	for _, keyBytes := range positiveKeys {
		// Try to get the entry (Get returns value and Tx)
		getResp, err := client.Get(apiCtx, &schema.KeyRequest{Key: keyBytes})
		if err != nil {
			log.Printf("[WARN] Failed to get key %x: %v", keyBytes, err)
			continue
		}
		txID := getResp.GetTx()
		if txID == 0 {
			log.Printf("[WARN] No transaction ID found for key %x", keyBytes)
			continue
		}
		if _, exists := seenTxIDs[txID]; exists {
			continue // Already written this transaction
		}
		tx, err := client.TxById(apiCtx, &schema.TxRequest{Tx: txID})
		if err != nil {
			log.Printf("[WARN] Failed to fetch transaction %d for key %x: %v", txID, keyBytes, err)
			continue
		}
		txData, err := proto.Marshal(tx)
		if err != nil {
			log.Printf("[WARN] Failed to marshal transaction %d: %v", txID, err)
			continue
		}
		if err := binary.Write(file, binary.BigEndian, uint64(len(txData))); err != nil {
			log.Printf("[WARN] Failed to write length for transaction %d: %v", txID, err)
			continue
		}
		if _, err := file.Write(txData); err != nil {
			log.Printf("[WARN] Failed to write data for transaction %d: %v", txID, err)
			continue
		}
		seenTxIDs[txID] = struct{}{}
	}
	log.Printf("Exported %d unique transactions from IBLT keys.", len(seenTxIDs))
	return nil
}
