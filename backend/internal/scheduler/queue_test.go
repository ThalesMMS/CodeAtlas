package scheduler

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestRequestQueueSerializesAndCoalescesPendingRequests(t *testing.T) {
	queue := NewRequestQueue()

	firstDone := queue.EnqueueWait(domain.ReconcileRequest{
		ID:        "first",
		CreatedAt: time.Unix(1, 0).UTC(),
		Scope: domain.ReconcileScope{
			Mode:  domain.ReconcilePaths,
			Paths: []string{"pkg/a.go"},
		},
	})
	first, ok := queue.Dequeue(context.Background())
	if !ok {
		t.Fatal("Dequeue returned no first request")
	}
	if first.ID != "first" {
		t.Fatalf("first ID = %q, want first", first.ID)
	}

	secondDone := queue.EnqueueWait(domain.ReconcileRequest{
		ID:        "second",
		CreatedAt: time.Unix(2, 0).UTC(),
		Scope: domain.ReconcileScope{
			Mode:  domain.ReconcilePaths,
			Paths: []string{"pkg/b.go"},
		},
	})
	fullDone := queue.EnqueueWait(domain.ReconcileRequest{
		ID:        "full",
		CreatedAt: time.Unix(3, 0).UTC(),
		Priority:  10,
		Scope: domain.ReconcileScope{
			Mode:        domain.ReconcileFull,
			ReasonCodes: []string{ReasonManual},
			MinSequence: 4,
			MaxSequence: 9,
		},
	})

	if !queue.HasPending() {
		t.Fatal("queue lost requests enqueued while first request is active")
	}
	queue.Complete(nil)
	if err := <-firstDone; err != nil {
		t.Fatalf("first waiter error = %v", err)
	}

	next, ok := queue.Dequeue(context.Background())
	if !ok {
		t.Fatal("Dequeue returned no coalesced request")
	}
	if next.Scope.Mode != domain.ReconcileFull {
		t.Fatalf("next scope = %s, want full", next.Scope.Mode)
	}
	if !reflect.DeepEqual(next.Scope.ReasonCodes, []string{ReasonManual}) {
		t.Fatalf("reason codes = %v, want manual", next.Scope.ReasonCodes)
	}
	queue.Complete(nil)
	for name, done := range map[string]<-chan error{"second": secondDone, "full": fullDone} {
		if err := <-done; err != nil {
			t.Fatalf("%s waiter error = %v", name, err)
		}
	}
}
