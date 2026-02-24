package metrics

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func findMetricFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

func TestRegisterRuntimeCollectors_ExposesGoGoroutines(t *testing.T) {
	// Given
	RegisterRuntimeCollectors()

	// When
	families, err := DefaultRegistry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	// Then
	if findMetricFamily(t, families, "go_goroutines") == nil {
		t.Fatalf("expected go_goroutines metric family to be present")
	}
}

func TestWrapTrackedGoroutine_UpdatesCountersAndGauge(t *testing.T) {
	// Given
	app := "app:test"
	local := "local:test"
	thread := "thread:test:ok"

	running := groGoroutinesRunning.WithLabelValues(app, local, thread)
	started := groGoroutinesStartedTotal.WithLabelValues(app, local, thread)
	finishedOK := groGoroutinesFinishedTotal.WithLabelValues(app, local, thread, groResultOK)

	// When
	tracked := WrapTrackedGoroutine(app, local, thread, func(ctx context.Context) error {
		return nil
	})
	if err := tracked(context.Background()); err != nil {
		t.Fatalf("tracked returned error: %v", err)
	}

	// Then
	if got := testutil.ToFloat64(running); got != 0 {
		t.Fatalf("running gauge expected 0, got %v", got)
	}
	if got := testutil.ToFloat64(started); got < 1 {
		t.Fatalf("started counter expected >= 1, got %v", got)
	}
	if got := testutil.ToFloat64(finishedOK); got < 1 {
		t.Fatalf("finished ok counter expected >= 1, got %v", got)
	}
}

func TestWrapTrackedGoroutine_ClassifiesCanceled(t *testing.T) {
	// Given
	app := "app:test"
	local := "local:test"
	thread := "thread:test:canceled"

	finishedCanceled := groGoroutinesFinishedTotal.WithLabelValues(
		app,
		local,
		thread,
		groResultCanceled,
	)

	before := testutil.ToFloat64(finishedCanceled)

	// When
	tracked := WrapTrackedGoroutine(app, local, thread, func(ctx context.Context) error {
		return context.Canceled
	})
	_ = tracked(context.Background())

	// Then
	after := testutil.ToFloat64(finishedCanceled)
	if after <= before {
		t.Fatalf("expected canceled counter to increase (before=%v after=%v)", before, after)
	}
}
