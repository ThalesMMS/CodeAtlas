package scheduler

import (
	"context"
	"sort"
	"sync"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

type queuedRequest struct {
	request domain.ReconcileRequest
	waiters []chan error
}

type RequestQueue struct {
	mu      sync.Mutex
	signal  chan struct{}
	active  *queuedRequest
	pending *queuedRequest
	closed  bool
}

func NewRequestQueue() *RequestQueue {
	return &RequestQueue{signal: make(chan struct{}, 1)}
}

func (q *RequestQueue) Enqueue(req domain.ReconcileRequest) {
	_ = q.enqueue(req, nil)
}

func (q *RequestQueue) EnqueueWait(req domain.ReconcileRequest) <-chan error {
	done := make(chan error, 1)
	_ = q.enqueue(req, done)
	return done
}

func (q *RequestQueue) enqueue(req domain.ReconcileRequest, done chan error) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		if done != nil {
			done <- context.Canceled
			close(done)
		}
		return false
	}
	incoming := &queuedRequest{request: req}
	if done != nil {
		incoming.waiters = append(incoming.waiters, done)
	}
	if q.active == nil && q.pending == nil {
		q.pending = incoming
		q.wake()
		return true
	}
	if q.pending == nil {
		q.pending = incoming
		q.wake()
		return true
	}
	q.pending.request = mergeRequests(q.pending.request, req)
	q.pending.waiters = append(q.pending.waiters, incoming.waiters...)
	q.wake()
	return true
}

func (q *RequestQueue) Dequeue(ctx context.Context) (domain.ReconcileRequest, bool) {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return domain.ReconcileRequest{}, false
		}
		if q.active == nil && q.pending != nil {
			q.active = q.pending
			q.pending = nil
			req := q.active.request
			q.mu.Unlock()
			return req, true
		}
		q.mu.Unlock()

		select {
		case <-ctx.Done():
			return domain.ReconcileRequest{}, false
		case <-q.signal:
		}
	}
}

func (q *RequestQueue) Complete(err error) {
	q.mu.Lock()
	active := q.active
	q.active = nil
	q.wake()
	q.mu.Unlock()
	if active == nil {
		return
	}
	for _, waiter := range active.waiters {
		waiter <- err
		close(waiter)
	}
}

func (q *RequestQueue) HasPending() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pending != nil
}

func (q *RequestQueue) Snapshot() (pendingPathCount int, pendingFull bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending == nil {
		return 0, false
	}
	if q.pending.request.Scope.Mode == domain.ReconcileFull {
		return 0, true
	}
	return len(q.pending.request.Scope.Paths), false
}

func (q *RequestQueue) Close() {
	q.mu.Lock()
	q.closed = true
	active := q.active
	pending := q.pending
	q.active = nil
	q.pending = nil
	q.wake()
	q.mu.Unlock()
	for _, item := range []*queuedRequest{active, pending} {
		if item == nil {
			continue
		}
		for _, waiter := range item.waiters {
			waiter <- context.Canceled
			close(waiter)
		}
	}
}

func (q *RequestQueue) wake() {
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func mergeRequests(a, b domain.ReconcileRequest) domain.ReconcileRequest {
	if b.Priority > a.Priority {
		a.Priority = b.Priority
	}
	a.Scope = mergeScopes(a.Scope, b.Scope)
	return a
}

func mergeScopes(a, b domain.ReconcileScope) domain.ReconcileScope {
	out := a
	out.MinSequence = minNonZero(a.MinSequence, b.MinSequence)
	if b.MaxSequence > out.MaxSequence {
		out.MaxSequence = b.MaxSequence
	}
	out.ReasonCodes = mergeStrings(a.ReasonCodes, b.ReasonCodes)
	if a.Mode == domain.ReconcileFull || b.Mode == domain.ReconcileFull {
		out.Mode = domain.ReconcileFull
		out.Paths = nil
		out.Root = ""
		return out
	}
	if a.Mode == domain.ReconcileSubtree || b.Mode == domain.ReconcileSubtree {
		out.Mode = domain.ReconcileSubtree
		out.Paths = nil
		out.Root = commonRoot(rootForMerge(a), rootForMerge(b))
		return out
	}
	out.Mode = domain.ReconcilePaths
	out.Paths = mergeStrings(a.Paths, b.Paths)
	return out
}

func rootForMerge(scope domain.ReconcileScope) string {
	if scope.Mode == domain.ReconcileSubtree {
		if scope.Root == "" {
			return "."
		}
		return scope.Root
	}
	if len(scope.Paths) == 0 {
		return "."
	}
	root := ""
	for _, path := range scope.Paths {
		root = commonRoot(root, parent(path))
	}
	if root == "" {
		return "."
	}
	return root
}

func mergeStrings(a, b []string) []string {
	set := make(map[string]struct{}, len(a)+len(b))
	for _, value := range a {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	for _, value := range b {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func minNonZero(a, b uint64) uint64 {
	if a == 0 {
		return b
	}
	if b == 0 || a < b {
		return a
	}
	return b
}
