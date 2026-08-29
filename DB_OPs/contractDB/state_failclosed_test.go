package contractDB

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// UNTESTED-LOCALLY: no Go toolchain is available in this environment, so this
// test was written by static reasoning and NOT executed. Validate with:
//
//	go test ./DB_OPs/contractDB/ -run TestApplyPathFailClosed -v
//
// It guards the EVM-A16 fail-closed invariant for the storage/code read sinks
// (state_object.go loadStorage + getCode): a GENUINE backend read error must set
// the sticky DBError() (so the executor aborts the block), while a legitimately
// absent slot/code must NOT (returns zero/nil, DBError() stays clean — normal EVM
// behavior). Mirrors the balance/nonce fail-closed already proven for the account
// path in state_accessors.go getStateObject.

// failRepo is a fake StateRepository whose storage/code reads can be forced to
// return a genuine backend error (the not-found case is modelled by the repo
// contract of returning a zero value with a nil error).
type failRepo struct {
	storageErr error
	codeErr    error
}

func (f *failRepo) NewBatch() StateBatch { return nil }

func (f *failRepo) GetCode(_ context.Context, _ common.Address) ([]byte, error) {
	if f.codeErr != nil {
		return nil, f.codeErr
	}
	return nil, nil // absent code: NOT an error (KVStore contract: not-found = nil,nil)
}

func (f *failRepo) GetStorage(_ context.Context, _ common.Address, _ common.Hash) (common.Hash, error) {
	if f.storageErr != nil {
		return common.Hash{}, f.storageErr
	}
	return common.Hash{}, nil // absent slot: NOT an error
}

func (f *failRepo) GetStorageMetadata(_ context.Context, _ common.Address, _ common.Hash) (*StorageMetadata, error) {
	return nil, nil
}
func (f *failRepo) GetNonce(_ context.Context, _ common.Address) (uint64, error) { return 0, nil }
func (f *failRepo) GetContractMetadata(_ context.Context, _ common.Address) ([]byte, error) {
	return nil, nil
}
func (f *failRepo) GetReceipt(_ context.Context, _ common.Hash) ([]byte, error) { return nil, nil }

func TestApplyPathFailClosed(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	key := common.HexToHash("0x01")

	t.Run("storage_read_error_fails_closed", func(t *testing.T) {
		injected := errors.New("badger: transient read failure")
		cdb := NewContractDB(nil, &failRepo{storageErr: injected})

		// A read on a real backend error must (a) return the zero slot so unwind has
		// no nil deref, and (b) set the sticky DBError so the executor aborts.
		if got := cdb.GetState(addr, key); got != (common.Hash{}) {
			t.Fatalf("GetState on read error: want zero hash, got %s", got.Hex())
		}
		dberr := cdb.DBError()
		if dberr == nil {
			t.Fatal("DBError() is nil after a genuine storage read error: apply path is FAIL-OPEN (silent chain-split hole)")
		}
		if !errors.Is(dberr, injected) {
			t.Fatalf("DBError() = %v, want it to wrap the injected error", dberr)
		}
	})

	t.Run("code_read_error_fails_closed", func(t *testing.T) {
		injected := errors.New("badger: transient read failure")
		cdb := NewContractDB(nil, &failRepo{codeErr: injected})

		if code := cdb.GetCode(addr); code != nil {
			t.Fatalf("GetCode on read error: want nil, got %d bytes", len(code))
		}
		if cdb.DBError() == nil {
			t.Fatal("DBError() is nil after a genuine code read error: apply path is FAIL-OPEN")
		}
	})

	t.Run("absent_slot_and_code_do_not_fail", func(t *testing.T) {
		// Legitimately absent storage/code (zero value, nil error) is NORMAL EVM
		// behavior and must NOT trip the fail-closed sticky error.
		cdb := NewContractDB(nil, &failRepo{})

		if got := cdb.GetState(addr, key); got != (common.Hash{}) {
			t.Fatalf("GetState on absent slot: want zero hash, got %s", got.Hex())
		}
		if code := cdb.GetCode(addr); code != nil {
			t.Fatalf("GetCode on absent code: want nil, got %d bytes", len(code))
		}
		if err := cdb.DBError(); err != nil {
			t.Fatalf("DBError() = %v, want nil: absent slot/code must not fail closed", err)
		}
	})
}
