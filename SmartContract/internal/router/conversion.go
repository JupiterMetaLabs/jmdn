package router

import (
	"encoding/hex"
	"fmt"
	"strings"

	contractDB "gossipnode/DB_OPs/contractDB"
)

// HexToBytes converts a hex string (with or without 0x prefix) to bytes.
func HexToBytes(hexStr string) ([]byte, error) {
	hexStr = strings.TrimPrefix(hexStr, "0x")
	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid hex string: %w", err)
	}
	return decoded, nil
}

// ConvertToInternalStateDB safely casts a vm.StateDB to contractDB.StateDB.
func ConvertToInternalStateDB(db interface{}) (contractDB.StateDB, bool) {
	sdb, ok := db.(contractDB.StateDB)
	return sdb, ok
}
