package utils

import (
	"fmt"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

//
// -------- Big Int <-> String --------
//

//
// -------- Uint64 <-> String --------
//

//
// -------- Bytes <-> String (hex/base64/utf8) --------
//

//
// -------- Address/DID helpers --------
//

// GenerateReceiptRoot generates a receipt root from a list of receipts
// This follows Ethereum's receipt root calculation using RLP encoding and Keccak-256
func GenerateReceiptRoot(receipts []*config.Receipt) ([]byte, error) {
	if len(receipts) == 0 {
		// Empty receipts list - return empty hash
		return make([]byte, 32), nil
	}

	// RLP encode the receipts
	receiptBytes, err := rlp.EncodeToBytes(receipts)
	if err != nil {
		return nil, fmt.Errorf("failed to RLP encode receipts: %w", err)
	}

	// Calculate Keccak-256 hash of the RLP encoded receipts
	hash := crypto.Keccak256(receiptBytes)

	return hash, nil
}
