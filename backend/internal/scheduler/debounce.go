package scheduler

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

type DebounceConfig struct {
	Clock       Clock
	QuietWindow time.Duration
	MaxDelay    time.Duration
	Flush       func(domain.ReconcileRequest)
}

type Debouncer struct {
	mu          sync.Mutex
	clock       Clock
	quietWindow time.Duration
	maxDelay    time.Duration
	resolver    ScopeResolver
	flush       func(domain.ReconcileRequest)
	pending     map[string]domain.ChangeHint
	quietTimer  Timer
	maxTimer    Timer
	requestSeq  atomic.Uint64
	minSequence uint64
	maxSequence uint64
}

func NewDebouncer(cfg DebounceConfig) *Debouncer {
	clock := cfg.Clock
	if clock == nil {
		clock = RealClock()
	}
	if cfg.QuietWindow <= 0 {
		cfg.QuietWindow = 250 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 2 * time.Second
	}
	return &Debouncer{
		clock: clock, quietWindow: cfg.QuietWindow, maxDelay: cfg.MaxDelay,
		resolver: NewScopeResolver(), flush: cfg.Flush, pending: make(map[string]domain.ChangeHint),
	}
}

func (d *Debouncer) Submit(hint domain.ChangeHint) bool {
	if metadataOnly(hint) {
		return false
	}
	path := cleanRelative(hint.Path)
	key := path
	if key == "" {
		key = fmt.Sprintf("__sequence_%d", hint.Sequence)
	}
	hint.Path = path

	d.mu.Lock()
	first := len(d.pending) == 0
	if hint.Sequence > 0 {
		if d.minSequence == 0 || hint.Sequence < d.minSequence {
			d.minSequence = hint.Sequence
		}
		if hint.Sequence > d.maxSequence {
			d.maxSequence = hint.Sequence
		}
	}
	if existing, ok := d.pending[key]; ok {
		existing.Operations |= hint.Operations
		if existing.Kind == "" || existing.Kind == "unknown" {
			existing.Kind = hint.Kind
		}
		if existing.ReasonCode == "" {
			existing.ReasonCode = hint.ReasonCode
		}
		if existing.Sequence == 0 || (hint.Sequence > 0 && hint.Sequence < existing.Sequence) {
			existing.Sequence = hint.Sequence
		}
		d.pending[key] = existing
	} else {
		d.pending[key] = hint
	}
	if first {
		d.quietTimer = d.clock.AfterFunc(d.quietWindow, func() { d.Flush(ReasonDebounce) })
		d.maxTimer = d.clock.AfterFunc(d.maxDelay, func() { d.Flush(ReasonMaxDelay) })
	} else if d.quietTimer != nil {
		d.quietTimer.Reset(d.quietWindow)
	}
	d.mu.Unlock()
	return true
}

func (d *Debouncer) Flush(reason string) bool {
	d.mu.Lock()
	if len(d.pending) == 0 {
		d.mu.Unlock()
		return false
	}
	pending := make([]domain.ChangeHint, 0, len(d.pending))
	for _, hint := range d.pending {
		pending = append(pending, hint)
	}
	minSequence := d.minSequence
	maxSequence := d.maxSequence
	d.pending = make(map[string]domain.ChangeHint)
	d.minSequence = 0
	d.maxSequence = 0
	if d.quietTimer != nil {
		d.quietTimer.Stop()
		d.quietTimer = nil
	}
	if d.maxTimer != nil {
		d.maxTimer.Stop()
		d.maxTimer = nil
	}
	d.mu.Unlock()

	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Path == pending[j].Path {
			return pending[i].Sequence < pending[j].Sequence
		}
		return pending[i].Path < pending[j].Path
	})
	scope := d.resolver.Resolve(pending)
	if minSequence > 0 {
		scope.MinSequence = minSequence
	}
	if maxSequence > 0 {
		scope.MaxSequence = maxSequence
	}
	if reason != "" {
		scope.ReasonCodes = appendReason(scope.ReasonCodes, reason)
	}
	if scope.Mode == domain.ReconcilePaths && len(scope.Paths) == 0 {
		return false
	}
	req := domain.ReconcileRequest{
		ID:        fmt.Sprintf("reconcile-%d", d.requestSeq.Add(1)),
		Scope:     scope,
		CreatedAt: d.clock.Now(),
	}
	if d.flush != nil {
		d.flush(req)
	}
	return true
}

func metadataOnly(hint domain.ChangeHint) bool {
	return hint.Operations == domain.OpChmod
}

func appendReason(reasons []string, reason string) []string {
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	reasons = append(reasons, reason)
	sort.Strings(reasons)
	return reasons
}
