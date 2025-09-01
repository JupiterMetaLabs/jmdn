package DB_OPs

import (
    "errors"
    "testing"
 
    "gossipnode/config"

    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
)

func TestToBytes(t *testing.T) {
    // string
    b, err := toBytes("hello")
    if err != nil { t.Fatalf("unexpected error: %v", err) }
    if string(b) != "hello" { t.Fatalf("expected 'hello', got %q", string(b)) }

    // []byte
    b2, err := toBytes([]byte{1,2,3})
    if err != nil { t.Fatalf("unexpected error: %v", err) }
    if len(b2) != 3 || b2[0]!=1 || b2[1]!=2 || b2[2]!=3 { t.Fatalf("unexpected bytes: %v", b2) }

    // nil value
    if _, err := toBytes(nil); err == nil {
        t.Fatalf("expected error for nil value")
    } else if !errors.Is(err, ErrNilValue) {
        t.Fatalf("expected ErrNilValue, got %v", err)
    }

    // struct JSON
    type S struct { A int `json:"a"`; B string `json:"b"` }
    b3, err := toBytes(S{A:1, B:"x"})
    if err != nil { t.Fatalf("unexpected error: %v", err) }
    expected := "{\"a\":1,\"b\":\"x\"}"
    if string(b3) != expected { t.Fatalf("expected %s, got %s", expected, string(b3)) }
}

func TestIsConnectionError(t *testing.T) {
    // gRPC codes commonly used for connection issues
    if !isConnectionError(status.Error(codes.Unavailable, "unavailable")) { t.Fatalf("Unavailable should be connection error") }
    if !isConnectionError(status.Error(codes.Canceled, "canceled")) { t.Fatalf("Canceled should be connection error") }
    if !isConnectionError(status.Error(codes.DeadlineExceeded, "deadline")) { t.Fatalf("DeadlineExceeded should be connection error") }

    // String matches
    if !isConnectionError(errors.New("connection refused")) { t.Fatalf("'connection refused' should be connection error") }
    if !isConnectionError(errors.New("broken pipe")) { t.Fatalf("'broken pipe' should be connection error") }
    if !isConnectionError(errors.New("connection reset by peer")) { t.Fatalf("prefix connection reset should be connection error") }

    // Non-connection error
    if isConnectionError(errors.New("some other error")) { t.Fatalf("unexpected connection error detection") }
}

func TestIsNotFoundError(t *testing.T) {
    if !isNotFoundError(errors.New("key not found")) { t.Fatalf("expected not found true") }
    if !isNotFoundError(errors.New("tbtree: key not found")) { t.Fatalf("expected tbtree not found true") }
    if isNotFoundError(errors.New("another error")) { t.Fatalf("expected not found false") }
}

func TestHashBlockDeterministic(t *testing.T) {
    h := NewBlockHasher()
    a := HashBlock(h, "nonce", "sender", 123456789)
    b := HashBlock(h, "nonce", "sender", 123456789)
    if a != b { t.Fatalf("expected deterministic hash, got %s vs %s", a, b) }
    if len(a) != 16 { t.Fatalf("expected truncated 16-char hex, got %d", len(a)) }
}

func TestSet_Validation(t *testing.T) {
    tx := &config.ImmuTransaction{}

    if err := Set(tx, "", "value"); !errors.Is(err, ErrEmptyKey) {
        t.Fatalf("expected ErrEmptyKey, got %v", err)
    }
    if err := Set(tx, "key", nil); !errors.Is(err, ErrNilValue) {
        t.Fatalf("expected ErrNilValue, got %v", err)
    }
    if err := Set(tx, "key", map[string]int{"a":1}); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(tx.Ops) != 1 { t.Fatalf("expected 1 operation, got %d", len(tx.Ops)) }
}

func TestPing_NilClient(t *testing.T) {
    if err := Ping(nil); err == nil {
        t.Fatal("expected error for nil client")
    }
}

func TestIsHealthy_NilOrDisconnected(t *testing.T) {
    if IsHealthy(nil) {
        t.Fatal("nil client should not be healthy")
    }
    ic := &config.ImmuClient{IsConnected:false}
    if IsHealthy(ic) {
        t.Fatal("disconnected client should not be healthy")
    }
}

func TestBatchCreate_Empty(t *testing.T) {
    if err := BatchCreate(nil, map[string]interface{}{}); !errors.Is(err, ErrEmptyBatch) {
        t.Fatalf("expected ErrEmptyBatch, got %v", err)
    }
}

