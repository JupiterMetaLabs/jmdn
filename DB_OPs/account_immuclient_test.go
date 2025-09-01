package DB_OPs

import (
    "testing"
    "time"
)

func TestPutNonceofAccount_Increasing(t *testing.T) {
    n1, err := PutNonceofAccount()
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    // ensure time advances
    time.Sleep(2 * time.Nanosecond)
    n2, err := PutNonceofAccount()
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if n1 == 0 || n2 == 0 {
        t.Fatalf("nonces should be non-zero: %d, %d", n1, n2)
    }
    if n2 < n1 {
        t.Fatalf("expected monotonic nonces, got %d then %d", n1, n2)
    }
}

