package router

import (
	"encoding/hex"
	"fmt"
	"strings"

	"gossipnode/SmartContract/internal/state"
)

// HexToBytes converts hex string to bytes with 0x handling
func HexToBytes(hexStr string) ([]byte, error) {
	// Remove 0x prefix if present
	hexStr = strings.TrimPrefix(hexStr, "0x")

	// Decode hex
	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid hex string: %w", err)
	}

	return decoded, nil
}

// ConvertStateDB safely converts vm.StateDB to our internal state.StateDB interface if possible
func ConvertToInternalStateDB(db interface{}) (state.StateDB, bool) {
	sdb, ok := db.(state.StateDB)
	return sdb, ok
}
