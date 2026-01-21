package state

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/DB_OPs"
)

// GetCode returns the contract code associated with this account
func (s *ImmuStateDB) GetCode(addr common.Address) []byte {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	addrBytes := addr.Bytes()

	if s.witness != nil {
		s.witnessMutex.Lock()
		s.witness.AddCode(addrBytes)
		s.witnessMutex.Unlock()
	}
	stateObject := s.getStateObject(addr)
	if stateObject == nil {
		return nil
	}

	if len(stateObject.account.Code) > 0 {
		return stateObject.account.Code
	}

	// Try loading code from database if not in memory
	if s.dbClient != nil {
		codeKey := getDBKey(prefixCode, addr)
		code, err := DB_OPs.Read(s.dbClient, codeKey)
		if err != nil {
			return nil
		}

		// Update in-memory state
		stateObject.account.Code = code
		return code
	}

	return nil
}

// GetCodeSize returns the size of the contract code
func (s *ImmuStateDB) GetCodeSize(addr common.Address) int {
	code := s.GetCode(addr)
	return len(code)
}

// GetCodeHash returns the code hash of the given account
func (s *ImmuStateDB) GetCodeHash(addr common.Address) common.Hash {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	stateObject := s.getStateObject(addr)
	if stateObject == nil {
		return common.Hash{}
	}
	return stateObject.account.CodeHash
}

// SetCode sets the contract code
func (s *ImmuStateDB) SetCode(addr common.Address, code []byte) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	stateObject := s.getOrCreateStateObject(addr)
	stateObject.account.Code = code
	stateObject.account.CodeHash = crypto.Keccak256Hash(code)
	stateObject.isDirty = true
	s.stateObjectsDirty[addr] = struct{}{}
}

// CodeIterator returns an iterator for all contracts in the state
func (s *ImmuStateDB) CodeIterator() *CodeIterator {
	return &CodeIterator{
		stateDB: s,
		addrs:   make([]common.Address, 0),
		index:   0,
	}
}

// Next advances the iterator to the next contract
func (it *CodeIterator) Next() bool {
	// Lazy initialization of addresses
	if it.index == 0 && len(it.addrs) == 0 {
		it.stateDB.mutex.RLock()
		for addr, obj := range it.stateDB.stateObjects {
			if len(obj.account.Code) > 0 {
				it.addrs = append(it.addrs, addr)
			}
		}
		it.stateDB.mutex.RUnlock()
	}

	it.index++
	return it.index <= len(it.addrs)
}

// Address returns the address of the current contract
func (it *CodeIterator) Address() common.Address {
	if it.index <= 0 || it.index > len(it.addrs) {
		return common.Address{}
	}
	return it.addrs[it.index-1]
}

// Code returns the code of the current contract
func (it *CodeIterator) Code() []byte {
	if it.index <= 0 || it.index > len(it.addrs) {
		return nil
	}
	return it.stateDB.GetCode(it.addrs[it.index-1])
}
