package main

import "testing"

type recordingReporter struct{ calls int }

func (r *recordingReporter) ReportEquivocation(string, string, uint64, []int8) { r.calls++ }

func TestVerifiedEquivocationReporter(t *testing.T) {
	inner := &recordingReporter{}
	confirmed := false
	r := verifiedEquivocationReporter{inner: inner, verify: func(string, string, uint64) bool { return confirmed }}

	r.ReportEquivocation("p", "0xh", 1, []int8{-1, 1})
	if inner.calls != 0 {
		t.Fatal("unconfirmed verdict forwarded")
	}
	confirmed = true
	r.ReportEquivocation("p", "0xh", 1, []int8{-1, 1})
	if inner.calls != 1 {
		t.Fatal("confirmed verdict not forwarded")
	}
}
