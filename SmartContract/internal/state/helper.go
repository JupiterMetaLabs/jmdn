package state

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie/utils"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/crypto"
)

// Auxiliary Methods and Implementation Details

// CreateContract creates a new contract and returns its address
func (s *ImmuStateDB) CreateContract(creator common.Address) {
	// Get and increment nonce
	nonce := s.GetNonce(creator)
	s.SetNonce(creator, nonce+1)

	// Generate contract address
	addr := crypto.CreateAddress(creator, nonce)

	// Create account using the method already defined in statedb.go
	s.CreateAccount(addr)
}

// queueDBOperation adds an operation to the transaction batch
func (s *ImmuStateDB) queueDBOperation(key string, value []byte, isDelete bool) {
	s.txMutex.Lock()
	defer s.txMutex.Unlock()

	op := &dbOperation{
		key:      key,
		value:    value,
		isDelete: isDelete,
	}

	s.txOps = append(s.txOps, op)

	// If we've reached the batch size, commit the batch
	if len(s.txOps) >= dbBatchSize {
		s.commitBatch()
	}
}

// commitBatch writes all queued operations to the database
func (s *ImmuStateDB) commitBatch() error {
	if len(s.txOps) == 0 {
		return nil
	}

	// Start a database transaction
	err := DB_OPs.Transaction(s.dbClient.Client, func(tx *config.ImmuTransaction) error {
		for _, op := range s.txOps {
			var err error
			if op.isDelete {
				// For now, write empty bytes to signify deletion
				// This usually requires application-level handling of "empty" as "deleted"
				err = DB_OPs.Set(tx, op.key, []byte{})
			} else {
				err = DB_OPs.Set(tx, op.key, op.value)
			}

			if err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to commit batch: %w", err)
	}

	// Reset batch
	s.txOps = s.txOps[:0]
	return nil
}

// AddLog adds a log to the logs collection
func (s *ImmuStateDB) AddLog(log *types.Log) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.logs = append(s.logs, log)
	s.logSize += uint(len(log.Data))
	for _, topic := range log.Topics {
		s.logSize += uint(len(topic))
	}
}

// GetLogs returns all collected logs
func (s *ImmuStateDB) GetLogs(hash common.Hash, blockNumber uint64) []*types.Log {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	cpy := make([]*types.Log, len(s.logs))
	for i, log := range s.logs {
		// Update log with block details
		cpy[i] = &types.Log{
			Address:     log.Address,
			Topics:      log.Topics,
			Data:        log.Data,
			BlockNumber: blockNumber,
			TxHash:      hash,
			TxIndex:     uint(i),
			BlockHash:   hash,
			Index:       uint(i),
			Removed:     false,
		}
	}

	return cpy
}

// Suicide methods

// Suicide marks the given account as suicided
func (s *ImmuStateDB) Suicide(addr common.Address) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	stateObject := s.getStateObject(addr)
	if stateObject == nil {
		return false
	}

	s.suicided[addr] = true
	stateObject.deleted = true

	return true
}

// HasSuicided returns true if the account was suicided
func (s *ImmuStateDB) HasSuicided(addr common.Address) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.suicided[addr]
}

// Refund methods

// AddRefund adds gas to the refund counter
func (s *ImmuStateDB) AddRefund(gas uint64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.refund += gas
}

// SubRefund removes gas from the refund counter
func (s *ImmuStateDB) SubRefund(gas uint64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if gas > s.refund {
		s.refund = 0
	} else {
		s.refund -= gas
	}
}

// GetRefund returns the current value of the refund counter
func (s *ImmuStateDB) GetRefund() uint64 {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.refund
}

// SelfDestruct methods (EIP-6780)

func (s *ImmuStateDB) HasSelfDestructed(addr common.Address) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.suicided[addr]
}

func (s *ImmuStateDB) SelfDestruct(addr common.Address) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if _, exists := s.suicided[addr]; exists {
		return
	}
	s.suicided[addr] = true
}

func (s *ImmuStateDB) Selfdestruct6780(addr common.Address) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.suicided[addr] = true
	obj := s.getOrCreateStateObject(addr)
	obj.deleted = true
	s.stateObjectsDirty[addr] = struct{}{}
}

func (s *ImmuStateDB) PointCache() *utils.PointCache {
	return s.pointCache
}

// Witness returns the witness for statutory execution
func (s *ImmuStateDB) Witness() *stateless.Witness {
	s.witnessMutex.RLock()
	defer s.witnessMutex.RUnlock()
	return s.witness
}

// AddPreimage adds a preimage to the database
func (s *ImmuStateDB) AddPreimage(hash common.Hash, preimage []byte) {
	// Optimization for debugging/tracing - no-op for minimal implementation
}

// Preimages returns a map of preimages
func (s *ImmuStateDB) Preimages() map[common.Hash][]byte {
	return make(map[common.Hash][]byte)
}

// AddAddressToAccessList adds an address to the access list
func (s *ImmuStateDB) AddAddressToAccessList(addr common.Address) {
	if s.accessList == nil {
		s.accessList = &accessList{
			addresses: make(map[common.Address]struct{}),
			slots:     make(map[common.Address]map[common.Hash]struct{}),
		}
	}
	s.accessList.addresses[addr] = struct{}{}
}

// AddSlotToAccessList adds the specified contract slot to the access list
func (s *ImmuStateDB) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	if s.accessList == nil {
		s.accessList = &accessList{
			addresses: make(map[common.Address]struct{}),
			slots:     make(map[common.Address]map[common.Hash]struct{}),
		}
	}

	if _, ok := s.accessList.slots[addr]; !ok {
		s.accessList.slots[addr] = make(map[common.Hash]struct{})
	}

	s.accessList.slots[addr][slot] = struct{}{}
	s.accessList.addresses[addr] = struct{}{}
}

// AddressInAccessList returns whether an address is in the access list
func (s *ImmuStateDB) AddressInAccessList(addr common.Address) bool {
	if s.accessList == nil {
		return false
	}
	_, ok := s.accessList.addresses[addr]
	return ok
}

// SlotInAccessList returns whether the specified slot of an address is in the access list
func (s *ImmuStateDB) SlotInAccessList(addr common.Address, slot common.Hash) (bool, bool) {
	addrPresent := false
	if _, ok := s.accessList.addresses[addr]; ok {
		addrPresent = true
	}

	slotPresent := false
	if slots, ok := s.accessList.slots[addr]; ok {
		if _, ok := slots[slot]; ok {
			slotPresent = true
		}
	}

	return slotPresent, addrPresent
}

// Prepare implements the StateDB interface
func (s *ImmuStateDB) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses types.AccessList) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if len(s.txOps) > 0 {
		s.txOps = s.txOps[:0]
	}

	s.accessList = &accessList{
		addresses: make(map[common.Address]struct{}),
		slots:     make(map[common.Address]map[common.Hash]struct{}),
	}

	s.accessList.addresses[sender] = struct{}{}

	if rules.IsByzantium {
		s.accessList.addresses[coinbase] = struct{}{}
	}

	if dest != nil {
		s.accessList.addresses[*dest] = struct{}{}
	}

	for _, addr := range precompiles {
		s.accessList.addresses[addr] = struct{}{}
	}

	if txAccesses != nil {
		for _, tuple := range txAccesses {
			s.AddAddressToAccessList(tuple.Address)
			for _, key := range tuple.StorageKeys {
				s.AddSlotToAccessList(tuple.Address, key)
			}
		}
	}
}
