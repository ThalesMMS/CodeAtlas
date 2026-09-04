package observability

import (
	"sync"
	"testing"
	"time"
)

func TestMetricsConcurrentUpdatesAreSafe(t *testing.T) {
	t.Parallel()
	metrics := NewMetrics()
	const workers, iterations = 8, 500
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				metrics.IndexScanStarted()
				metrics.LLMCall(i%2 == 0)
				metrics.Commit(true)
				metrics.HTTPRequest()
				metrics.SetReadinessState("READY")
				_ = metrics.Snapshot()
			}
		}()
	}
	wg.Wait()

	snapshot := metrics.Snapshot()
	if snapshot.IndexScansTotal != workers*iterations {
		t.Fatalf("IndexScansTotal = %d, want %d", snapshot.IndexScansTotal, workers*iterations)
	}
	if snapshot.LLMSuccessTotal+snapshot.LLMFailureTotal != snapshot.LLMCallsTotal {
		t.Fatalf("LLM success+failure (%d+%d) != calls %d", snapshot.LLMSuccessTotal, snapshot.LLMFailureTotal, snapshot.LLMCallsTotal)
	}
	if snapshot.ReadinessState != "READY" {
		t.Fatalf("ReadinessState = %q", snapshot.ReadinessState)
	}
}

func TestNilMetricsIsNoOp(t *testing.T) {
	t.Parallel()
	var metrics *Metrics // nil
	metrics.IndexScanStarted()
	metrics.LLMCall(true)
	metrics.IndexScanSucceeded(time.Second, 10, time.Now())
	if snapshot := metrics.Snapshot(); snapshot.IndexScansTotal != 0 {
		t.Fatalf("nil metrics produced non-zero snapshot: %+v", snapshot)
	}
}

func TestIndexScanGaugesRecorded(t *testing.T) {
	t.Parallel()
	metrics := NewMetrics()
	at := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	metrics.IndexScanSucceeded(1500*time.Millisecond, 42, at)
	snapshot := metrics.Snapshot()
	if snapshot.LastScanDurationMS != 1500 || snapshot.IndexSize != 42 || !snapshot.LastScanOK {
		t.Fatalf("scan gauges = %+v", snapshot)
	}
}
