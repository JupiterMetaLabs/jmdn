package Block

import (
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// Hash returns the Keccak256 hash of the transaction
func Hash(tx *config.Transaction) (common.Hash, error) {

	encodedTx, err := rlp.EncodeToBytes(tx)
	if err != nil {
		return common.Hash{}, err
	}

	hash := crypto.Keccak256Hash(encodedTx)
	return hash, nil
}
