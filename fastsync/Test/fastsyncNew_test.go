package fastsync_test

import (
	"testing"
	"gossipnode/fastsync"
)

func TestMakeHashMap_Default(t *testing.T) {
	fs := fastsync.NewFastSync(nil, nil, nil)
	MAP, err := fs.MakeHashMap_Default()
	if err != nil {
		t.Fatalf("failed to make HashMap: %v", err)
	}
	if MAP.Size() != 0 {
		t.Errorf("expected HashMap to have 0 keys, got %d", MAP.Size())
	}
}

func TestMakeHashMap_Accounts(t *testing.T) {
	fs := fastsync.NewFastSync(nil, nil, nil)
	MAP, err := fs.MakeHashMap_Accounts()
	if err != nil {
		t.Fatalf("failed to make HashMap: %v", err)
	}
	if MAP.Size() != 0 {
		t.Errorf("expected HashMap to have 0 keys, got %d", MAP.Size())
	}
}