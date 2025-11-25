package Utils

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"testing"
	"time"

	"gossipnode/config"

	"github.com/stretchr/testify/assert"
)

var URL = "http://34.174.102.18:8090/api/block/number/2185"

func GetBlockFromURL() (*config.ZKBlock, error) {
	client := http.Client{
		Timeout: 10 * time.Second, // avoid hanging forever
	}

	resp, err := client.Get(URL)
	if err != nil {
		return nil, fmt.Errorf("http get error: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body error: %w", err)
	}

	var blockData config.ZKBlock
	if err := json.Unmarshal(body, &blockData); err != nil {
		return nil, fmt.Errorf("json unmarshal error: %w", err)
	}

	return &blockData, nil
}


func TestCalculateBaseFee(t *testing.T) {
	// Fetch block data from the API
	blockData, err := GetBlockFromURL()
	if err != nil {
		t.Fatalf("Failed to fetch block data: %v", err)
	}

	// Print the block data
	fmt.Println("Block Transactions:", len(blockData.Transactions))

	// Calculate the base fee
	start := time.Now()
	baseFee := calculateBaseFee(blockData)
	end := time.Now()
	fmt.Println("Time taken to calculate base fee:", end.Sub(start))

	// Debugging
	baseFeeStr := new(big.Int).SetBytes(baseFee).String()
	fmt.Println("Base fee:", baseFeeStr)

	// Verify the base fee is not empty
	assert.NotNil(t, baseFee, "Base fee should not be nil")
	assert.NotEmpty(t, baseFee, "Base fee should not be empty")
}