package DB_OPs

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	hashmap "gossipnode/crdt/HashMap"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/linkedin/goavro/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Config holds the configuration for the backup process.
type Config struct {
	Address    string
	Username   string
	Password   string
	Database   string
	OutputPath string
}

const (
	compression = "snappy"
)

func BackupFromHashMap(cfg Config, MAP *hashmap.HashMap) error {
	// ———————————————————————————————————————————————
	// 1. Dial & login
	// ———————————————————————————————————————————————
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, cfg.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer conn.Close()

	client := schema.NewImmuServiceClient(conn)
	apiCtx := context.Background()
	login, err := client.Login(apiCtx, &schema.LoginRequest{
		User:     []byte(cfg.Username),
		Password: []byte(cfg.Password),
	})
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	apiCtx = metadata.NewOutgoingContext(apiCtx,
		metadata.Pairs("authorization", "Bearer "+login.GetToken()),
	)
	useDb, err := client.UseDatabase(apiCtx, &schema.Database{DatabaseName: cfg.Database})
	if err != nil {
		return fmt.Errorf("use database failed: %w", err)
	}
	apiCtx = metadata.NewOutgoingContext(apiCtx,
		metadata.Pairs("authorization", "Bearer "+useDb.GetToken()),
	)
	log.Printf("Connected to %s/%s", cfg.Address, cfg.Database)

	// ———————————————————————————————————————————————
	// 2. Prepare Avro OCF writer
	// ———————————————————————————————————————————————
	// Schema: simple Key, Value (both strings)
	schemaJSON := `{
      "namespace": "fastsync",
      "type": "record",
      "name": "KeyValue",
      "fields": [
        {"name":"Key",   "type":"string"},
        {"name":"Value", "type":"string"},
        {"name":"Database", "type":"string"}
      ]
    }`
	codec, err := goavro.NewCodec(schemaJSON)
	if err != nil {
		return fmt.Errorf("avro codec init: %w", err)
	}

	// Detect if we need to append or write fresh
	exists := false
	if _, err := os.Stat(cfg.OutputPath); err == nil {
		exists = true
	}

	var avroFile *os.File
	if exists {
		avroFile, err = os.OpenFile(cfg.OutputPath, os.O_APPEND|os.O_WRONLY, 0644)
	} else {
		avroFile, err = os.Create(cfg.OutputPath)
	}
	if err != nil {
		return fmt.Errorf("open avro file: %w", err)
	}
	defer avroFile.Close()

	ocfWriter, err := goavro.NewOCFWriter(goavro.OCFConfig{
		W:               avroFile,
		Codec:           codec,
		CompressionName: compression,
	})

	if err != nil {
		return fmt.Errorf("avro writer init: %w", err)
	}

	// ———————————————————————————————————————————————
	// 3. Peel the HashMap & stream into Avro
	// ———————————————————————————————————————————————
	keys := MAP.Keys()
	log.Printf("Exporting %d keys into Avro → %s", len(keys), cfg.OutputPath)

	for _, key := range keys {
		resp, err := client.Get(apiCtx, &schema.KeyRequest{Key: []byte(key)})
		if err != nil {
			log.Printf("[WARN] Get(%s): %v", key, err)
			continue
		}
		record := map[string]interface{}{
			"Key":      key,
			"Value":    string(resp.Value),
			"Database": cfg.Database,
		}
		if err := ocfWriter.Append([]interface{}{record}); err != nil {
			log.Printf("[WARN] Avro append: %v", err)
			continue
		}
	}

	log.Printf("Avro export complete.")
	return nil
}
