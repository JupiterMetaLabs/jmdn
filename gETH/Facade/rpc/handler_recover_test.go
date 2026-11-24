package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"gossipnode/gETH/Facade/Service"
)

// panicService panics on every call (nil embedded interface) — stands in for
// any handler bug reachable from a JSON-RPC request.
type panicService struct{ Service.Service }

// A panic inside a batch element is recovered and that element answers with a
// JSON-RPC internal error.
func TestHandleJSONRPC_BatchHandlerPanicRecovered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `[{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":["0x7b2268617368223a2230783031227d"]},
	          {"jsonrpc":"2.0","id":2,"method":"eth_sendRawTransaction","params":["0x01"]}]`
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))

	s := &HTTPServer{h: NewHandlers(panicService{})}
	s.handleJSONRPC(c)

	var resps []Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resps); err != nil {
		t.Fatalf("batch response is not a JSON array: %v (%s)", err, rec.Body.String())
	}
	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2", len(resps))
	}
	for i, r := range resps {
		if r.Error == nil || r.Error.Code != -32603 {
			t.Fatalf("element %d: want JSON-RPC -32603 internal error, got %+v", i, r)
		}
	}
}

// The single-request path answers with a JSON-RPC error too.
func TestHandleJSONRPC_SingleHandlerPanicRecovered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"eth_sendRawTransaction","params":["0x01"]}`))

	s := &HTTPServer{h: NewHandlers(panicService{})}
	s.handleJSONRPC(c)

	var r Response
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if r.Error == nil || r.Error.Code != -32603 {
		t.Fatalf("want -32603, got %+v", r)
	}
}
