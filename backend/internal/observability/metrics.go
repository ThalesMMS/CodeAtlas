package observability

import (
	"sync"
	"sync/atomic"
	"time"
)

// Metrics is a thread-safe, in-memory operational snapshot source. It holds only
// low-cardinality counters and gauges — never per-path/per-query/per-request
// labels — so it cannot grow unbounded. A nil *Metrics is a valid no-op, so
// instrumentation is safe to call even when metrics are disabled.
type Metrics struct {
	indexScans    atomic.Int64
	indexSuccess  atomic.Int64
	indexFailure  atomic.Int64
	llmCalls      atomic.Int64
	llmSuccess    atomic.Int64
	llmFailure    atomic.Int64
	commits       atomic.Int64
	commitsFailed atomic.Int64
	sseDropped    atomic.Int64
	journalReco   atomic.Int64
	httpRequests  atomic.Int64

	mu                 sync.RWMutex
	lastScanDurationMS int64
	lastScanAt         time.Time
	lastScanOK         bool
	indexSize          int64
	readinessState     string
}

// NewMetrics returns an empty collector.
func NewMetrics() *Metrics { return &Metrics{} }

// MetricsSnapshot is a copy-safe view for JSON serialization; it holds no locks.
type MetricsSnapshot struct {
	IndexScansTotal    int64     `json:"indexScansTotal"`
	IndexSuccessTotal  int64     `json:"indexSuccessTotal"`
	IndexFailureTotal  int64     `json:"indexFailureTotal"`
	LLMCallsTotal      int64     `json:"llmCallsTotal"`
	LLMSuccessTotal    int64     `json:"llmSuccessTotal"`
	LLMFailureTotal    int64     `json:"llmFailureTotal"`
	CommitsTotal       int64     `json:"commitsTotal"`
	CommitsFailedTotal int64     `json:"commitsFailedTotal"`
	SSEEventsDropped   int64     `json:"sseEventsDropped"`
	JournalRecoveries  int64     `json:"journalRecoveries"`
	HTTPRequestsTotal  int64     `json:"httpRequestsTotal"`
	LastScanDurationMS int64     `json:"lastScanDurationMs"`
	LastScanAt         time.Time `json:"lastScanAt"`
	LastScanOK         bool      `json:"lastScanOk"`
	IndexSize          int64     `json:"indexSize"`
	ReadinessState     string    `json:"readinessState"`
}

func (m *Metrics) IndexScanStarted() {
	if m == nil {
		return
	}
	m.indexScans.Add(1)
}

func (m *Metrics) IndexScanSucceeded(duration time.Duration, indexSize int, at time.Time) {
	if m == nil {
		return
	}
	m.indexSuccess.Add(1)
	m.mu.Lock()
	m.lastScanDurationMS = duration.Milliseconds()
	m.lastScanAt = at
	m.lastScanOK = true
	m.indexSize = int64(indexSize)
	m.mu.Unlock()
}

func (m *Metrics) IndexScanFailed(duration time.Duration, at time.Time) {
	if m == nil {
		return
	}
	m.indexFailure.Add(1)
	m.mu.Lock()
	m.lastScanDurationMS = duration.Milliseconds()
	m.lastScanAt = at
	m.lastScanOK = false
	m.mu.Unlock()
}

func (m *Metrics) LLMCall(ok bool) {
	if m == nil {
		return
	}
	m.llmCalls.Add(1)
	if ok {
		m.llmSuccess.Add(1)
	} else {
		m.llmFailure.Add(1)
	}
}

func (m *Metrics) Commit(ok bool) {
	if m == nil {
		return
	}
	if ok {
		m.commits.Add(1)
	} else {
		m.commitsFailed.Add(1)
	}
}

func (m *Metrics) SSEEventDropped() {
	if m == nil {
		return
	}
	m.sseDropped.Add(1)
}

func (m *Metrics) JournalRecovered() {
	if m == nil {
		return
	}
	m.journalReco.Add(1)
}

func (m *Metrics) HTTPRequest() {
	if m == nil {
		return
	}
	m.httpRequests.Add(1)
}

func (m *Metrics) SetReadinessState(state string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.readinessState = state
	m.mu.Unlock()
}

// Snapshot returns a consistent copy. It reads atomics individually (eventual
// consistency across counters is acceptable for diagnostics) and the gauge block
// under a short read lock.
func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	m.mu.RLock()
	snapshot := MetricsSnapshot{
		LastScanDurationMS: m.lastScanDurationMS,
		LastScanAt:         m.lastScanAt,
		LastScanOK:         m.lastScanOK,
		IndexSize:          m.indexSize,
		ReadinessState:     m.readinessState,
	}
	m.mu.RUnlock()
	snapshot.IndexScansTotal = m.indexScans.Load()
	snapshot.IndexSuccessTotal = m.indexSuccess.Load()
	snapshot.IndexFailureTotal = m.indexFailure.Load()
	snapshot.LLMCallsTotal = m.llmCalls.Load()
	snapshot.LLMSuccessTotal = m.llmSuccess.Load()
	snapshot.LLMFailureTotal = m.llmFailure.Load()
	snapshot.CommitsTotal = m.commits.Load()
	snapshot.CommitsFailedTotal = m.commitsFailed.Load()
	snapshot.SSEEventsDropped = m.sseDropped.Load()
	snapshot.JournalRecoveries = m.journalReco.Load()
	snapshot.HTTPRequestsTotal = m.httpRequests.Load()
	return snapshot
}
