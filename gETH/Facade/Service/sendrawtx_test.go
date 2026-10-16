package Service

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
)

// eth_sendRawTransaction accepts RLP/typed-envelope transactions only; a
// JSON-encoded transaction is rejected before any validation runs.
func TestSendRawTx_RejectsJSONPayload(t *testing.T) {
	s := &ServiceImpl{}
	raw := "0x" + hex.EncodeToString([]byte(`{"hash":"0x01"}`))
	_, err := s.SendRawTx(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "RLP") {
		t.Fatalf("want an RLP parse error, got %v", err)
	}
}
