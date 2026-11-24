// TestGetBlockNumberReturnsChainTip verifies that the ThebeDB-backed BlockInfo
// adapter reports the real chain tip — the value FastSync sends to syncing
// peers as AvailabilityResponse.block_height. A stub returning 0 would
// silently break the CatchUp flow.
package nodeinfo_test

import (
	"context"
	"testing"

	"gossipnode/DB_OPs"
	NodeInfo "gossipnode/DB_OPs/Nodeinfo"
	"gossipnode/DB_OPs/store"
)

// fakeHandle overrides only GetLatestBlockNumber; all other ThebeHandle
// methods panic via the nil embedded interface if accidentally called.
type fakeHandle struct {
	store.ThebeHandle
	tip uint64
}

func (f fakeHandle) GetLatestBlockNumber(_ context.Context) (uint64, error) {
	return f.tip, nil
}

func TestGetBlockNumberReturnsChainTip(t *testing.T) {
	const wantTip = uint64(12345)

	DB_OPs.SetGlobalHandle(fakeHandle{tip: wantTip})
	defer DB_OPs.SetGlobalHandle(nil)

	bi := NodeInfo.NewSyncStruct()
	if got := bi.GetBlockNumber(); got != wantTip {
		t.Fatalf("GetBlockNumber: want chain tip %d, got %d — block_height sent to peers would be wrong", wantTip, got)
	}
}

func TestGetBlockNumberIsNotHardcodedZero(t *testing.T) {
	DB_OPs.SetGlobalHandle(fakeHandle{tip: 1})
	defer DB_OPs.SetGlobalHandle(nil)

	if got := NodeInfo.NewSyncStruct().GetBlockNumber(); got == 0 {
		t.Fatal("GetBlockNumber returned 0 with a non-zero tip — stub detected")
	}
}
