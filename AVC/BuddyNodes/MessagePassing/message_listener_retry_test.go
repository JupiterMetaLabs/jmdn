package MessagePassing

import (
	"errors"
	"testing"
)

func TestIsRetryableStreamReadErr_GivenStreamReset_WhenCalled_ThenTrue(t *testing.T) {
	// Given
	err := errors.New("stream reset (remote): code: 0x1002: transport error")

	// When
	got := isRetryableStreamReadErr(err)

	// Then
	if !got {
		t.Fatalf("expected retryable=true for stream reset error")
	}
}

func TestIsRetryableStreamReadErr_GivenNil_WhenCalled_ThenFalse(t *testing.T) {
	// Given/When
	got := isRetryableStreamReadErr(nil)

	// Then
	if got {
		t.Fatalf("expected retryable=false for nil error")
	}
}
