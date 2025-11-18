package DB_OPs

import (
	"encoding/json"
	"fmt"
	"strings"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// AddressDirection indicates whether the transaction entry is incoming or outgoing
type AddressDirection string

const (
	// AddressDirectionFrom denotes a debit/outgoing transaction for the address
	AddressDirectionFrom AddressDirection = "from"
	// AddressDirectionTo denotes a credit/incoming transaction for the address
	AddressDirectionTo AddressDirection = "to"
)

// AddressTxPointer is a lightweight payload stored for each address → transaction entry
type AddressTxPointer struct {
	Address     string           `json:"address"`
	TxHash      string           `json:"tx_hash"`
	BlockNumber uint64           `json:"block_number"`
	TxIndex     int              `json:"tx_index"`
	Direction   AddressDirection `json:"direction"`
}

// indexTransactionAddresses stores address-scoped entries for a given transaction
func IndexTransactionAddresses(mainDBClient *config.PooledConnection, blockNumber uint64, tx *config.Transaction, txIndex int) error {
	if tx == nil || mainDBClient == nil {
		return fmt.Errorf("invalid arguments to indexTransactionAddresses")
	}

	if err := createAddressTxEntry(mainDBClient, tx.From, blockNumber, tx, txIndex, AddressDirectionFrom); err != nil {
		return err
	}

	if tx.To != nil {
		if err := createAddressTxEntry(mainDBClient, tx.To, blockNumber, tx, txIndex, AddressDirectionTo); err != nil {
			return err
		}
	}

	return nil
}

func createAddressTxEntry(mainDBClient *config.PooledConnection, address *common.Address, blockNumber uint64, tx *config.Transaction, txIndex int, direction AddressDirection) error {
	if address == nil || tx == nil {
		return nil
	}

	normalizedAddr := normalizeAddress(*address)
	if normalizedAddr == "" {
		return nil
	}

	txHash := strings.ToLower(tx.Hash.Hex())
	key := fmt.Sprintf("%s%s:%d:%s", PREFIX_ADDR_TX, normalizedAddr, blockNumber, txHash)

	payload := AddressTxPointer{
		Address:     normalizedAddr,
		TxHash:      txHash,
		BlockNumber: blockNumber,
		TxIndex:     txIndex,
		Direction:   direction,
	}

	value, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal address transaction pointer: %w", err)
	}

	if err := Create(mainDBClient, key, value); err != nil {
		return fmt.Errorf("failed to store address transaction entry: %w", err)
	}

	return nil
}

func normalizeAddress(address common.Address) string {
	return strings.ToLower(address.Hex())
}
