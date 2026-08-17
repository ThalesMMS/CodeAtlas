package job

import (
	"context"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestDefaultConfigAllowsLongDeepWikiJobsWithoutExtendingOtherTypes(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()
	if config.DefaultTimeout != 5*time.Minute {
		t.Fatalf("DefaultTimeout = %s, want %s", config.DefaultTimeout, 5*time.Minute)
	}
	if got := config.TypeTimeouts["deepwiki.refresh"]; got != 15*time.Minute {
		t.Fatalf("deepwiki.refresh timeout = %s, want %s", got, 15*time.Minute)
	}
	if _, ok := config.TypeTimeouts["codemap.generate"]; ok {
		t.Fatal("codemap.generate should continue using DefaultTimeout")
	}
}

func TestShutdownWaitsForRunnerAccountedBeforeDispatchUnlock(t *testing.T) {
	manager := NewManager(Config{GlobalConcurrency: 1, RetainCompleted: 8, DefaultTimeout: time.Second})
	runnerFinished := make(chan struct{})

	// Keep the submitted job queued until the test is ready to pin dispatch at
	// the post-unlock job.started publication boundary.
	manager.mu.Lock()
	manager.running = 1
	manager.mu.Unlock()
	snapshot, err := manager.Submit(context.Background(), Spec{
		Type: "test", Key: "shutdown-dispatch",
		Runner: func(ctx context.Context, _ ProgressReporter) (Result, error) {
			defer close(runnerFinished)
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	manager.mu.Lock()
	manager.running = 0
	manager.mu.Unlock()

	manager.broker.mu.Lock()
	dispatchDone := make(chan struct{})
	go func() {
		manager.dispatch()
		close(dispatchDone)
	}()
	waitInternalJobState(t, manager, snapshot.ID, domain.JobRunning)

	// Avoid blocking Shutdown on its own job.canceling publication; the test is
	// specifically pinning dispatch before the runner goroutine starts.
	manager.mu.Lock()
	manager.jobs[snapshot.ID].snapshot.State = domain.JobCanceling
	manager.mu.Unlock()

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()

	select {
	case err := <-shutdownDone:
		manager.broker.mu.Unlock()
		<-dispatchDone
		<-runnerFinished
		t.Fatalf("Shutdown returned before the accounted runner started: %v", err)
	case <-time.After(100 * time.Millisecond):
		// Expected: Wait includes the runner even though dispatch is still pinned.
	}

	manager.broker.mu.Unlock()
	<-dispatchDone
	<-runnerFinished
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func waitInternalJobState(t *testing.T, manager *Manager, id domain.JobID, want domain.JobState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		state := manager.jobs[id].snapshot.State
		manager.mu.Unlock()
		if state == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("job %s did not reach state %s", id, want)
}

func TestEvictTerminalLockedPrunesTTLExpiredOrderEntries(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0).UTC()
	manager := NewManager(Config{RetainCompleted: 8, TerminalTTL: time.Minute})
	expiredID := domain.JobID("job:expired")
	recentID := domain.JobID("job:recent")
	runningID := domain.JobID("job:running")
	manager.jobs[expiredID] = &record{snapshot: domain.JobSnapshot{
		ID: expiredID, State: domain.JobSucceeded, FinishedAt: now.Add(-2 * time.Minute),
	}}
	manager.jobs[recentID] = &record{snapshot: domain.JobSnapshot{
		ID: recentID, State: domain.JobSucceeded, FinishedAt: now.Add(-30 * time.Second),
	}}
	manager.jobs[runningID] = &record{snapshot: domain.JobSnapshot{
		ID: runningID, State: domain.JobRunning, StartedAt: now.Add(-2 * time.Minute),
	}}
	manager.order = []domain.JobID{expiredID, recentID, runningID}

	manager.mu.Lock()
	manager.evictTerminalLocked(now)
	gotOrder := append([]domain.JobID(nil), manager.order...)
	_, expiredPresent := manager.jobs[expiredID]
	manager.mu.Unlock()

	if expiredPresent {
		t.Fatal("TTL-expired job remains in jobs map")
	}
	if want := []domain.JobID{recentID, runningID}; !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("order = %v, want only live job IDs %v", gotOrder, want)
	}
}
