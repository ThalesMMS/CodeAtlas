package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	"github.com/ThalesMMS/CodeAtlas/internal/job"
	"github.com/ThalesMMS/CodeAtlas/internal/mutation"
	"github.com/ThalesMMS/CodeAtlas/internal/observability"
	"github.com/ThalesMMS/CodeAtlas/internal/overlay"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/parsesession"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
	"github.com/ThalesMMS/CodeAtlas/internal/scheduler"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
	"github.com/ThalesMMS/CodeAtlas/internal/webui"
)

type Server struct {
	workspace      *service.Workspace
	store          repository.Store
	indexer        *indexer.Indexer
	scheduler      scheduler.ManualReconciler
	mutations      mutation.Registry
	retriever      *retrieval.Hybrid
	explainer      *service.Explainer
	navigation     *service.NavigationService
	semanticSync   semantic.DocumentSemanticSync
	semanticEditor *service.SemanticEditorService
	codemaps       *service.CodemapService
	deepWiki       *service.DeepWikiService
	saver          *service.WorkspaceCommitCoordinator
	overlays       *overlay.Store
	parseSessions  *parsesession.Manager
	overlayParses  *overlayParseCache
	provider       ai.Provider
	jobs           *job.Manager
	readiness      ReadinessReader
	capabilities   CapabilityReader
	logger         *slog.Logger
	metrics        *observability.Metrics
	events         *sseBroker
}

const documentLeaseTTL = 30 * time.Minute

// SetMetrics injects the in-memory metrics collector exposed via /api/stats. A
// nil collector is a no-op.
func (s *Server) SetMetrics(metrics *observability.Metrics) {
	s.metrics = metrics
	if s.events == nil {
		return
	}
	if metrics == nil {
		s.events.SetDropObserver(nil)
		return
	}
	s.events.SetDropObserver(metrics.SSEEventDropped)
}

func (s *Server) SetScheduler(reconciler scheduler.ManualReconciler) {
	s.scheduler = reconciler
}

// ScheduleEmbeddingRebuild submits the same coalescing job used by the public
// maintenance API. Runtime settings activation uses this narrow surface so
// concurrent incompatible changes reuse the stable repository rebuild key.
func (s *Server) ScheduleEmbeddingRebuild(ctx context.Context) (domain.JobID, error) {
	snapshot, err := s.submitReindexJob(ctx, "embeddings.rebuild", "embeddings:repository", "")
	if err != nil {
		return "", err
	}
	return snapshot.ID, nil
}

// SetMutationRegistry exposes internal-write correlation status in diagnostics.
func (s *Server) SetMutationRegistry(registry mutation.Registry) {
	s.mutations = registry
}

// SetSemanticProvider enables semantic navigation and, when supported by the
// provider, keeps language-server documents synchronized with editor overlays.
func (s *Server) SetSemanticProvider(provider semantic.SemanticProvider) {
	s.navigation.SetSemanticProvider(provider)
	s.semanticEditor.SetSemanticProvider(provider)
	if syncer, ok := provider.(semantic.DocumentSemanticSync); ok {
		s.semanticSync = syncer
	}
}

func (s *Server) publishIndexEvent(event domain.IndexEvent) {
	if s.indexer == nil {
		return
	}
	broker := s.indexer.Broker()
	if broker == nil {
		return
	}
	broker.Publish(event)
}

func New(
	workspace *service.Workspace,
	store repository.Store,
	indexer *indexer.Indexer,
	retriever *retrieval.Hybrid,
	explainer *service.Explainer,
	codemaps *service.CodemapService,
	deepWiki *service.DeepWikiService,
	saver *service.WorkspaceCommitCoordinator,
	provider ai.Provider,
	readiness ReadinessReader,
	capabilities CapabilityReader,
	logger *slog.Logger,
) *Server {
	baseSnapshot := func() domain.SnapshotID { return "" }
	if store != nil {
		baseSnapshot = store.SnapshotID
	}
	overlays := overlay.NewStore(workspace, detectOverlayLanguage, baseSnapshot, nil, overlay.Config{LeaseTTL: documentLeaseTTL})
	parseSessions := parsesession.NewManager(0, contenthash.HashContent, nil)
	jobs := job.NewManager(job.DefaultConfig())
	events := newSSEBroker(defaultSSEHistory)
	server := &Server{
		workspace: workspace, store: store, indexer: indexer, retriever: retriever,
		explainer: explainer, navigation: service.NewNavigationService(store, workspace), semanticEditor: service.NewSemanticEditorService(store, overlays, parseSessions), codemaps: codemaps, deepWiki: deepWiki, saver: saver,
		overlays: overlays, parseSessions: parseSessions, overlayParses: newOverlayParseCache(), jobs: jobs, events: events,
		provider: provider, readiness: readiness, capabilities: capabilities, logger: logger,
	}
	if indexer != nil {
		indexer.SetWorkspaceChangeSink(server.handlePublishedWorkspaceChange)
		indexer.Broker().SetSink(events.PublishIndex)
	}
	overlays.SetExpireHandler(server.handleExpiredOverlay)
	jobs.SetEventSink(events.PublishJob)
	return server
}

// detectOverlayLanguage adapts the parser's language detection to the overlay
// store's DetectFunc.
func detectOverlayLanguage(path string) (string, bool) {
	_, language, supported := codeparser.DetectLanguage(path)
	return language, supported
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleLive)
	mux.HandleFunc("GET /api/health/live", s.handleLive)
	mux.HandleFunc("GET /api/health/ready", s.handleReady)
	mux.HandleFunc("GET /api/capabilities", s.handleCapabilities)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("GET /api/file", s.handleGetFile)
	mux.HandleFunc("PUT /api/file", s.handlePutFile)
	mux.HandleFunc("POST /api/documents/open", s.handleOpenDocument)
	mux.HandleFunc("PUT /api/documents/{documentId}/content", s.handleReplaceDocument)
	mux.HandleFunc("GET /api/documents/{documentId}", s.handleGetDocument)
	mux.HandleFunc("POST /api/documents/{documentId}/renew", s.handleRenewDocument)
	mux.HandleFunc("POST /api/documents/{documentId}/reclaim", s.handleReclaimDocument)
	mux.HandleFunc("GET /api/documents/{documentId}/diagnostics", s.handleDocumentDiagnostics)
	mux.HandleFunc("GET /api/documents/{documentId}/semantic-tokens", s.handleDocumentSemanticTokens)
	mux.HandleFunc("POST /api/documents/{documentId}/save", s.handleSaveDocument)
	mux.HandleFunc("POST /api/documents/{documentId}/resolve-conflict", s.handleResolveDocumentConflict)
	mux.HandleFunc("DELETE /api/documents/{documentId}", s.handleDeleteDocument)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("POST /api/explain", s.handleExplain)
	mux.HandleFunc("POST /api/navigation/query", s.handleNavigationQuery)
	mux.HandleFunc("POST /api/jobs", s.handleCreateJob)
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/jobs/{jobId}", s.handleGetJob)
	mux.HandleFunc("DELETE /api/jobs/{jobId}", s.handleCancelJob)
	mux.HandleFunc("POST /api/jobs/{jobId}/cancel", s.handleCancelJob)
	mux.HandleFunc("GET /api/jobs/{jobId}/result", s.handleJobResult)
	mux.HandleFunc("POST /api/codemaps", s.handleCodemap)
	mux.HandleFunc("GET /api/codemaps/{artifactId}", s.handleGetCodemap)
	mux.HandleFunc("GET /api/deepwiki", s.handleWikiPages)
	mux.HandleFunc("POST /api/deepwiki/refresh", s.handleWikiRefresh)
	mux.HandleFunc("POST /api/reindex", s.handleReindex)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("/api/", s.handleMissingAPI)
	mux.Handle("/", webui.HandlerWithCSP(frontendCSP))
	return s.middleware(s.readinessGate(mux))
}

func (s *Server) handleMissingAPI(response http.ResponseWriter, _ *http.Request) {
	http.NotFound(response, nil)
}

// requestIDKey is the private context key holding the per-request id.
type requestIDKey struct{}

// statusRecorder tracks whether headers were written so a panic after a partial
// write does not double-write. It does not buffer the body, so streaming (SSE)
// keeps working.
type statusRecorder struct {
	http.ResponseWriter
	wroteHeader bool
	status      int
	bytes       int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush keeps the SSE handler working through the recorder.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying connection through
// the middleware recorder (required for per-handler deadlines).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		// Honor a well-formed client X-Request-ID for cross-tier correlation,
		// otherwise mint a fresh one. The effective id is always echoed back.
		id := observability.AcceptRequestID(request.Header.Get("X-Request-ID"))
		request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, id))
		recorder := &statusRecorder{ResponseWriter: response}
		response.Header().Set("X-Request-ID", id)
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Content-Security-Policy", frontendCSP(""))
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("HTTP panic", "error", recovered, "path", request.URL.Path, "requestId", id)
				// Only emit an envelope if nothing was written yet.
				if !recorder.wroteHeader {
					s.writeAppError(recorder, request, apperror.InternalError(fmt.Errorf("panic: %v", recovered)))
				}
			}
			s.metrics.HTTPRequest()
			s.logAccess(request, recorder, id, time.Since(started))
		}()
		next.ServeHTTP(recorder, request)
	})
}

func frontendCSP(styleNonce string) string {
	styleElements := "style-src-elem 'self'"
	if nonce := strings.TrimSpace(styleNonce); nonce != "" {
		styleElements += " 'nonce-" + nonce + "'"
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		styleElements,
		"style-src-attr 'unsafe-inline'",
		"img-src 'self' data:",
		"connect-src 'self'",
		"font-src 'self'",
		"worker-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}

// logAccess emits one structured access line per request. High-frequency
// diagnostic/polling routes log at Debug to avoid drowning out real activity; the
// route is the normalized matched pattern, never the raw query (which may be
// sensitive).
func (s *Server) logAccess(request *http.Request, recorder *statusRecorder, requestID string, duration time.Duration) {
	route := request.Pattern
	if route == "" {
		route = request.URL.Path
	}
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	level := slog.LevelInfo
	if isDiagnosticRoute(request.URL.Path) {
		level = slog.LevelDebug
	}
	s.logger.Log(request.Context(), level, "http request",
		"method", request.Method, "route", route, "status", status,
		"durationMs", duration.Milliseconds(), "bytes", recorder.bytes, "requestId", requestID)
}

// isDiagnosticRoute marks high-frequency polling/diagnostic paths and static
// assets, which log at Debug.
func isDiagnosticRoute(path string) bool {
	switch path {
	case "/api/health", "/api/health/live", "/api/health/ready", "/api/stats", "/api/capabilities", "/api/events":
		return true
	}
	return !strings.HasPrefix(path, "/api/")
}

func requestIDFrom(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

func (s *Server) handleStats(response http.ResponseWriter, request *http.Request) {
	if s.store == nil || s.indexer == nil || s.workspace == nil || s.provider == nil {
		snapshot := observability.MetricsSnapshot{}
		if s.metrics != nil {
			snapshot = s.metrics.Snapshot()
		}
		if s.readiness != nil {
			snapshot.ReadinessState = string(s.readiness.CurrentState())
		}
		writeJSON(response, http.StatusOK, struct {
			domain.WorkspaceStats
			Metrics           observability.MetricsSnapshot    `json:"metrics"`
			Frontend          webui.BuildManifest              `json:"frontend"`
			InternalMutations *domain.MutationRegistrySnapshot `json:"internalMutations,omitempty"`
		}{
			WorkspaceStats:    domain.WorkspaceStats{},
			Metrics:           snapshot,
			Frontend:          frontendManifest(),
			InternalMutations: s.mutationSnapshot(),
		})
		return
	}
	files, symbols, edges, wikiPages, indexedAt := s.store.Counts()
	indexing, lastError := s.indexer.Status()
	snapshot := observability.MetricsSnapshot{}
	if s.metrics != nil {
		snapshot = s.metrics.Snapshot()
	}
	if s.readiness != nil {
		snapshot.ReadinessState = string(s.readiness.CurrentState())
	}
	metadata, err := s.store.SnapshotMetadataContext(request.Context())
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		domain.WorkspaceStats
		SnapshotID        domain.SnapshotID                `json:"snapshotId"`
		Revision          domain.Revision                  `json:"revision"`
		Metrics           observability.MetricsSnapshot    `json:"metrics"`
		Frontend          webui.BuildManifest              `json:"frontend"`
		Scheduler         *domain.SchedulerState           `json:"scheduler,omitempty"`
		InternalMutations *domain.MutationRegistrySnapshot `json:"internalMutations,omitempty"`
	}{
		WorkspaceStats: domain.WorkspaceStats{
			Workspace: s.workspace.Root(), Files: files, Symbols: symbols, Edges: edges,
			WikiPages: wikiPages, IndexedAt: indexedAt, Indexing: indexing, LastError: lastError,
			LLMProvider: s.provider.Name(),
		},
		SnapshotID:        metadata.ID,
		Revision:          metadata.Revision,
		Metrics:           snapshot,
		Frontend:          frontendManifest(),
		Scheduler:         s.schedulerState(),
		InternalMutations: s.mutationSnapshot(),
	})
}

func frontendManifest() webui.BuildManifest {
	manifest, err := webui.Manifest(webui.Assets())
	if err != nil {
		return webui.BuildManifest{}
	}
	return manifest
}

func (s *Server) mutationSnapshot() *domain.MutationRegistrySnapshot {
	if s.mutations == nil {
		return nil
	}
	snapshot := s.mutations.Snapshot()
	snapshot.Entries = nil
	return &snapshot
}

func (s *Server) schedulerState() *domain.SchedulerState {
	if s.scheduler == nil {
		return nil
	}
	state := s.scheduler.State()
	return &state
}

func (s *Server) handleTree(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, s.store.FileTree())
}

func (s *Server) handleSearch(response http.ResponseWriter, request *http.Request) {
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	if query == "" {
		writeJSON(response, http.StatusOK, []domain.SearchHit{})
		return
	}
	hits, err := s.retriever.Search(request.Context(), query, 30)
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, hits)
}

func (s *Server) handleExplain(response http.ResponseWriter, request *http.Request) {
	var payload domain.ExplainRequest
	if err := decodeJSON(response, request, &payload, 64<<10); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	normalized, err := service.NormalizeExplainRequest(payload)
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	payload = normalized
	s.clearWriteDeadline(response, request)
	var explanation domain.Explanation
	if payload.DocumentID != "" {
		// An open document is explained against its unsaved buffer via a composite
		// view, not the persisted file.
		explanation, err = s.explainOverlay(request.Context(), payload)
	} else {
		explanation, err = s.explainer.Explain(request.Context(), payload)
	}
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	if explanation.Ephemeral {
		response.Header().Set("Cache-Control", "no-store")
	}
	writeJSON(response, http.StatusOK, explanation)
}

func (s *Server) handleCodemap(response http.ResponseWriter, request *http.Request) {
	var payload domain.CodemapRequest
	if err := decodeJSON(response, request, &payload, 64<<10); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	snapshot, err := s.submitCodemapJob(request.Context(), payload, "")
	if err != nil {
		s.writeJobError(response, request, err, "")
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"job": snapshot})
}

func (s *Server) handleGetCodemap(response http.ResponseWriter, request *http.Request) {
	codemap, err := s.store.CodemapByArtifactIDContext(request.Context(), domain.ArtifactID(request.PathValue("artifactId")))
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, codemap)
}

func (s *Server) handleWikiPages(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, s.deepWiki.Collection())
}

func (s *Server) handleWikiRefresh(response http.ResponseWriter, request *http.Request) {
	var payload map[string]any
	if request.Body != nil && request.ContentLength != 0 {
		if err := decodeJSON(response, request, &payload, 16<<10); err != nil {
			s.writeAppError(response, request, err)
			return
		}
	}
	snapshot, err := s.submitDeepWikiJob(request.Context(), "")
	if err != nil {
		s.writeJobError(response, request, err, "")
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"job": snapshot})
}

func (s *Server) handleReindex(response http.ResponseWriter, request *http.Request) {
	var payload map[string]any
	if request.Body != nil && request.ContentLength != 0 {
		if err := decodeJSON(response, request, &payload, 16<<10); err != nil {
			s.writeAppError(response, request, err)
			return
		}
	}
	snapshot, err := s.submitReindexJob(request.Context(), "repository.reindex", "repository:full", "")
	if err != nil {
		s.writeJobError(response, request, err, "")
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"job": snapshot})
}

func (s *Server) handleEvents(response http.ResponseWriter, request *http.Request) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		s.writeAppError(response, request, apperror.InternalError(errors.New("streaming unsupported")))
		return
	}
	s.clearWriteDeadline(response, request)
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Connection", "keep-alive")
	eventChannel, replay, resync, cancel := s.events.Subscribe(request.Header.Get("Last-Event-ID"))
	defer cancel()

	if _, err := fmt.Fprint(response, "event: connected\ndata: {\"type\":\"connected\"}\n\n"); err != nil {
		return
	}
	flusher.Flush()
	if resync {
		if _, err := fmt.Fprint(response, "event: resync\ndata: {\"type\":\"resync\",\"reason\":\"event_history_unavailable\"}\n\n"); err != nil {
			return
		}
		flusher.Flush()
	} else {
		for _, event := range replay {
			if writeSSE(response, flusher, event) != nil {
				return
			}
		}
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-eventChannel:
			if !open {
				return
			}
			if writeSSE(response, flusher, event) != nil {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(response, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(response http.ResponseWriter, flusher http.Flusher, event sseEvent) error {
	if _, err := fmt.Fprintf(response, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, event.Data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) clearWriteDeadline(response http.ResponseWriter, request *http.Request) {
	err := http.NewResponseController(response).SetWriteDeadline(time.Time{})
	if err != nil && !errors.Is(err, http.ErrNotSupported) && s.logger != nil {
		s.logger.Warn("failed to clear write deadline", "error", err, "path", request.URL.Path)
	}
}

type createJobRequest struct {
	Type            string          `json:"type"`
	Input           json.RawMessage `json:"input,omitempty"`
	ClientRequestID string          `json:"clientRequestId,omitempty"`
}

func (s *Server) handleCreateJob(response http.ResponseWriter, request *http.Request) {
	var payload createJobRequest
	if err := decodeJSON(response, request, &payload, 64<<10); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	var snapshot domain.JobSnapshot
	var err error
	switch payload.Type {
	case "deepwiki.refresh":
		if err = decodeJobInput(payload.Input, &struct{}{}); err == nil {
			snapshot, err = s.submitDeepWikiJob(request.Context(), payload.ClientRequestID)
		}
	case "codemap.generate":
		var input domain.CodemapRequest
		if err = decodeJobInput(payload.Input, &input); err == nil {
			snapshot, err = s.submitCodemapJob(request.Context(), input, payload.ClientRequestID)
		}
	case "repository.reindex":
		if err = decodeJobInput(payload.Input, &struct{}{}); err == nil {
			snapshot, err = s.submitReindexJob(request.Context(), "repository.reindex", "repository:full", payload.ClientRequestID)
		}
	case "embeddings.rebuild":
		if err = decodeJobInput(payload.Input, &struct{}{}); err == nil {
			snapshot, err = s.submitReindexJob(request.Context(), "embeddings.rebuild", "embeddings:repository", payload.ClientRequestID)
		}
	case "fts.rebuild":
		if err = decodeJobInput(payload.Input, &struct{}{}); err == nil {
			snapshot, err = s.submitReindexJob(request.Context(), "fts.rebuild", "fts:repository", payload.ClientRequestID)
		}
	default:
		err = apperror.JobTypeUnsupported(payload.Type)
	}
	if err != nil {
		if _, ok := apperror.As(err); ok {
			s.writeAppError(response, request, err)
		} else {
			s.writeJobError(response, request, err, "")
		}
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"job": snapshot})
}

func (s *Server) handleGetJob(response http.ResponseWriter, request *http.Request) {
	id := domain.JobID(request.PathValue("jobId"))
	snapshot, err := s.jobs.Get(id)
	if err != nil {
		s.writeJobError(response, request, err, id)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"job": snapshot})
}

func (s *Server) handleListJobs(response http.ResponseWriter, request *http.Request) {
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			s.writeAppError(response, request, apperror.InvalidArgumentMessage("limit", "limit must be between 1 and 200.", err))
			return
		}
		limit = parsed
	}
	filter := domain.JobFilter{Type: request.URL.Query().Get("type"), Limit: limit}
	if state := request.URL.Query().Get("state"); state != "" {
		filter.State = domain.JobState(state)
	}
	writeJSON(response, http.StatusOK, map[string]any{"jobs": s.jobs.List(filter)})
}

func (s *Server) handleCancelJob(response http.ResponseWriter, request *http.Request) {
	id := domain.JobID(request.PathValue("jobId"))
	expected, err := expectedJobRevision(request)
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	snapshot, err := s.jobs.Cancel(request.Context(), id, expected)
	if err != nil {
		s.writeJobError(response, request, err, id)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"job": snapshot})
}

func (s *Server) handleJobResult(response http.ResponseWriter, request *http.Request) {
	id := domain.JobID(request.PathValue("jobId"))
	result, err := s.jobs.Result(id)
	if err != nil {
		s.writeJobError(response, request, err, id)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) submitCodemapJob(ctx context.Context, payload domain.CodemapRequest, clientRequestID string) (domain.JobSnapshot, error) {
	payload, err := service.NormalizeCodemapRequest(payload)
	if err != nil {
		return domain.JobSnapshot{}, err
	}
	key := "codemap:" + hashPublicKey(strings.ToLower(strings.Join(strings.Fields(payload.Query), " "))+"#"+strconv.Itoa(payload.MaxNodes))
	inputSnapshot := domain.SnapshotID("")
	if s.store != nil {
		metadata, err := s.store.SnapshotMetadataContext(ctx)
		if err != nil {
			return domain.JobSnapshot{}, err
		}
		inputSnapshot = metadata.ID
	}
	return s.jobs.Submit(ctx, job.Spec{
		Type: "codemap.generate", Key: key, InputSnapshotID: inputSnapshot, ClientRequestID: clientRequestID, DedupPolicy: job.DedupCoalesce,
		Runner: func(jobCtx context.Context, reporter job.ProgressReporter) (job.Result, error) {
			_ = reporter.Report("retrieve", "montando contexto", domain.Progress{Completed: 1, Total: 6, Unit: "stage"})
			_ = reporter.Report("generate", "generating narrative", domain.Progress{Completed: 4, Total: 6, Unit: "stage"})
			codemap, err := s.codemaps.Generate(jobCtx, payload)
			if err != nil {
				return job.Result{}, err
			}
			_ = reporter.Report("publish", "publishing artifact", domain.Progress{Completed: 6, Total: 6, Unit: "stage"})
			return job.Result{ArtifactID: string(codemap.Artifact.ID), SnapshotID: codemap.SnapshotID, Payload: codemap}, nil
		},
	})
}

func (s *Server) submitDeepWikiJob(ctx context.Context, clientRequestID string) (domain.JobSnapshot, error) {
	inputSnapshot := domain.SnapshotID("")
	if s.store != nil {
		metadata, err := s.store.SnapshotMetadataContext(ctx)
		if err != nil {
			return domain.JobSnapshot{}, err
		}
		inputSnapshot = metadata.ID
	}
	return s.jobs.Submit(ctx, job.Spec{
		Type: "deepwiki.refresh", Key: "deepwiki:repository", InputSnapshotID: inputSnapshot, ClientRequestID: clientRequestID, DedupPolicy: job.DedupCoalesce,
		Runner: func(jobCtx context.Context, reporter job.ProgressReporter) (job.Result, error) {
			_ = reporter.Report("inventory", "inventorying modules", domain.Progress{Completed: 1, Total: 6, Unit: "stage"})
			if _, err := s.deepWiki.GenerateWithProgress(jobCtx, func(progress service.WikiProgress) {
				message := "page completed " + progress.PageSlug
				if progress.Status == service.WikiProgressFailed {
					message = "failed to generate page " + progress.PageSlug
					s.logDeepWikiPageFailure(jobCtx, progress)
				}
				_ = reporter.Report("pages", message, domain.Progress{Completed: int64(progress.Completed), Total: int64(progress.Total), Unit: "page"})
			}); err != nil {
				if errors.Is(err, service.ErrSnapshotChanged) {
					return job.Result{}, job.ErrStale
				}
				return job.Result{}, err
			}
			collection := s.deepWiki.Collection()
			_ = reporter.Report("publish", "publishing pages", domain.Progress{Completed: 6, Total: 6, Unit: "stage"})
			return job.Result{ArtifactID: string(collection.Artifact.ID), SnapshotID: domain.SnapshotID(collection.SnapshotID), Payload: collection}, nil
		},
	})
}

func (s *Server) logDeepWikiPageFailure(ctx context.Context, progress service.WikiProgress) {
	if s.logger == nil || progress.Err == nil {
		return
	}
	cause := progress.Err
	code := "JOB_FAILED"
	if appErr, ok := apperror.As(progress.Err); ok {
		code = string(appErr.Code)
		if appErr.Cause != nil {
			cause = appErr.Cause
		}
	}
	s.logger.ErrorContext(ctx, "DeepWiki page generation failed",
		"pageSlug", progress.PageSlug, "code", code, "cause", cause,
	)
}

func (s *Server) submitReindexJob(ctx context.Context, jobType, key, clientRequestID string) (domain.JobSnapshot, error) {
	inputSnapshot := domain.SnapshotID("")
	inputRevision := domain.Revision(0)
	if s.store != nil {
		metadata, err := s.store.SnapshotMetadataContext(ctx)
		if err != nil {
			return domain.JobSnapshot{}, err
		}
		inputSnapshot = metadata.ID
		inputRevision = metadata.Revision
	}
	return s.jobs.Submit(ctx, job.Spec{
		Type: jobType, Key: key, InputSnapshotID: inputSnapshot, ClientRequestID: clientRequestID, DedupPolicy: job.DedupCoalesce,
		Runner: func(jobCtx context.Context, reporter job.ProgressReporter) (job.Result, error) {
			currentMetadata := func() (domain.SnapshotMetadata, error) {
				if s.store == nil {
					return domain.SnapshotMetadata{}, repository.ErrStoreUnavailable
				}
				return s.store.SnapshotMetadataContext(jobCtx)
			}
			switch jobType {
			case "embeddings.rebuild":
				_ = reporter.Report("generate", "regenerando embeddings", domain.Progress{Indeterminate: true, Unit: "symbol"})
				if s.retriever == nil {
					return job.Result{}, errors.New("embedding retriever is unavailable")
				}
				if err := s.retriever.RebuildEmbeddings(jobCtx); err != nil {
					return job.Result{}, err
				}
				metadata, err := currentMetadata()
				if err != nil {
					return job.Result{}, err
				}
				count := s.store.EmbeddingCount()
				_ = reporter.Report("rebuilt", "embeddings index rebuilt", domain.Progress{Completed: int64(count), Total: int64(count), Unit: "vector"})
				return job.Result{SnapshotID: metadata.ID, Payload: map[string]any{
					"rebuilt": true, "index": "embeddings", "vectors": count, "snapshotId": metadata.ID,
				}}, nil
			case "fts.rebuild":
				_ = reporter.Report("rebuild", "rebuilding FTS index", domain.Progress{Indeterminate: true, Unit: "symbol"})
				rebuilder, ok := s.store.(repository.FTSRebuilder)
				if !ok {
					return job.Result{}, errors.New("repository backend does not support FTS rebuild")
				}
				if err := rebuilder.RebuildFTSContext(jobCtx); err != nil {
					return job.Result{}, err
				}
				metadata, err := currentMetadata()
				if err != nil {
					return job.Result{}, err
				}
				_, symbols, _, _, _ := s.store.Counts()
				_ = reporter.Report("rebuilt", "FTS index rebuilt", domain.Progress{Completed: int64(symbols), Total: int64(symbols), Unit: "symbol"})
				return job.Result{SnapshotID: metadata.ID, Payload: map[string]any{
					"rebuilt": true, "index": "fts", "symbols": symbols, "snapshotId": metadata.ID,
				}}, nil
			}

			_ = reporter.Report("scan", "reconciling workspace", domain.Progress{Indeterminate: true, Unit: "workspace"})
			var err error
			if s.scheduler != nil {
				err = s.scheduler.RequestFull(jobCtx, scheduler.ReasonManual)
			} else {
				metadata, metadataErr := currentMetadata()
				if metadataErr != nil {
					return job.Result{}, metadataErr
				}
				err = s.indexer.ReconcileFull(jobCtx, uint64(metadata.Revision))
			}
			if err != nil {
				return job.Result{}, err
			}
			resultSnapshot := domain.SnapshotID("")
			resultRevision := domain.Revision(0)
			metadata, metadataErr := currentMetadata()
			if metadataErr != nil {
				return job.Result{}, metadataErr
			}
			resultSnapshot = metadata.ID
			resultRevision = metadata.Revision
			if jobType == "repository.reindex" && inputSnapshot == resultSnapshot && inputRevision == resultRevision {
				_ = reporter.Report("no_changes", "index was already up to date", domain.Progress{Unit: "workspace"})
				return job.Result{SnapshotID: resultSnapshot, Payload: map[string]any{
					"committed": false, "filesChanged": 0, "filesRemoved": 0, "snapshotId": resultSnapshot,
				}}, nil
			}
			_ = reporter.Report("publish", "index updated", domain.Progress{Completed: 1, Total: 1, Unit: "commit"})
			return job.Result{SnapshotID: resultSnapshot, Payload: map[string]any{"committed": true, "snapshotId": resultSnapshot}}, nil
		},
	})
}

func decodeJobInput(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return apperror.InvalidArgumentMessage("input", "Invalid job input.", err)
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return apperror.InvalidArgumentMessage("input", "Expected a single JSON object as input.", nil)
	}
	return nil
}

func expectedJobRevision(request *http.Request) (*uint64, error) {
	raw := strings.TrimSpace(request.Header.Get("If-Match"))
	if raw == "" {
		return nil, nil
	}
	raw = strings.Trim(raw, `"`)
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return nil, apperror.InvalidArgumentMessage("If-Match", "If-Match must contain the numeric job revision.", err)
	}
	return &value, nil
}

func hashPublicKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func (s *Server) writeJobError(response http.ResponseWriter, request *http.Request, err error, id domain.JobID) {
	s.writeAppError(response, request, job.HTTPError(err, id))
}

func (s *Server) ShutdownJobs(ctx context.Context) error {
	var err error
	if s.jobs != nil {
		err = s.jobs.Shutdown(ctx)
	}
	if s.events != nil {
		s.events.Close()
	}
	return err
}

// decodeJSON reads a single JSON object into target, returning a typed apperror
// that differentiates an oversized body, invalid syntax, a wrong field type, an
// unknown field, an empty body and trailing data. It does not write a response;
// the caller passes the error to writeAppError.
func decodeJSON(response http.ResponseWriter, request *http.Request, target any, maxBytes int64) error {
	request.Body = http.MaxBytesReader(response, request.Body, maxBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesErr *http.MaxBytesError
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &maxBytesErr):
			return apperror.RequestTooLarge(maxBytes)
		case errors.As(err, &syntaxErr):
			return apperror.InvalidArgumentMessage("body", "Invalid JSON.", err)
		case errors.As(err, &typeErr):
			return apperror.InvalidArgumentMessage(typeErr.Field, "Invalid field type.", err)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), "\"")
			return apperror.InvalidArgumentMessage(field, "Unknown request field.", err)
		case errors.Is(err, io.EOF):
			return apperror.InvalidArgumentMessage("body", "Empty request body.", err)
		default:
			return apperror.InvalidArgument("body", err)
		}
	}
	if decoder.More() {
		return apperror.InvalidArgumentMessage("body", "Expected a single JSON object.", nil)
	}
	return nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

// writeAppError is the single error translator: it classifies any error into a
// typed *apperror.AppError (never by substring), logs the internal cause with the
// request id, and emits the sanitized public envelope. The cause chain is never
// serialized to the client.
func (s *Server) writeAppError(response http.ResponseWriter, request *http.Request, err error) {
	id := requestIDFrom(request.Context())

	// A pre-typed SaveError keeps its own stable code and status (#4/#5).
	var saveErr *service.SaveError
	if errors.As(err, &saveErr) {
		s.logError(request, saveErr.Status, saveErr.Code, err, id)
		writeErrorEnvelope(response, saveErr.Status, saveErr.Code, saveErr.Message, retryableForStatus(saveErr.Status), nil, id)
		return
	}

	appErr := toAppError(err)
	status := apperror.HTTPStatus(appErr)
	s.logError(request, status, string(appErr.Code), err, id)
	writeErrorEnvelope(response, status, string(appErr.Code), appErr.Message, appErr.Retryable, appErr.Details, id)
}

// toAppError classifies an error using errors.Is/errors.As over typed errors and
// stable sentinels — never message substrings. An unknown error becomes a generic
// INTERNAL_ERROR so no internal message leaks.
func toAppError(err error) *apperror.AppError {
	if appErr, ok := apperror.As(err); ok {
		return appErr
	}
	switch {
	case errors.Is(err, repository.ErrVersionConflict):
		return apperror.StoreVersionConflict()
	case errors.Is(err, indexer.ErrIndexingInProgress):
		return apperror.IndexingInProgress()
	case errors.Is(err, service.ErrGenerationInProgress):
		return apperror.DeepWikiGenerationInProgress()
	case errors.Is(err, ai.ErrUnavailable):
		return apperror.ProviderUnavailable(err)
	case errors.Is(err, context.Canceled):
		return apperror.RequestCanceled(err)
	case errors.Is(err, context.DeadlineExceeded):
		return apperror.ProviderTimeout(err)
	case errors.Is(err, os.ErrNotExist):
		return apperror.FileNotFound("", err)
	default:
		return apperror.InternalError(err)
	}
}

func writeErrorEnvelope(response http.ResponseWriter, status int, code, message string, retryable bool, details map[string]any, requestID string) {
	envelope := map[string]any{"code": code, "message": message, "retryable": retryable}
	if len(details) > 0 {
		envelope["details"] = details
	}
	if requestID != "" {
		envelope["requestId"] = requestID
	}
	writeJSON(response, status, map[string]any{"error": envelope})
}

// retryableForStatus marks transient infrastructure statuses retryable; client
// errors and state conflicts are not retried blindly.
func retryableForStatus(status int) bool {
	return status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// logError records the internal cause for diagnostics — code, status, request id,
// method and path — at Warn for 4xx and Error for 5xx. It never logs source
// content or credentials.
func (s *Server) logError(request *http.Request, status int, code string, cause error, requestID string) {
	level := slog.LevelWarn
	if status >= 500 {
		level = slog.LevelError
	}
	s.logger.Log(request.Context(), level, "request failed",
		"code", code, "status", status, "method", request.Method,
		"path", request.URL.Path, "requestId", requestID, "cause", cause)
}

func Shutdown(ctx context.Context, server *http.Server) error {
	return server.Shutdown(ctx)
}
