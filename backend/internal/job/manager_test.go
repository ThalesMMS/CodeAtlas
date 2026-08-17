package job_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/job"
)

func TestManagerCoalescesKeyAndPublishesOrderedLifecycle(t *testing.T) {
	manager := job.NewManager(job.Config{
		MaxQueued:         4,
		GlobalConcurrency: 1,
		PerTypeConcurrency: map[string]int{
			"deepwiki.refresh": 1,
		},
		RetainCompleted: 8,
		EventBuffer:     8,
		DefaultTimeout:  time.Second,
	})
	defer manager.Shutdown(context.Background())

	events, unsubscribe := manager.Subscribe()
	defer unsubscribe()

	started := make(chan struct{})
	release := make(chan struct{})
	spec := job.Spec{
		Type:            "deepwiki.refresh",
		Key:             "deepwiki:test-repo",
		InputSnapshotID: "sha256:input",
		DedupPolicy:     job.DedupCoalesce,
		Runner: func(ctx context.Context, reporter job.ProgressReporter) (job.Result, error) {
			if err := reporter.Report("inventory", "inventorying modules", domain.Progress{Completed: 1, Total: 2, Unit: "stage"}); err != nil {
				return job.Result{}, err
			}
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return job.Result{}, ctx.Err()
			}
			if err := reporter.Report("publish", "publishing artifact", domain.Progress{Completed: 2, Total: 2, Unit: "stage"}); err != nil {
				return job.Result{}, err
			}
			return job.Result{ArtifactID: "artifact-1", SnapshotID: "sha256:result"}, nil
		},
	}

	first, err := manager.Submit(context.Background(), spec)
	if err != nil {
		t.Fatalf("Submit first: %v", err)
	}
	second, err := manager.Submit(context.Background(), spec)
	if err != nil {
		t.Fatalf("Submit coalesced: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("coalesced id = %q, want %q", second.ID, first.ID)
	}
	<-started
	close(release)

	final := waitJobState(t, manager, first.ID, domain.JobSucceeded)
	if final.ResultArtifactID != "artifact-1" || final.InputSnapshotID != "sha256:input" || final.Revision < 4 {
		t.Fatalf("final snapshot = %#v", final)
	}
	got := collectEventTypes(t, events, 4)
	want := []string{"job.queued", "job.started", "job.progress", "job.progress"}
	if !reflect.DeepEqual(got[:4], want) {
		t.Fatalf("event types = %v, want prefix %v", got, want)
	}
	if got[len(got)-1] != "job.succeeded" {
		t.Fatalf("last event = %q, want job.succeeded; all=%v", got[len(got)-1], got)
	}
}

func TestManagerQueueLimitCancelPanicAndStale(t *testing.T) {
	manager := job.NewManager(job.Config{
		MaxQueued:         1,
		GlobalConcurrency: 1,
		RetainCompleted:   8,
		EventBuffer:       8,
		DefaultTimeout:    time.Second,
	})
	defer manager.Shutdown(context.Background())

	release := make(chan struct{})
	first, err := manager.Submit(context.Background(), job.Spec{
		Type:        "repository.reindex",
		Key:         "repository:full",
		DedupPolicy: job.DedupReject,
		Runner: func(ctx context.Context, _ job.ProgressReporter) (job.Result, error) {
			select {
			case <-release:
				return job.Result{}, nil
			case <-ctx.Done():
				return job.Result{}, ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatalf("Submit first: %v", err)
	}
	waitJobState(t, manager, first.ID, domain.JobRunning)

	queued, err := manager.Submit(context.Background(), job.Spec{
		Type:        "codemap.generate",
		Key:         "codemap:one",
		DedupPolicy: job.DedupReject,
		Runner:      func(context.Context, job.ProgressReporter) (job.Result, error) { return job.Result{}, nil },
	})
	if err != nil {
		t.Fatalf("Submit queued: %v", err)
	}
	if _, err := manager.Submit(context.Background(), job.Spec{
		Type:        "codemap.generate",
		Key:         "codemap:two",
		DedupPolicy: job.DedupReject,
		Runner:      func(context.Context, job.ProgressReporter) (job.Result, error) { return job.Result{}, nil },
	}); !errors.Is(err, job.ErrQueueFull) {
		t.Fatalf("third submit err = %v, want ErrQueueFull", err)
	}
	canceled, err := manager.Cancel(context.Background(), queued.ID, nil)
	if err != nil {
		t.Fatalf("Cancel queued: %v", err)
	}
	if canceled.State != domain.JobCanceled {
		t.Fatalf("queued cancel state = %s", canceled.State)
	}
	close(release)
	waitJobState(t, manager, first.ID, domain.JobSucceeded)

	panicJob, err := manager.Submit(context.Background(), job.Spec{
		Type: "fts.rebuild", Key: "fts:panic",
		Runner: func(context.Context, job.ProgressReporter) (job.Result, error) {
			panic("secret api_key=sk-test")
		},
	})
	if err != nil {
		t.Fatalf("Submit panic job: %v", err)
	}
	panicFinal := waitJobState(t, manager, panicJob.ID, domain.JobFailed)
	if panicFinal.Error == nil || panicFinal.Error.Code != "JOB_PANIC" || panicFinal.Error.Message == "" || panicFinal.Error.Message == "secret api_key=sk-test" {
		t.Fatalf("panic public error leaked or missing detail: %#v", panicFinal.Error)
	}

	staleJob, err := manager.Submit(context.Background(), job.Spec{
		Type: "deepwiki.refresh", Key: "deepwiki:stale",
		Runner: func(context.Context, job.ProgressReporter) (job.Result, error) {
			return job.Result{}, job.ErrStale
		},
	})
	if err != nil {
		t.Fatalf("Submit stale job: %v", err)
	}
	staleFinal := waitJobState(t, manager, staleJob.ID, domain.JobStale)
	if staleFinal.Error == nil || staleFinal.Error.Code != "JOB_INPUT_STALE" {
		t.Fatalf("stale error = %#v, want JOB_INPUT_STALE", staleFinal.Error)
	}
}

func TestManagerStartsLaterJobWhenHeadIsBlockedByTypeLimit(t *testing.T) {
	manager := job.NewManager(job.Config{
		MaxQueued:         4,
		GlobalConcurrency: 2,
		PerTypeConcurrency: map[string]int{
			"codemap.generate":   1,
			"repository.reindex": 1,
		},
		RetainCompleted: 8,
		EventBuffer:     8,
		DefaultTimeout:  time.Second,
	})
	defer manager.Shutdown(context.Background())

	releaseCodemap := make(chan struct{})
	first, err := manager.Submit(context.Background(), job.Spec{
		Type: "codemap.generate", Key: "codemap:first", DedupPolicy: job.DedupReject,
		Runner: blockingRunner(releaseCodemap),
	})
	if err != nil {
		t.Fatalf("Submit first codemap: %v", err)
	}
	waitJobState(t, manager, first.ID, domain.JobRunning)

	second, err := manager.Submit(context.Background(), job.Spec{
		Type: "codemap.generate", Key: "codemap:second", DedupPolicy: job.DedupReject,
		Runner: blockingRunner(make(chan struct{})),
	})
	if err != nil {
		t.Fatalf("Submit second codemap: %v", err)
	}
	waitJobState(t, manager, second.ID, domain.JobQueued)

	releaseReindex := make(chan struct{})
	reindex, err := manager.Submit(context.Background(), job.Spec{
		Type: "repository.reindex", Key: "repository:full", DedupPolicy: job.DedupReject,
		Runner: blockingRunner(releaseReindex),
	})
	if err != nil {
		t.Fatalf("Submit reindex: %v", err)
	}
	waitJobState(t, manager, reindex.ID, domain.JobRunning)

	close(releaseCodemap)
	close(releaseReindex)
	waitJobState(t, manager, first.ID, domain.JobSucceeded)
	waitJobState(t, manager, reindex.ID, domain.JobSucceeded)
}

func blockingRunner(release <-chan struct{}) job.Runner {
	return func(ctx context.Context, _ job.ProgressReporter) (job.Result, error) {
		select {
		case <-release:
			return job.Result{}, nil
		case <-ctx.Done():
			return job.Result{}, ctx.Err()
		}
	}
}

func waitJobState(t *testing.T, manager *job.Manager, id domain.JobID, want domain.JobState) domain.JobSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := manager.Get(id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if snapshot.State == want {
			return snapshot
		}
		time.Sleep(time.Millisecond)
	}
	snapshot, _ := manager.Get(id)
	t.Fatalf("job %s state = %s, want %s", id, snapshot.State, want)
	return domain.JobSnapshot{}
}

func collectEventTypes(t *testing.T, events <-chan domain.JobEvent, min int) []string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	var got []string
	for len(got) < min || got[len(got)-1] != "job.succeeded" {
		select {
		case event := <-events:
			got = append(got, event.Type)
		case <-deadline:
			t.Fatalf("timed out waiting for job events; got %v", got)
		}
	}
	return got
}
