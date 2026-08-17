package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/app"
	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/httpapi"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
)

// syncBuffer is a thread-safe writer for capturing logs while the bootstrap
// goroutine writes concurrently with the test reading them.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---- Fakes (explicit, counter/channel based; no arbitrary sleeps) ----

type fakeProbe struct {
	id    capabilities.CapabilityID
	req   capabilities.Requirement
	state capabilities.CapabilityState
	code  string
}

func (p fakeProbe) ID() capabilities.CapabilityID         { return p.id }
func (p fakeProbe) Requirement() capabilities.Requirement { return p.req }
func (p fakeProbe) Run(context.Context) capabilities.Result {
	return capabilities.Result{ID: p.id, Requirement: p.req, State: p.state, ErrorCode: p.code, CheckedAt: time.Now().UTC()}
}

type fakeProviderProbe struct {
	chat ai.ProviderProbeResult
	emb  ai.ProviderProbeResult
}

func (p fakeProviderProbe) ProbeChat(context.Context) ai.ProviderProbeResult       { return p.chat }
func (p fakeProviderProbe) ProbeEmbeddings(context.Context) ai.ProviderProbeResult { return p.emb }

func availableProbe(id capabilities.CapabilityID) fakeProbe {
	return fakeProbe{id: id, req: capabilities.Required, state: capabilities.CapabilityAvailable}
}

func okChat() ai.ProviderProbeResult { return ai.ProviderProbeResult{Status: ai.ProbeSuccess} }
func okEmb() ai.ProviderProbeResult  { return ai.ProviderProbeResult{Status: ai.ProbeDisabled} }

func failProbe(id capabilities.CapabilityID, code string) fakeProbe {
	return fakeProbe{id: id, req: capabilities.Required, state: capabilities.CapabilityUnavailable, code: code}
}

// ---- Test harness ----

type harness struct {
	coordinator *readiness.Coordinator
	registry    *capabilities.Registry
	baseURL     string
	logs        *syncBuffer
	runErr      chan error
	indexer     *int32
	cancel      context.CancelFunc
}

func newHarness(t *testing.T, deps app.RuntimeDeps, listenFails bool) *harness {
	t.Helper()
	coordinator := readiness.NewCoordinator()
	registry := capabilities.NewRegistry()
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	var indexerStarts int32

	deps.Logger = logger
	deps.Coordinator = coordinator
	deps.Registry = registry
	deps.Server = &http.Server{Handler: httpapi.New(nil, nil, nil, nil, nil, nil, nil, nil, nil, coordinator, registry, logger).Handler()}
	deps.ShutdownTimeout = 3 * time.Second
	if deps.RunIndexer == nil {
		deps.RunIndexer = func(ctx context.Context) {
			atomic.AddInt32(&indexerStarts, 1)
			<-ctx.Done()
		}
	}

	h := &harness{coordinator: coordinator, registry: registry, logs: logs, runErr: make(chan error, 1), indexer: &indexerStarts}

	if listenFails {
		deps.Listen = func() (net.Listener, error) { return nil, errors.New("bind failed: address in use") }
		h.cancel = func() {}
		h.runErr <- app.Run(context.Background(), deps) // synchronous: returns immediately on bind error
		return h
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	h.baseURL = "http://" + listener.Addr().String()
	deps.Listen = func() (net.Listener, error) { return listener, nil }

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.runErr <- app.Run(ctx, deps) }()
	return h
}

func (h *harness) waitForState(t *testing.T, target readiness.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.coordinator.CurrentState() == target {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state = %s, want %s", h.coordinator.CurrentState(), target)
}

func (h *harness) waitForIndexerStarts(t *testing.T, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(h.indexer) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("periodic indexer starts = %d, want %d", atomic.LoadInt32(h.indexer), want)
}

func (h *harness) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case <-h.runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("app.Run did not return after cancellation (goroutine leak)")
	}
}

func httpGet(t *testing.T, url string) (int, []byte) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, body
}

func capabilityState(t *testing.T, registry *capabilities.Registry, id capabilities.CapabilityID) capabilities.CapabilityState {
	t.Helper()
	for _, result := range registry.Results() {
		if result.ID == id {
			return result.State
		}
	}
	return ""
}

// ---- The failure matrix ----

func TestStartupFailureMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		probes           []capabilities.Probe
		provider         fakeProviderProbe
		enableEmbeddings bool
		initialIndex     func(context.Context) error
		failingID        capabilities.CapabilityID // capability expected unavailable in registry
		recoverable      bool
	}{
		{
			name:     "workspace illegible",
			probes:   []capabilities.Probe{failProbe(capabilities.CapabilityWorkspace, "WORKSPACE_UNREADABLE")},
			provider: fakeProviderProbe{chat: okChat(), emb: okEmb()}, failingID: capabilities.CapabilityWorkspace,
		},
		{
			name:     "state area not writable",
			probes:   []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace), failProbe(capabilities.CapabilityStateArea, "STATE_AREA_UNWRITABLE")},
			provider: fakeProviderProbe{chat: okChat(), emb: okEmb()}, failingID: capabilities.CapabilityStateArea,
		},
		{
			name:     "snapshot corrupted",
			probes:   []capabilities.Probe{failProbe(capabilities.CapabilityStore, "STORE_CORRUPTED")},
			provider: fakeProviderProbe{chat: okChat(), emb: okEmb()}, failingID: capabilities.CapabilityStore,
		},
		{
			name:     "tree-sitter go fails",
			probes:   []capabilities.Probe{failProbe(capabilities.CapabilityTreeSitterGo, "TREE_SITTER_PARSE_FAILED")},
			provider: fakeProviderProbe{chat: okChat(), emb: okEmb()}, failingID: capabilities.CapabilityTreeSitterGo,
		},
		{
			name:     "tree-sitter tsx fails",
			probes:   []capabilities.Probe{failProbe(capabilities.CapabilityTreeSitterTSX, "TREE_SITTER_UNEXPECTED_ROOT")},
			provider: fakeProviderProbe{chat: okChat(), emb: okEmb()}, failingID: capabilities.CapabilityTreeSitterTSX,
		},
		{
			name:      "chat endpoint unreachable",
			probes:    []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
			provider:  fakeProviderProbe{chat: ai.ProviderProbeResult{Status: ai.ProbeFailure, ErrorCode: ai.CodeProviderUnreachable}, emb: okEmb()},
			failingID: "llm-chat", recoverable: true,
		},
		{
			name:      "llm auth invalid",
			probes:    []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
			provider:  fakeProviderProbe{chat: ai.ProviderProbeResult{Status: ai.ProbeFailure, ErrorCode: ai.CodeProviderUnauthorized}, emb: okEmb()},
			failingID: "llm-chat", recoverable: true,
		},
		{
			name:      "chat model invalid",
			probes:    []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
			provider:  fakeProviderProbe{chat: ai.ProviderProbeResult{Status: ai.ProbeFailure, ErrorCode: ai.CodeChatModelInvalid}, emb: okEmb()},
			failingID: "llm-chat", recoverable: true,
		},
		{
			name:             "embeddings enabled without model",
			probes:           []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
			provider:         fakeProviderProbe{chat: okChat(), emb: ai.ProviderProbeResult{Status: ai.ProbeFailure, ErrorCode: ai.CodeEmbeddingModelMissing}},
			enableEmbeddings: true, failingID: "llm-embeddings", recoverable: true,
		},
		{
			name:             "embeddings endpoint invalid",
			probes:           []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
			provider:         fakeProviderProbe{chat: okChat(), emb: ai.ProviderProbeResult{Status: ai.ProbeFailure, ErrorCode: ai.CodeEmbeddingModelInvalid}},
			enableEmbeddings: true, failingID: "llm-embeddings", recoverable: true,
		},
		{
			name:             "embedding dimension invalid",
			probes:           []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
			provider:         fakeProviderProbe{chat: okChat(), emb: ai.ProviderProbeResult{Status: ai.ProbeFailure, ErrorCode: ai.CodeEmbeddingResponseInvalid}},
			enableEmbeddings: true, failingID: "llm-embeddings", recoverable: true,
		},
		{
			name:         "initial index read error",
			probes:       []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
			provider:     fakeProviderProbe{chat: okChat(), emb: okEmb()},
			initialIndex: func(context.Context) error { return errors.New("read error") },
		},
		{
			name:         "initial index parse error",
			probes:       []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
			provider:     fakeProviderProbe{chat: okChat(), emb: okEmb()},
			initialIndex: func(context.Context) error { return errors.New("parse error") },
		},
		{
			name:         "initial persist failing",
			probes:       []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
			provider:     fakeProviderProbe{chat: okChat(), emb: okEmb()},
			initialIndex: func(context.Context) error { return errors.New("persist failed") },
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			index := tc.initialIndex
			if index == nil {
				index = func(context.Context) error { return nil }
			}
			h := newHarness(t, app.RuntimeDeps{
				Probes:           tc.probes,
				ProviderProbe:    tc.provider,
				EnableEmbeddings: tc.enableEmbeddings,
				InitialIndex:     index,
			}, false)
			defer h.stop(t)

			expectedState := readiness.StateFailed
			if tc.recoverable {
				expectedState = readiness.StateAwaitingConfiguration
			}
			h.waitForState(t, expectedState)

			// /live always answers 200.
			if status, _ := httpGet(t, h.baseURL+"/api/health/live"); status != http.StatusOK {
				t.Fatalf("/live status = %d, want 200", status)
			}
			// /ready answers 503 while not READY.
			if status, _ := httpGet(t, h.baseURL+"/api/health/ready"); status != http.StatusServiceUnavailable {
				t.Fatalf("/ready status = %d, want 503", status)
			}
			// Diagnostic stats remain available while the runtime is not READY.
			status, body := httpGet(t, h.baseURL+"/api/stats")
			if status != http.StatusOK || !strings.Contains(string(body), `"readinessState":"`+string(expectedState)+`"`) {
				t.Fatalf("/api/stats status=%d body=%s, want 200 readinessState %s", status, body, expectedState)
			}
			// The periodic indexer must not have started.
			if atomic.LoadInt32(h.indexer) != 0 {
				t.Fatal("periodic indexer started after a failed bootstrap")
			}
			// The failing capability is exposed as unavailable.
			if tc.failingID != "" {
				if got := capabilityState(t, h.registry, tc.failingID); got != capabilities.CapabilityUnavailable {
					t.Fatalf("capability %s state = %q, want unavailable", tc.failingID, got)
				}
			}
			// Logs never leak auth headers.
			if strings.Contains(h.logs.String(), "Bearer ") {
				t.Fatalf("logs leaked an Authorization header: %s", h.logs.String())
			}
		})
	}
}

func TestStartupBindErrorIsFatal(t *testing.T) {
	t.Parallel()
	h := newHarness(t, app.RuntimeDeps{
		Probes:        []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
		ProviderProbe: fakeProviderProbe{chat: okChat(), emb: okEmb()},
		InitialIndex:  func(context.Context) error { return nil },
	}, true)
	select {
	case err := <-h.runErr:
		if err == nil {
			t.Fatal("Run returned nil for a bind failure, want error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on bind failure")
	}
	if h.coordinator.CurrentState() == readiness.StateReady {
		t.Fatal("reached READY despite a bind failure")
	}
}

func TestRunNotifiesActualListenerBeforeServing(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	notified := make(chan net.Addr, 1)
	served := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	})}
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, app.RuntimeDeps{
			Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
			Coordinator:       readiness.NewCoordinator(),
			Registry:          capabilities.NewRegistry(),
			Probes:            []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
			ProviderProbe:     fakeProviderProbe{chat: okChat(), emb: okEmb()},
			MigrateStore:      func(context.Context) error { return nil },
			InitialIndex:      func(context.Context) error { return nil },
			RunIndexer:        func(runCtx context.Context) { <-runCtx.Done() },
			Server:            server,
			Listen:            func() (net.Listener, error) { return listener, nil },
			OnListening:       func(addr net.Addr) { notified <- addr },
			ShutdownTimeout:   3 * time.Second,
		})
	}()

	addr := <-notified
	if addr.String() != listener.Addr().String() {
		t.Fatalf("notified address = %q, want %q", addr, listener.Addr())
	}
	response, err := http.Get("http://" + addr.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	<-served
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStartupRunsStoreMigrationBetweenProbesAndIndexing(t *testing.T) {
	t.Parallel()
	migrationStarted := make(chan struct{})
	allowMigration := make(chan struct{})
	var indexed int32
	h := newHarness(t, app.RuntimeDeps{
		Probes:        []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
		ProviderProbe: fakeProviderProbe{chat: okChat(), emb: okEmb()},
		MigrateStore: func(ctx context.Context) error {
			close(migrationStarted)
			select {
			case <-allowMigration:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		InitialIndex: func(context.Context) error {
			atomic.AddInt32(&indexed, 1)
			return nil
		},
	}, false)
	defer h.stop(t)

	select {
	case <-migrationStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("migration hook did not start")
	}
	h.waitForState(t, readiness.StateMigratingStore)
	status, body := httpGet(t, h.baseURL+"/api/search")
	if status != http.StatusServiceUnavailable || !strings.Contains(string(body), `"state":"MIGRATING_STORE"`) {
		t.Fatalf("/api/search during migration status=%d body=%s, want 503 MIGRATING_STORE", status, body)
	}
	if atomic.LoadInt32(&indexed) != 0 {
		t.Fatal("initial index ran before store migration completed")
	}

	close(allowMigration)
	h.waitForState(t, readiness.StateReady)
	if atomic.LoadInt32(&indexed) != 1 {
		t.Fatalf("initial index count = %d, want 1", indexed)
	}
}

func TestStartupHappyPathReachesReady(t *testing.T) {
	t.Parallel()
	var indexed int32
	h := newHarness(t, app.RuntimeDeps{
		Probes: []capabilities.Probe{
			availableProbe(capabilities.CapabilityWorkspace),
			availableProbe(capabilities.CapabilityStore),
		},
		ProviderProbe: fakeProviderProbe{chat: okChat(), emb: okEmb()},
		InitialIndex:  func(context.Context) error { atomic.StoreInt32(&indexed, 1); return nil },
	}, false)
	defer h.stop(t)

	h.waitForState(t, readiness.StateReady)
	h.waitForIndexerStarts(t, 1)

	if atomic.LoadInt32(&indexed) != 1 {
		t.Fatal("reached READY without running the initial index")
	}
	if status, _ := httpGet(t, h.baseURL+"/api/health/ready"); status != http.StatusOK {
		t.Fatalf("/ready status = %d, want 200", status)
	}
	// Functional routes are no longer gated (handler may 500 on nil deps, but not 503 APP_NOT_READY).
	status, body := httpGet(t, h.baseURL+"/api/stats")
	if status == http.StatusServiceUnavailable && strings.Contains(string(body), "APP_NOT_READY") {
		t.Fatal("functional route still gated after READY")
	}
}

func TestStartupCancellationDuringProbe(t *testing.T) {
	t.Parallel()
	probeStarted := make(chan struct{})
	h := newHarness(t, app.RuntimeDeps{
		Probes:        []capabilities.Probe{&blockingProbe{started: probeStarted}},
		ProviderProbe: fakeProviderProbe{chat: okChat(), emb: okEmb()},
		InitialIndex:  func(context.Context) error { return nil },
	}, false)
	<-probeStarted
	h.cancel()
	select {
	case <-h.runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation during probe")
	}
	if h.coordinator.CurrentState() == readiness.StateReady {
		t.Fatal("reached READY despite cancellation during probe")
	}
}

func TestStartupCancellationDuringIndexing(t *testing.T) {
	t.Parallel()
	indexStarted := make(chan struct{})
	h := newHarness(t, app.RuntimeDeps{
		Probes:        []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
		ProviderProbe: fakeProviderProbe{chat: okChat(), emb: okEmb()},
		InitialIndex: func(ctx context.Context) error {
			close(indexStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	}, false)
	<-indexStarted
	h.cancel()
	select {
	case <-h.runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation during indexing")
	}
	if h.coordinator.CurrentState() == readiness.StateReady {
		t.Fatal("reached READY despite cancellation during indexing")
	}
}

// A failing embedding reconciliation (e.g. an incompatible dense index that
// cannot be rebuilt) must drive readiness to FAILED rather than READY, so dense
// retrieval is never promised over a mismatched index.
func TestStartupReconcileFailureIsFatal(t *testing.T) {
	t.Parallel()
	var reconciled int32
	h := newHarness(t, app.RuntimeDeps{
		Probes: []capabilities.Probe{
			availableProbe(capabilities.CapabilityWorkspace),
			availableProbe(capabilities.CapabilityStore),
		},
		ProviderProbe: fakeProviderProbe{chat: okChat(), emb: okEmb()},
		InitialIndex:  func(context.Context) error { return nil },
		ReconcileEmbeddings: func(context.Context) error {
			atomic.StoreInt32(&reconciled, 1)
			return errors.New("incompatible dense index")
		},
	}, false)
	defer h.stop(t)

	h.waitForState(t, readiness.StateFailed)
	if atomic.LoadInt32(&reconciled) != 1 {
		t.Fatal("reconcile seam was not invoked before FAILED")
	}
	if h.coordinator.CurrentState() == readiness.StateReady {
		t.Fatal("reached READY despite a failed embedding reconciliation")
	}
}

// A successful reconciliation runs after indexing and before READY.
func TestStartupReconcileSuccessReachesReady(t *testing.T) {
	t.Parallel()
	var indexed, reconciled int32
	h := newHarness(t, app.RuntimeDeps{
		Probes: []capabilities.Probe{
			availableProbe(capabilities.CapabilityWorkspace),
			availableProbe(capabilities.CapabilityStore),
		},
		ProviderProbe: fakeProviderProbe{chat: okChat(), emb: okEmb()},
		InitialIndex:  func(context.Context) error { atomic.StoreInt32(&indexed, 1); return nil },
		ReconcileEmbeddings: func(context.Context) error {
			if atomic.LoadInt32(&indexed) != 1 {
				t.Error("reconcile ran before the initial index")
			}
			atomic.StoreInt32(&reconciled, 1)
			return nil
		},
	}, false)
	defer h.stop(t)

	h.waitForState(t, readiness.StateReady)
	if atomic.LoadInt32(&reconciled) != 1 {
		t.Fatal("reached READY without running the embedding reconciliation")
	}
}

// blockingProbe signals when it starts and blocks until the context is cancelled.
type blockingProbe struct {
	started chan struct{}
	once    atomic.Bool
}

func (p *blockingProbe) ID() capabilities.CapabilityID         { return capabilities.CapabilityWorkspace }
func (p *blockingProbe) Requirement() capabilities.Requirement { return capabilities.Required }
func (p *blockingProbe) Run(ctx context.Context) capabilities.Result {
	if p.once.CompareAndSwap(false, true) {
		close(p.started)
	}
	<-ctx.Done()
	return capabilities.Result{ID: capabilities.CapabilityWorkspace, Requirement: capabilities.Required, State: capabilities.CapabilityUnavailable, ErrorCode: capabilities.ErrCodeCancelled}
}

func TestReadyQueriedDuringTransitionsIsConsistent(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	h := newHarness(t, app.RuntimeDeps{
		Probes:        []capabilities.Probe{availableProbe(capabilities.CapabilityWorkspace)},
		ProviderProbe: fakeProviderProbe{chat: okChat(), emb: okEmb()},
		InitialIndex: func(context.Context) error {
			<-release // hold the bootstrap in INDEXING while /ready is polled
			return nil
		},
	}, false)
	defer h.stop(t)

	// Deterministically pause in INDEXING before the concurrent assault begins.
	h.waitForState(t, readiness.StateIndexing)

	// Concurrently hammer /ready while the bootstrap advances to READY.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				status, body := httpGet(t, h.baseURL+"/api/health/ready")
				var parsed struct {
					Status string `json:"status"`
				}
				_ = json.Unmarshal(body, &parsed)
				// The contract must always hold: 200 iff status=="ready".
				if (status == http.StatusOK) != (parsed.Status == "ready") {
					t.Errorf("inconsistent /ready: http=%d status=%q", status, parsed.Status)
					return
				}
			}
		}
	}()

	close(release)
	h.waitForState(t, readiness.StateReady)
	close(stop)
	<-done
}
