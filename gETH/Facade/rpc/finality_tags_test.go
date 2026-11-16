package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/messaging"
)

// finalityService answers the head, the finalized height and certified-block
// reports; any other call panics (nil embedded interface).
type finalityService struct {
	Service.Service
	head, finalized uint64
	finalizedErr    error
	askedBlock      *big.Int
}

var errStopAfterLookup = errors.New("stop after the block lookup")

func (s *finalityService) BlockNumber(context.Context) (*big.Int, error) {
	return new(big.Int).SetUint64(s.head), nil
}

func (s *finalityService) FinalizedBlockNumber(context.Context) (*big.Int, error) {
	if s.finalizedErr != nil {
		return nil, s.finalizedErr
	}
	return new(big.Int).SetUint64(s.finalized), nil
}

func (s *finalityService) BlockByNumber(_ context.Context, num *big.Int, _ bool) (*Types.Block, error) {
	s.askedBlock = num
	return nil, errStopAfterLookup
}

func (s *finalityService) CertifiedBlock(_ context.Context, height uint64) (*messaging.CertifiedBlockReport, error) {
	return &messaging.CertifiedBlockReport{Number: height, Certified: true, Finalized: height <= s.finalized}, nil
}

// "finalized" and "safe" resolve to the certified-with-certified-child height,
// never the head.
func TestGetBlockByNumber_FinalizedIsNotTheHead(t *testing.T) {
	for _, tag := range []string{"finalized", "safe"} {
		svc := &finalityService{head: 10, finalized: 8}
		h := NewHandlers(svc)
		_, _ = h.Handle(context.Background(), Request{Method: "eth_getBlockByNumber", Params: []any{tag, false}, ID: 1})
		if svc.askedBlock == nil || svc.askedBlock.Uint64() != 8 {
			t.Fatalf("%q: looked up block %v, want the finalized height 8 (head is 10)", tag, svc.askedBlock)
		}
	}
}

// With no finalized block the tag is an error, not a fallback to the head.
func TestGetBlockByNumber_NoFinalizedBlockIsAnError(t *testing.T) {
	svc := &finalityService{head: 10, finalizedErr: messaging.ErrNotFinalized}
	resp, _ := NewHandlers(svc).Handle(context.Background(), Request{Method: "eth_getBlockByNumber", Params: []any{"finalized", false}, ID: 1})
	if resp.Error == nil || svc.askedBlock != nil {
		t.Fatalf("want an error and no block lookup, got %+v (looked up %v)", resp, svc.askedBlock)
	}
}

// eth_getLogs' toBlock "finalized" resolves to the finalized height.
func TestResolveFilterRange_Finalized(t *testing.T) {
	svc := &finalityService{head: 10, finalized: 8}
	q := &Types.FilterQuery{}
	if err := resolveFilterRange(context.Background(), svc, map[string]any{"fromBlock": "earliest", "toBlock": "finalized"}, q); err != nil {
		t.Fatal(err)
	}
	if q.ToBlock == nil || q.ToBlock.Uint64() != 8 {
		t.Fatalf("toBlock = %v, want 8", q.ToBlock)
	}
}

func TestGetCertifiedBlock(t *testing.T) {
	svc := &finalityService{head: 10, finalized: 8}
	resp, err := NewHandlers(svc).Handle(context.Background(), Request{Method: "jmdt_getCertifiedBlock", Params: []any{"0x5"}, ID: 1})
	if err != nil || resp.Error != nil || resp.Result == nil {
		t.Fatalf("unexpected response %+v, %v", resp, err)
	}
	var got messaging.CertifiedBlockReport
	if err := json.Unmarshal(*resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Number != 5 || !got.Certified || !got.Finalized {
		t.Fatalf("got %+v", got)
	}
	bad, _ := NewHandlers(svc).Handle(context.Background(), Request{Method: "jmdt_getCertifiedBlock", Params: []any{"latest"}, ID: 2})
	if bad.Error == nil {
		t.Fatal("a tag must be refused: the method answers about one explicit height")
	}
}
