package capabilities

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedProbe is a configurable Probe used to exercise the runner without
// touching the real environment.
type scriptedProbe struct {
	id        CapabilityID
	req       Requirement
	sleep     time.Duration
	willPanic bool
	onRun     func()
}

func (p scriptedProbe) ID() CapabilityID         { return p.id }
func (p scriptedProbe) Requirement() Requirement { return p.req }

func (p scriptedProbe) Run(ctx context.Context) Result {
	if p.onRun != nil {
		p.onRun()
	}
	if p.willPanic {
		panic("scripted panic")
	}
	if p.sleep > 0 {
		time.Sleep(p.sleep) // deliberately ignores ctx to test enforced timeouts
	}
	return Result{ID: p.id, Requirement: p.req, State: CapabilityAvailable, CheckedAt: time.Now().UTC()}
}

func TestRunnerEmptyProbes(t *testing.T) {
	t.Parallel()
	if got := (Runner{}).Run(context.Background(), nil, nil); got != nil {
		t.Fatalf("Run(nil) = %#v, want nil", got)
	}
}

func TestRunnerDeterministicOrder(t *testing.T) {
	t.Parallel()
	// Completion order is scrambled (a is slowest), but result order must be by ID.
	probes := []Probe{
		scriptedProbe{id: "c", req: Required, sleep: 5 * time.Millisecond},
		scriptedProbe{id: "a", req: Required, sleep: 25 * time.Millisecond},
		scriptedProbe{id: "b", req: Required, sleep: 15 * time.Millisecond},
	}
	results := Runner{MaxConcurrency: 8}.Run(context.Background(), probes, nil)
	got := []CapabilityID{results[0].ID, results[1].ID, results[2].ID}
	want := []CapabilityID{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestRunnerRecoversPanic(t *testing.T) {
	t.Parallel()
	probes := []Probe{scriptedProbe{id: "boom", req: Required, willPanic: true}}
	results := Runner{}.Run(context.Background(), probes, nil)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].State != CapabilityUnavailable || results[0].ErrorCode != ErrCodePanic {
		t.Fatalf("panic result = %#v, want unavailable/%s", results[0], ErrCodePanic)
	}
}

func TestRunnerEnforcesPerProbeTimeout(t *testing.T) {
	t.Parallel()
	probes := []Probe{scriptedProbe{id: "slow", req: Required, sleep: 500 * time.Millisecond}}
	results := Runner{PerProbeTimeout: 20 * time.Millisecond}.Run(context.Background(), probes, nil)
	if results[0].State != CapabilityUnavailable || results[0].ErrorCode != ErrCodeTimeout {
		t.Fatalf("timeout result = %#v, want unavailable/%s", results[0], ErrCodeTimeout)
	}
}

func TestRunnerGlobalTimeoutCancelsQueued(t *testing.T) {
	t.Parallel()
	probes := []Probe{
		scriptedProbe{id: "a", req: Required, sleep: 200 * time.Millisecond},
		scriptedProbe{id: "b", req: Required, sleep: 200 * time.Millisecond},
		scriptedProbe{id: "c", req: Required, sleep: 200 * time.Millisecond},
	}
	results := Runner{GlobalTimeout: 30 * time.Millisecond, MaxConcurrency: 1}.Run(context.Background(), probes, nil)
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	for _, result := range results {
		if result.State != CapabilityUnavailable {
			t.Fatalf("result %s = %#v, want unavailable under global timeout", result.ID, result)
		}
	}
}

func TestRunnerRespectsParentCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probes := []Probe{scriptedProbe{id: "a", req: Required, sleep: time.Second}}
	results := Runner{MaxConcurrency: 1}.Run(ctx, probes, nil)
	if results[0].State != CapabilityUnavailable {
		t.Fatalf("result = %#v, want unavailable for cancelled parent", results[0])
	}
}

type recordingReceiver struct {
	mu      sync.Mutex
	results []Result
}

func (r *recordingReceiver) UpdateCapability(result Result) {
	r.mu.Lock()
	r.results = append(r.results, result)
	r.mu.Unlock()
}

func TestRunnerNotifiesReceiver(t *testing.T) {
	t.Parallel()
	probes := []Probe{
		scriptedProbe{id: "a", req: Required},
		scriptedProbe{id: "b", req: Required},
	}
	receiver := &recordingReceiver{}
	results := Runner{}.Run(context.Background(), probes, receiver)
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if len(receiver.results) != 2 {
		t.Fatalf("receiver got %d results, want 2", len(receiver.results))
	}
}

func TestRunnerRespectsConcurrencyLimit(t *testing.T) {
	t.Parallel()
	const limit = 2
	var current, maxSeen int32
	enter := func() {
		now := atomic.AddInt32(&current, 1)
		for {
			peak := atomic.LoadInt32(&maxSeen)
			if now <= peak || atomic.CompareAndSwapInt32(&maxSeen, peak, now) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&current, -1)
	}
	probes := make([]Probe, 0, 8)
	for i := 0; i < 8; i++ {
		probes = append(probes, scriptedProbe{id: CapabilityID(string(rune('a' + i))), req: Required, onRun: enter})
	}
	Runner{MaxConcurrency: limit}.Run(context.Background(), probes, nil)
	if maxSeen > limit {
		t.Fatalf("observed concurrency %d, want <= %d", maxSeen, limit)
	}
}
