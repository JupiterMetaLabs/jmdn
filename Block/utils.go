package Block
import (
    "github.com/ethereum/go-ethereum/core/types"
    "gossipnode/config"
    "github.com/ethereum/go-ethereum/crypto"
    "github.com/ethereum/go-ethereum/rlp"
)
// Helper function to convert our AccessList type to go-ethereum's types.AccessList
func convertAccessList(accessList config.AccessList) types.AccessList {
    result := make(types.AccessList, len(accessList))
    for i, tuple := range accessList {
        result[i] = types.AccessTuple{
            Address:    tuple.Address,
            StorageKeys: tuple.StorageKeys,
        }
    }
    return result
}

// Hash returns the Keccak256 hash of the transaction
func Hash(tx *config.ZKBlockTransaction) (string, error) {

    encodedTx, err := rlp.EncodeToBytes(tx)
    if err != nil {
        return "", err
    }
    
    hash := crypto.Keccak256Hash(encodedTx)
    return hash.String(), nil
}
