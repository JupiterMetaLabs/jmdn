package thebesyncserve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"

	"gossipnode/config"
)

// chain is an in-memory block store of heights 0..tip.
func chain(tip uint64) Provider {
	return Provider{
		latest: func(context.Context) (uint64, error) { return tip, nil },
		block: func(n uint64) (*config.ZKBlock, error) {
			if n > tip {
				return nil, errors.New("key not found")
			}
			return &config.ZKBlock{BlockNumber: n, BlockHash: common.BigToHash(new(big.Int).SetUint64(n + 1000))}, nil
		},
	}
}

func TestClampedTo(t *testing.T) {
	for _, c := range []struct{ from, to, want uint64 }{
		{5, 5, 5},
		{0, 9, 9},
		{0, 1_000_000, MaxBlocksPerRequest - 1},
		{42, math.MaxUint64, 42 + MaxBlocksPerRequest - 1},
	} {
		if got := clampedTo(c.from, c.to); got != c.want {
			t.Fatalf("clampedTo(%d,%d) = %d, want %d", c.from, c.to, got, c.want)
		}
	}
}

func TestProviderStopsAtTip(t *testing.T) {
	p := chain(10)
	if _, found, err := p.RawBlock(11); found || err != nil {
		t.Fatalf("beyond tip: found=%v err=%v", found, err)
	}
	raw, found, err := p.RawBlock(7)
	if err != nil || !found {
		t.Fatalf("block 7: found=%v err=%v", found, err)
	}
	var b config.ZKBlock
	if err := json.Unmarshal(raw, &b); err != nil || b.BlockNumber != 7 {
		t.Fatalf("decoded %+v, %v", b, err)
	}
	if h, err := p.TipHash(10); err != nil || h == "" {
		t.Fatalf("TipHash = %q, %v", h, err)
	}
}

func TestBuildGetBlocks(t *testing.T) {
	s := &Server{Provider: chain(10)}
	if r := s.buildGetBlocks(GetBlocksRequest{From: 5, To: 3}); r.Error == "" {
		t.Fatal("inverted range accepted")
	}
	r := s.buildGetBlocks(GetBlocksRequest{From: 8, To: 50})
	if r.Error != "" || len(r.Blocks) != 3 {
		t.Fatalf("got %d blocks, err %q; want 8..10", len(r.Blocks), r.Error)
	}
}

// End to end over libp2p: the head and getblocks handlers answer a client
// speaking the ThebeSync wire format.
func TestHandlersOverLibp2p(t *testing.T) {
	mn, err := mocknet.FullMeshConnected(2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mn.Close() })
	server, client := mn.Hosts()[0], mn.Hosts()[1]
	srv := &Server{Provider: chain(40)}
	server.SetStreamHandler(HeadProtocol, srv.HeadHandler)
	server.SetStreamHandler(GetBlocksProtocol, srv.GetBlocksHandler)

	call := func(p protocol.ID, req any, resp any) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := client.NewStream(ctx, server.ID(), p)
		if err != nil {
			t.Fatal(err)
		}
		defer func(s network.Stream) { _ = s.Close() }(st)
		b, _ := json.Marshal(req)
		if _, err := st.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(st).ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(line, resp); err != nil {
			t.Fatal(err)
		}
	}

	var head HeadResponse
	call(HeadProtocol, struct{}{}, &head)
	if head.Height != 40 || head.TipBlockHash == "" || head.Error != "" {
		t.Fatalf("head = %+v", head)
	}
	var blocks GetBlocksResponse
	call(GetBlocksProtocol, GetBlocksRequest{From: 1, To: 100}, &blocks)
	if blocks.Error != "" || len(blocks.Blocks) != MaxBlocksPerRequest {
		t.Fatalf("got %d blocks, err %q; want %d", len(blocks.Blocks), blocks.Error, MaxBlocksPerRequest)
	}
	var first config.ZKBlock
	if err := json.Unmarshal(blocks.Blocks[0], &first); err != nil || first.BlockNumber != 1 {
		t.Fatalf("first block %+v, %v", first, err)
	}
}
