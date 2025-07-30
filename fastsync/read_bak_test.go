package fastsync

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"google.golang.org/protobuf/proto"
)

type DIDDocumentTest struct {
	DID       string `json:"did"`
	PublicKey string `json:"publicKey"`
	Balance   string `json:"balance"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	Nonce     int64  `json:"nonce"`
}

func TestReadAccountsBak(t *testing.T) {
	dbPath := ".temp/accounts.bak"
	file, err := os.Open(dbPath)
	if err != nil {
		t.Fatalf("cannot open backup file: %v", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	txCount := 0
	success := 0
	fail := 0

	for {
		var length uint64
		err := binary.Read(reader, binary.BigEndian, &length)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("error reading length prefix: %v", err)
		}

		txData := make([]byte, length)
		if _, err := io.ReadFull(reader, txData); err != nil {
			t.Fatalf("error reading tx data: %v", err)
		}

		tx := &schema.Tx{}
		if err := proto.Unmarshal(txData, tx); err != nil {
			t.Logf("❌ Failed to decode protobuf: %v\n", err)
			continue
		}

		for i, entry := range tx.Entries {
			t.Logf("\nTx %d Entry %d", txCount, i)
			t.Logf("Key: %s", entry.Key)

			if len(entry.Value) == 0 {
				t.Log("⚠️  Empty value")
				fail++
				continue
			}

			t.Logf("Raw value (string): %s", string(entry.Value))
			t.Logf("Raw value (hex): %x", entry.Value)

			if json.Valid(entry.Value) {
				var doc DIDDocumentTest
				if err := json.Unmarshal(entry.Value, &doc); err == nil {
					t.Log("✅ Parsed DID Document:")
					t.Logf("  DID:        %s", doc.DID)
					t.Logf("  PublicKey:  %s", doc.PublicKey)
					t.Logf("  Balance:    %s", doc.Balance)
					t.Logf("  CreatedAt:  %s", time.Unix(doc.CreatedAt, 0).Format(time.RFC3339))
					t.Logf("  UpdatedAt:  %s", time.Unix(doc.UpdatedAt, 0).Format(time.RFC3339))
					t.Logf("  Nonce:      %d", doc.Nonce)
					success++
				} else {
					t.Logf("❌ JSON Unmarshal error: %v", err)
					fail++
				}
			} else {
				t.Log("❌ Not valid JSON")
				fail++
			}
		}
		txCount++
	}

	t.Log("\n=== Summary ===")
	t.Logf("Transactions: %d", txCount)
	t.Logf("Success:      %d", success)
	t.Logf("Failures:     %d", fail)
}
