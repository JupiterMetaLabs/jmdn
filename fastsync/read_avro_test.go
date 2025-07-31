package fastsync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linkedin/goavro/v2"
	"github.com/stretchr/testify/require"
)

// A sample struct that matches the data you expect in the AVRO file.
// Adjust this to match your actual data structure (e.g., DIDDocument).
type TestRecord struct {
	DID       string `json:"did"`
	PublicKey string `json:"publicKey"`
	Balance   string `json:"balance"`
}

func TestReadAccountsAvro(t *testing.T) {
	// This test assumes an 'accounts.avro' file exists in the .temp directory.
	// You might need to create a sample one for testing purposes.
	dbPath := filepath.Join(AVRO_FILE_PATH, "accounts.avro")

	file, err := os.Open(dbPath)
	if os.IsNotExist(err) {
		t.Skipf("skipping test, avro file not found at %s", dbPath)
	}
	require.NoError(t, err, "failed to open avro file")
	defer file.Close()

	ocfReader, err := goavro.NewOCFReader(file)
	require.NoError(t, err, "failed to create avro ocf reader")

	var recordCount int
	for ocfReader.Scan() {
		record, err := ocfReader.Read()
		require.NoError(t, err, "failed to read avro record")
		recordCount++

		recordMap, ok := record.(map[string]interface{})
		require.True(t, ok, "expected record to be a map[string]interface{}")

		key, keyOk := recordMap["Key"].(string)
		value, valueOk := recordMap["Value"].(string)

		require.True(t, keyOk, "record missing 'Key' field")
		require.True(t, valueOk, "record missing 'Value' field")

		t.Logf("Read record: Key=%s \nValue=%s\n", key, value)
	}

	require.Greater(t, recordCount, 0, "avro file should not be empty")
	t.Logf("Successfully read %d records from %s", recordCount, dbPath)
}
