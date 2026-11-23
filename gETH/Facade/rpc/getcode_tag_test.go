package rpc

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"
)

// codeService records the block eth_getCode asks the backend for.
type codeService struct {
	finalityService
	codeBlock *big.Int
}

func (s *codeService) GetCode(_ context.Context, _ string, block *big.Int) (string, error) {
	s.codeBlock = block
	return "0x", nil
}

// eth_getCode accepts an omitted block tag (some exchange clients send only the
// address) and resolves it to latest; explicit tags keep working and finality
// tags stay refused (TestHeadStateMethodsRefuseFinalityTags).
func TestGetCode_BlockTagOptional(t *testing.T) {
	addr := "0x00000000000000000000000000000000000000a1"
	cases := []struct {
		name   string
		params []any
		want   int64
	}{
		{"omitted", []any{addr}, 10},
		{"latest", []any{addr, "latest"}, 10},
		{"hex", []any{addr, "0x5"}, 5},
	}
	for _, c := range cases {
		svc := &codeService{finalityService: finalityService{head: 10, finalized: 8}}
		resp, err := NewHandlers(svc).Handle(context.Background(), Request{Method: "eth_getCode", Params: c.params, ID: 1})
		if err != nil || resp.Error != nil || resp.Result == nil {
			t.Fatalf("%s: unexpected response %+v, %v", c.name, resp, err)
		}
		var got string
		if err := json.Unmarshal(*resp.Result, &got); err != nil || got != "0x" {
			t.Fatalf("%s: result %q, %v", c.name, got, err)
		}
		if svc.codeBlock == nil || svc.codeBlock.Int64() != c.want {
			t.Fatalf("%s: asked block %v, want %d", c.name, svc.codeBlock, c.want)
		}
	}
	resp, _ := NewHandlers(&codeService{}).Handle(context.Background(), Request{Method: "eth_getCode", Params: []any{}, ID: 2})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("no params: want -32602, got %+v", resp)
	}
}
