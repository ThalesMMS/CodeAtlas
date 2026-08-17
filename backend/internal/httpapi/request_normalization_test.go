package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/job"
)

func TestSubmitCodemapJobDeduplicatesEquivalentNormalizedRequests(t *testing.T) {
	manager := job.NewManager(job.Config{GlobalConcurrency: 1})
	release := make(chan struct{})
	started := make(chan struct{})
	_, err := manager.Submit(context.Background(), job.Spec{
		Type: "blocker", Key: "blocker", DedupPolicy: job.DedupReject,
		Runner: func(context.Context, job.ProgressReporter) (job.Result, error) {
			close(started)
			<-release
			return job.Result{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	server := &Server{jobs: manager}
	defaulted, err := server.submitCodemapJob(context.Background(), domain.CodemapRequest{Query: " checkout   flow ", MaxNodes: 0}, "default")
	if err != nil {
		t.Fatal(err)
	}
	explicitDefault, err := server.submitCodemapJob(context.Background(), domain.CodemapRequest{Query: "checkout flow", MaxNodes: 36}, "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if defaulted.ID != explicitDefault.ID || defaulted.Key != explicitDefault.Key {
		t.Fatalf("default requests did not coalesce: first=%#v second=%#v", defaulted, explicitDefault)
	}

	floored, err := server.submitCodemapJob(context.Background(), domain.CodemapRequest{Query: "checkout flow", MaxNodes: 1}, "floor")
	if err != nil {
		t.Fatal(err)
	}
	explicitFloor, err := server.submitCodemapJob(context.Background(), domain.CodemapRequest{Query: "checkout flow", MaxNodes: 8}, "explicit-floor")
	if err != nil {
		t.Fatal(err)
	}
	if floored.ID != explicitFloor.ID || floored.Key != explicitFloor.Key {
		t.Fatalf("floor requests did not coalesce: first=%#v second=%#v", floored, explicitFloor)
	}

	_, _ = manager.Cancel(context.Background(), defaulted.ID, nil)
	_, _ = manager.Cancel(context.Background(), floored.ID, nil)
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}
