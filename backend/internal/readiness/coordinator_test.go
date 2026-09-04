package readiness

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNewCoordinatorStartsBooting(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	if got := c.CurrentState(); got != StateBooting {
		t.Fatalf("CurrentState() = %q, want %q", got, StateBooting)
	}
	snap := c.Snapshot()
	if snap.State != StateBooting {
		t.Fatalf("Snapshot().State = %q, want %q", snap.State, StateBooting)
	}
	if len(snap.TransitionHistory) != 1 {
		t.Fatalf("history len = %d, want 1 bootstrap entry", len(snap.TransitionHistory))
	}
	if snap.FatalError != nil {
		t.Fatalf("FatalError = %#v, want nil", snap.FatalError)
	}
}

func TestCanTransitionTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		from State
		to   State
		want bool
	}{
		// Valid forward lifecycle.
		{StateBooting, StateProbingCapabilities, true},
		{StateProbingCapabilities, StateAwaitingConfiguration, true},
		{StateAwaitingConfiguration, StateProbingCapabilities, true},
		{StateProbingCapabilities, StateIndexing, true},
		{StateIndexing, StateGeneratingArtifacts, true},
		{StateIndexing, StateReady, true},
		{StateGeneratingArtifacts, StateReady, true},
		// Failure reachable from every operational state.
		{StateBooting, StateFailed, true},
		{StateProbingCapabilities, StateFailed, true},
		{StateIndexing, StateFailed, true},
		{StateGeneratingArtifacts, StateFailed, true},
		{StateReady, StateFailed, true},
		// Shutdown reachable from any non-terminal state, including FAILED.
		{StateBooting, StateShuttingDown, true},
		{StateReady, StateShuttingDown, true},
		{StateFailed, StateShuttingDown, true},
		// Invalid: no silent recovery, no backward jumps, no self-loops.
		{StateFailed, StateReady, false},
		{StateFailed, StateIndexing, false},
		{StateReady, StateBooting, false},
		{StateReady, StateIndexing, false},
		{StateReady, StateReady, false},
		{StateIndexing, StateProbingCapabilities, false},
		{StateBooting, StateIndexing, false},
		{StateBooting, StateReady, false},
		// SHUTTING_DOWN is terminal.
		{StateShuttingDown, StateReady, false},
		{StateShuttingDown, StateFailed, false},
		{StateShuttingDown, StateShuttingDown, false},
	}
	for _, tc := range cases {
		if got := CanTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestCoordinatorAwaitingConfigurationRetryCoalesces(t *testing.T) {
	c := NewCoordinator()
	if err := c.Transition(StateProbingCapabilities, "probe"); err != nil {
		t.Fatal(err)
	}
	if err := c.Transition(StateAwaitingConfiguration, "provider configuration required"); err != nil {
		t.Fatal(err)
	}
	c.SignalConfigurationRetry()
	c.SignalConfigurationRetry()
	select {
	case <-c.ConfigurationRetries():
	case <-time.After(time.Second):
		t.Fatal("configuration retry was not signaled")
	}
	select {
	case <-c.ConfigurationRetries():
		t.Fatal("configuration retries did not coalesce")
	default:
	}
	if err := c.Transition(StateProbingCapabilities, "retry provider configuration"); err != nil {
		t.Fatal(err)
	}
	c.SignalConfigurationRetry()
	select {
	case <-c.ConfigurationRetries():
		t.Fatal("retry signal was retained outside awaiting state")
	default:
	}
}

func TestTransitionLifecycle(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	steps := []State{StateProbingCapabilities, StateIndexing, StateGeneratingArtifacts, StateReady, StateShuttingDown}
	for _, to := range steps {
		if err := c.Transition(to, "step"); err != nil {
			t.Fatalf("Transition(%q) error = %v", to, err)
		}
		if got := c.CurrentState(); got != to {
			t.Fatalf("after Transition(%q), state = %q", to, got)
		}
	}
}

func TestTransitionInvalidKeepsState(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	err := c.Transition(StateIndexing, "skip ahead")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition error = %v, want ErrInvalidTransition", err)
	}
	if got := c.CurrentState(); got != StateBooting {
		t.Fatalf("state after invalid transition = %q, want BOOTING", got)
	}
}

func TestFailPreservesFirstCauseAndAccumulates(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	if err := c.Transition(StateProbingCapabilities, "probe"); err != nil {
		t.Fatalf("Transition error = %v", err)
	}
	if err := c.Fail("PROBE_FAILED", "llm unreachable"); err != nil {
		t.Fatalf("Fail error = %v", err)
	}
	if got := c.CurrentState(); got != StateFailed {
		t.Fatalf("state = %q, want FAILED", got)
	}
	// Subsequent failures must not overwrite the first cause.
	if err := c.Fail("INITIAL_INDEX_FAILED", "initial indexing failed"); err != nil {
		t.Fatalf("second Fail error = %v", err)
	}
	snap := c.Snapshot()
	if snap.FatalError == nil {
		t.Fatal("FatalError = nil after Fail")
	}
	if snap.FatalError.Code != "PROBE_FAILED" || snap.FatalError.Message != "llm unreachable" {
		t.Fatalf("first cause mutated: %#v", snap.FatalError)
	}
	if len(snap.FatalError.SecondaryErrors) != 1 {
		t.Fatalf("SecondaryErrors = %#v, want 1 entry", snap.FatalError.SecondaryErrors)
	}
}

func TestFailUnreachableFromShuttingDown(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	if err := c.Shutdown("bye"); err != nil {
		t.Fatalf("Shutdown error = %v", err)
	}
	err := c.Fail("LATE", "too late")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Fail after shutdown error = %v, want ErrInvalidTransition", err)
	}
}

func TestFailFromEachOperationalState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		path []State
	}{
		{"booting", nil},
		{"probing", []State{StateProbingCapabilities}},
		{"indexing", []State{StateProbingCapabilities, StateIndexing}},
		{"generating", []State{StateProbingCapabilities, StateIndexing, StateGeneratingArtifacts}},
		{"ready", []State{StateProbingCapabilities, StateIndexing, StateReady}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCoordinator()
			for _, to := range tc.path {
				if err := c.Transition(to, "setup"); err != nil {
					t.Fatalf("setup Transition(%q) error = %v", to, err)
				}
			}
			if err := c.Fail("X", "boom"); err != nil {
				t.Fatalf("Fail error = %v", err)
			}
			if got := c.CurrentState(); got != StateFailed {
				t.Fatalf("state = %q, want FAILED", got)
			}
		})
	}
}

func TestShutdownIdempotent(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	if err := c.Shutdown("first"); err != nil {
		t.Fatalf("Shutdown error = %v", err)
	}
	if err := c.Shutdown("second"); err != nil {
		t.Fatalf("second Shutdown error = %v, want nil (no-op)", err)
	}
	if got := c.CurrentState(); got != StateShuttingDown {
		t.Fatalf("state = %q, want SHUTTING_DOWN", got)
	}
}

func TestSnapshotIsImmutable(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	c.SetCapability("llm", true, "")
	if err := c.Transition(StateProbingCapabilities, "probe"); err != nil {
		t.Fatalf("Transition error = %v", err)
	}
	if err := c.Fail("BOOM", "first"); err != nil {
		t.Fatalf("Fail error = %v", err)
	}

	snap := c.Snapshot()
	// Mutate every reference-typed field of the returned snapshot.
	snap.Capabilities[0].Available = false
	snap.Capabilities = append(snap.Capabilities, CapabilityStatus{Name: "injected"})
	snap.TransitionHistory[0].Reason = "tampered"
	snap.FatalError.Message = "tampered"
	snap.FatalError.SecondaryErrors = append(snap.FatalError.SecondaryErrors, "tampered")

	fresh := c.Snapshot()
	if len(fresh.Capabilities) != 1 || !fresh.Capabilities[0].Available {
		t.Fatalf("internal capabilities mutated via snapshot: %#v", fresh.Capabilities)
	}
	if fresh.TransitionHistory[0].Reason == "tampered" {
		t.Fatal("internal history mutated via snapshot")
	}
	if fresh.FatalError.Message != "first" || len(fresh.FatalError.SecondaryErrors) != 0 {
		t.Fatalf("internal fatal error mutated via snapshot: %#v", fresh.FatalError)
	}
}

func TestSubscribeReceivesEvents(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	events, cancel := c.Subscribe()
	defer cancel()

	if err := c.Transition(StateProbingCapabilities, "probe"); err != nil {
		t.Fatalf("Transition error = %v", err)
	}
	select {
	case event := <-events:
		if event.From != StateBooting || event.To != StateProbingCapabilities {
			t.Fatalf("event = %+v, want BOOTING->PROBING_CAPABILITIES", event)
		}
		if event.Snapshot.State != StateProbingCapabilities {
			t.Fatalf("event snapshot state = %q", event.Snapshot.State)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive event")
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	events, cancel := c.Subscribe()
	cancel()
	cancel() // idempotent, must not panic
	if _, ok := <-events; ok {
		t.Fatal("expected closed channel after unsubscribe")
	}
	// Further transitions must not panic with a removed subscriber.
	if err := c.Transition(StateProbingCapabilities, "probe"); err != nil {
		t.Fatalf("Transition after unsubscribe error = %v", err)
	}
}

func TestPublishDropsWhenSubscriberFull(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	dropped := 0
	c.SetEventDropObserver(func() {
		dropped++
		c.SetStep("drop observed")
	})
	full := make(chan StateChangeEvent, 1)
	full <- StateChangeEvent{} // saturate the buffer
	c.mu.Lock()
	c.subscribers[full] = struct{}{}
	c.mu.Unlock()

	done := make(chan struct{})
	go func() {
		c.publish(StateChangeEvent{To: StateReady})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on a full subscriber")
	}
	if len(full) != 1 {
		t.Fatalf("buffered events = %d, want 1 (new event dropped)", len(full))
	}
	if dropped != 1 {
		t.Fatalf("drop observer calls = %d, want 1", dropped)
	}
	if got := c.Snapshot().CurrentStep; got != "drop observed" {
		t.Fatalf("drop observer reentrant update = %q", got)
	}
}

func TestHistoryIsCapped(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	c.mu.Lock()
	for i := 0; i < maxHistory*2; i++ {
		c.applyLocked(StateReady, StateReady, "churn")
	}
	c.mu.Unlock()
	if got := len(c.Snapshot().TransitionHistory); got != maxHistory {
		t.Fatalf("history len = %d, want capped at %d", got, maxHistory)
	}
}

func TestConcurrentAccessRace(t *testing.T) {
	t.Parallel()
	c := NewCoordinator()
	const workers = 24
	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch (id + i) % 6 {
				case 0:
					_ = c.Transition(StateProbingCapabilities, "race")
				case 1:
					_ = c.Fail("RACE", "boom")
				case 2:
					_ = c.Snapshot()
				case 3:
					c.SetCapability("llm", i%2 == 0, "")
				case 4:
					c.SetIndexRevision("rev")
					c.SetStep("step")
				case 5:
					events, cancel := c.Subscribe()
					go func() {
						for range events {
						}
					}()
					cancel()
				}
			}
		}(w)
	}
	wg.Wait()
}
