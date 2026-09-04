package app

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
)

type retryProviderProbe struct {
	mu      sync.Mutex
	succeed bool
	calls   int
}

func (p *retryProviderProbe) ProbeChat(context.Context) ai.ProviderProbeResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.succeed {
		return ai.ProviderProbeResult{Status: ai.ProbeSuccess}
	}
	return ai.ProviderProbeResult{Status: ai.ProbeFailure, ErrorCode: ai.CodeProviderUnreachable, Message: "provider unavailable"}
}
func (p *retryProviderProbe) setSuccess() {
	p.mu.Lock()
	p.succeed = true
	p.mu.Unlock()
}
func (p *retryProviderProbe) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestBootstrapAwaitingConfigurationRetriesWithoutPolling(t *testing.T) {
	coordinator := readiness.NewCoordinator()
	probe := &retryProviderProbe{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var indexed atomic.Int32
	var migrated atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBootstrap(ctx, bootstrapDeps{
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)), coordinator: coordinator,
			registry: capabilities.NewRegistry(), providerProbe: probe,
			migrateStore: func(context.Context) error { migrated.Add(1); return nil },
			initialIndex: func(context.Context) error { indexed.Add(1); return nil },
		})
	}()
	waitReadinessState(t, coordinator, readiness.StateAwaitingConfiguration)
	if indexed.Load() != 0 || migrated.Load() != 1 || probe.callCount() != 1 {
		t.Fatalf("awaiting indexed/migrations/probes = %d/%d/%d", indexed.Load(), migrated.Load(), probe.callCount())
	}
	select {
	case <-done:
		t.Fatal("bootstrap returned while awaiting configuration")
	case <-time.After(40 * time.Millisecond):
	}
	if probe.callCount() != 1 {
		t.Fatalf("provider was polled %d times", probe.callCount())
	}

	probe.setSuccess()
	coordinator.SignalConfigurationRetry()
	waitReadinessState(t, coordinator, readiness.StateReady)
	if indexed.Load() != 1 || probe.callCount() != 2 {
		t.Fatalf("recovered indexed/probes = %d/%d", indexed.Load(), probe.callCount())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bootstrap did not stop after cancellation")
	}
	history := coordinator.Snapshot().TransitionHistory
	want := []readiness.State{
		readiness.StateBooting, readiness.StateProbingCapabilities, readiness.StateMigratingStore, readiness.StateAwaitingConfiguration,
		readiness.StateProbingCapabilities, readiness.StateIndexing, readiness.StateReady,
	}
	if len(history) < len(want) {
		t.Fatalf("transition history = %#v", history)
	}
	for index, state := range want {
		if history[index].To != state {
			t.Fatalf("transition[%d] = %s, want %s", index, history[index].To, state)
		}
	}
}

func waitReadinessState(t *testing.T, coordinator *readiness.Coordinator, state readiness.State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if coordinator.CurrentState() == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state = %s, want %s", coordinator.CurrentState(), state)
}
