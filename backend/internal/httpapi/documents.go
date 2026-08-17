package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/overlay"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
)

// explainOverlay explains an open document against its unsaved buffer: it parses
// the overlay content, composites it over the persisted snapshot, and re-checks
// the document version after the model call so a stale answer is never delivered.
func (s *Server) explainOverlay(ctx context.Context, payload domain.ExplainRequest) (domain.Explanation, error) {
	documentID := overlay.DocumentID(payload.DocumentID)
	var version *overlay.DocumentVersion
	if payload.DocumentVersion > 0 {
		value := overlay.DocumentVersion(payload.DocumentVersion)
		version = &value
	}
	snapshot, err := s.overlays.Get(documentID, version)
	if err != nil {
		return domain.Explanation{}, err
	}
	if payload.Path != "" && payload.Path != snapshot.Path {
		return domain.Explanation{}, apperror.InvalidArgumentMessage("path", "path does not match documentId.", nil)
	}
	payload.Path = snapshot.Path
	if _, _, supported := codeparser.DetectLanguage(snapshot.Path); !supported {
		return domain.Explanation{}, apperror.UnsupportedLanguage(snapshot.Path)
	}
	parsed, parseErr := s.parsedOverlayFile(snapshot)
	if parseErr != nil {
		return domain.Explanation{}, apperror.SourceParseFailed(snapshot.Path, parseErr)
	}
	composite, err := s.store.CompositeViewContext(ctx, parsed, repository.OverlayContext{
		DocumentID:      string(snapshot.DocumentID),
		Path:            snapshot.Path,
		Version:         int64(snapshot.Version),
		ContentHash:     snapshot.ContentHash,
		BaseContentHash: snapshot.BaseContentHash,
		BaseSnapshotID:  snapshot.BaseSnapshotID,
	})
	if err != nil {
		if errors.Is(err, repository.ErrDocumentBaseStale) {
			return domain.Explanation{}, apperror.FileChangedOnDisk(snapshot.Path, snapshot.BaseContentHash)
		}
		return domain.Explanation{}, apperror.InternalError(err)
	}

	capturedVersion := snapshot.Version
	recheck := func() error {
		latest, err := s.overlays.Get(documentID, nil)
		if err != nil {
			return apperror.DocumentNotFound(string(documentID))
		}
		if latest.Version != capturedVersion {
			return apperror.DocumentVersionConflict(snapshot.Path)
		}
		return nil
	}
	return s.explainer.ExplainOverlay(ctx, payload, composite, snapshot.Content, recheck)
}

// leaseHeader carries the document lease for GET/DELETE so it never lands in an
// access log or query string.
const leaseHeader = "X-Document-Lease"

// documentBody renders an overlay snapshot for the API. The lease is included
// only on Open (the client needs it once); it is never logged.
func documentBody(snapshot overlay.OverlaySnapshot, includeLease bool) map[string]any {
	body := map[string]any{
		"documentId":      string(snapshot.DocumentID),
		"path":            snapshot.Path,
		"language":        snapshot.Language,
		"version":         int64(snapshot.Version),
		"content":         string(snapshot.Content),
		"contentHash":     snapshot.ContentHash,
		"baseContentHash": snapshot.BaseContentHash,
		"baseSnapshotId":  string(snapshot.BaseSnapshotID),
		"dirty":           snapshot.Dirty,
		"state":           string(snapshot.State),
	}
	if includeLease {
		body["leaseId"] = string(snapshot.LeaseID)
	}
	if snapshot.Conflict != nil {
		body["conflict"] = documentConflictBody(snapshot.Conflict)
	}
	return body
}

func documentConflictBody(conflict *overlay.DocumentConflict) map[string]any {
	if conflict == nil {
		return nil
	}
	return map[string]any{
		"documentId":           string(conflict.DocumentID),
		"path":                 conflict.Path,
		"state":                string(conflict.State),
		"localVersion":         int64(conflict.LocalVersion),
		"localContentHash":     conflict.LocalContentHash,
		"baseContentHash":      conflict.BaseContentHash,
		"baseSnapshotId":       string(conflict.BaseSnapshotID),
		"externalContentHash":  conflict.ExternalContentHash,
		"externalSnapshotId":   string(conflict.ExternalSnapshotID),
		"externalRevision":     int64(conflict.ExternalRevision),
		"detectedAt":           conflict.DetectedAt,
		"reasonCode":           conflict.ReasonCode,
		"reconcileOperationId": conflict.ReconcileOperationID,
	}
}

func (s *Server) handlePublishedWorkspaceChange(ctx context.Context, change domain.PublishedWorkspaceChange) {
	transitions, err := s.overlays.ApplyWorkspaceChange(ctx, change)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("overlay workspace change failed", "operationId", change.OperationID, "error", err)
		}
		return
	}
	for _, transition := range transitions {
		if transition.To != overlay.StateExternalChangedClean {
			continue
		}
		snapshot, err := s.overlays.Get(transition.DocumentID, nil)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("overlay external reload vanished before parse sync", "documentId", transition.DocumentID, "error", err)
			}
			continue
		}
		if _, err := s.parseSessions.Update(string(snapshot.DocumentID), int64(snapshot.Version), snapshot.Content); err != nil && s.logger != nil {
			s.logger.Warn("overlay external reload parse sync failed", "documentId", snapshot.DocumentID, "path", snapshot.Path, "error", err)
		}
		s.overlayParses.invalidate(string(snapshot.DocumentID))
	}
}

// writeDocumentJSON writes a content-bearing response with no-store caching.
func writeDocumentJSON(response http.ResponseWriter, status int, body map[string]any) {
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, status, body)
}

// requireJSONContentType enforces application/json on write endpoints.
func requireJSONContentType(request *http.Request) error {
	contentType := request.Header.Get("Content-Type")
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	if strings.TrimSpace(strings.ToLower(contentType)) != "application/json" {
		return apperror.InvalidArgumentMessage("Content-Type", "Content-Type application/json is required.", nil)
	}
	return nil
}

func (s *Server) handleOpenDocument(response http.ResponseWriter, request *http.Request) {
	if err := requireJSONContentType(request); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	var payload struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(response, request, &payload, 1<<20); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	snapshot, err := s.overlays.Open(request.Context(), overlay.OpenRequest{Path: payload.Path})
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	// A document must be analyzable before it is confirmed open: create the parse
	// session now and roll the overlay back if the parser cannot be created, so no
	// editable-but-unparseable buffer is left behind.
	language, _, _ := codeparser.DetectLanguage(snapshot.Path)
	if _, perr := s.parseSessions.Open(string(snapshot.DocumentID), int64(snapshot.Version), language, snapshot.Content); perr != nil {
		_ = s.overlays.Close(snapshot.DocumentID, snapshot.LeaseID, true)
		s.writeAppError(response, request, apperror.UnsupportedLanguage(snapshot.Path))
		return
	}
	s.syncSemanticOpen(request.Context(), snapshot, string(language))
	s.overlayParses.invalidate(string(snapshot.DocumentID))
	writeDocumentJSON(response, http.StatusCreated, documentBody(snapshot, true))
}

func (s *Server) handleReplaceDocument(response http.ResponseWriter, request *http.Request) {
	if err := requireJSONContentType(request); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	documentID := overlay.DocumentID(request.PathValue("documentId"))
	var payload struct {
		LeaseID         string `json:"leaseId"`
		ExpectedVersion int64  `json:"expectedVersion"`
		NewVersion      int64  `json:"newVersion"`
		Content         string `json:"content"`
	}
	if err := decodeJSON(response, request, &payload, 8<<20); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	snapshot, err := s.overlays.Replace(request.Context(), overlay.ReplaceRequest{
		DocumentID:      documentID,
		LeaseID:         overlay.LeaseID(payload.LeaseID),
		ExpectedVersion: overlay.DocumentVersion(payload.ExpectedVersion),
		NewVersion:      overlay.DocumentVersion(payload.NewVersion),
		Content:         []byte(payload.Content),
	})
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	// The replace is only a confirmed, analyzable version once the parse session
	// publishes it; otherwise the backend does not declare sync complete.
	if _, perr := s.parseSessions.Update(string(documentID), int64(snapshot.Version), snapshot.Content); perr != nil {
		s.writeAppError(response, request, apperror.InternalError(perr))
		return
	}
	s.syncSemanticChange(request.Context(), snapshot)
	s.overlayParses.invalidate(string(documentID))
	writeDocumentJSON(response, http.StatusOK, documentBody(snapshot, false))
}

func (s *Server) handleGetDocument(response http.ResponseWriter, request *http.Request) {
	snapshot, ok := s.renewDocument(response, request)
	if !ok {
		return
	}
	body := documentBody(snapshot, false)
	body["baseContent"] = string(snapshot.BaseContent)
	writeDocumentJSON(response, http.StatusOK, body)
}

// handleRenewDocument extends the writable lease without returning either
// potentially large source buffer. It is the bounded heartbeat counterpart to
// handleGetDocument, which remains the explicit full snapshot read.
func (s *Server) handleRenewDocument(response http.ResponseWriter, request *http.Request) {
	snapshot, ok := s.renewDocument(response, request)
	if !ok {
		return
	}
	writeDocumentJSON(response, http.StatusOK, map[string]any{
		"documentId": string(snapshot.DocumentID),
		"path":       snapshot.Path,
		"version":    int64(snapshot.Version),
		"state":      string(snapshot.State),
	})
}

func (s *Server) renewDocument(response http.ResponseWriter, request *http.Request) (overlay.OverlaySnapshot, bool) {
	documentID := overlay.DocumentID(request.PathValue("documentId"))
	lease := overlay.LeaseID(request.Header.Get(leaseHeader))
	if lease == "" {
		s.writeAppError(response, request, apperror.InvalidArgumentMessage("lease", "Lease is required (header "+leaseHeader+").", nil))
		return overlay.OverlaySnapshot{}, false
	}
	var version *overlay.DocumentVersion
	if raw := request.URL.Query().Get("version"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			s.writeAppError(response, request, apperror.InvalidArgumentMessage("version", "invalid version.", err))
			return overlay.OverlaySnapshot{}, false
		}
		value := overlay.DocumentVersion(parsed)
		version = &value
	}
	snapshot, err := s.overlays.Renew(documentID, lease, version)
	if err != nil {
		s.writeAppError(response, request, err)
		return overlay.OverlaySnapshot{}, false
	}
	return snapshot, true
}

func (s *Server) handleReclaimDocument(response http.ResponseWriter, request *http.Request) {
	documentID := overlay.DocumentID(request.PathValue("documentId"))
	lease := overlay.LeaseID(request.Header.Get(leaseHeader))
	if lease == "" {
		s.writeAppError(response, request, apperror.InvalidArgumentMessage("lease", "Lease is required (header "+leaseHeader+").", nil))
		return
	}
	snapshot, err := s.overlays.Reclaim(documentID, lease)
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	body := documentBody(snapshot, true)
	body["baseContent"] = string(snapshot.BaseContent)
	writeDocumentJSON(response, http.StatusOK, body)
}

func (s *Server) handleSaveDocument(response http.ResponseWriter, request *http.Request) {
	if err := requireJSONContentType(request); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	documentID := overlay.DocumentID(request.PathValue("documentId"))
	var payload struct {
		LeaseID string `json:"leaseId"`
		Version int64  `json:"version"`
	}
	if err := decodeJSON(response, request, &payload, 1<<20); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	version := overlay.DocumentVersion(payload.Version)
	snapshot, err := s.overlays.Get(documentID, &version)
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	if snapshot.LeaseID != overlay.LeaseID(payload.LeaseID) {
		s.writeAppError(response, request, apperror.DocumentNotFound(string(documentID)))
		return
	}
	if err := saveConflictError(snapshot); err != nil {
		s.writeAppError(response, request, err)
		return
	}

	// Save through the existing journaled commit coordinator, using the overlay's
	// base content hash as the precondition: if the file changed on disk since the
	// overlay was opened, the prepare fails with FILE_CHANGED_ON_DISK and the
	// overlay base is left untouched.
	prepared, err := s.saver.Prepare(request.Context(), service.SaveRequest{
		Path:                snapshot.Path,
		Content:             snapshot.Content,
		ExpectedContentHash: snapshot.BaseContentHash,
	})
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	if !prepared.IsNoop() {
		if _, err := s.saver.CommitContext(request.Context(), prepared, nil); err != nil {
			s.writeAppError(response, request, err)
			return
		}
		s.publishIndexEvent(domain.IndexEvent{
			Type: "save.commit.succeeded", Path: snapshot.Path, StoreVersion: s.store.Version(),
			Message: "Document saved and indexed",
		})
	}
	// Only after a confirmed commit do we advance the overlay base to the published
	// hash/snapshot, clearing dirty.
	saved, err := s.overlays.MarkSaved(documentID, snapshot.LeaseID, snapshot.Version, prepared.NewContentHash(), s.store.SnapshotID())
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	body := documentBody(saved, false)
	body["storeVersion"] = s.store.Version()
	body["revision"] = int64(s.store.SnapshotMetadata().Revision)
	writeDocumentJSON(response, http.StatusOK, body)
}

func saveConflictError(snapshot overlay.OverlaySnapshot) error {
	switch snapshot.State {
	case overlay.StateConflictedDirty, overlay.StateExternalDeleted, overlay.StateExternalRenamed, overlay.StateResolving:
		return apperror.DocumentConflictChanged(string(snapshot.DocumentID))
	default:
		return nil
	}
}

func (s *Server) handleResolveDocumentConflict(response http.ResponseWriter, request *http.Request) {
	if err := requireJSONContentType(request); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	documentID := overlay.DocumentID(request.PathValue("documentId"))
	var payload struct {
		LeaseID             string `json:"leaseId"`
		DocumentVersion     int64  `json:"documentVersion"`
		ExternalContentHash string `json:"externalContentHash"`
		Strategy            string `json:"strategy"`
	}
	if err := decodeJSON(response, request, &payload, 1<<20); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	result, err := s.overlays.ResolveConflict(request.Context(), overlay.ResolveConflictRequest{
		DocumentID:          documentID,
		LeaseID:             overlay.LeaseID(payload.LeaseID),
		DocumentVersion:     overlay.DocumentVersion(payload.DocumentVersion),
		ExternalContentHash: payload.ExternalContentHash,
		Strategy:            overlay.ResolveStrategy(payload.Strategy),
	})
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	if _, err := s.parseSessions.Update(string(documentID), int64(result.Snapshot.Version), result.Snapshot.Content); err != nil && s.logger != nil {
		s.logger.Warn("overlay conflict resolution parse sync failed", "documentId", documentID, "path", result.Snapshot.Path, "error", err)
	}
	s.syncSemanticChange(request.Context(), result.Snapshot)
	s.overlayParses.invalidate(string(documentID))
	body := documentBody(result.Snapshot, false)
	body["willOverwriteExternalOnSave"] = result.WillOverwriteExternalOnSave
	writeDocumentJSON(response, http.StatusOK, body)
}

func (s *Server) handleDeleteDocument(response http.ResponseWriter, request *http.Request) {
	documentID := overlay.DocumentID(request.PathValue("documentId"))
	lease := overlay.LeaseID(request.Header.Get(leaseHeader))
	if lease == "" {
		s.writeAppError(response, request, apperror.InvalidArgumentMessage("lease", "Lease is required (header "+leaseHeader+").", nil))
		return
	}
	discard := request.URL.Query().Get("discard") == "true"
	snapshot, _ := s.overlays.Get(documentID, nil)
	if err := s.overlays.Close(documentID, lease, discard); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	s.syncSemanticClose(request.Context(), snapshot)
	s.parseSessions.Close(string(documentID))
	s.overlayParses.invalidate(string(documentID))
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleExpiredOverlay(snapshot overlay.OverlaySnapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.syncSemanticClose(ctx, snapshot)
	s.parseSessions.Close(string(snapshot.DocumentID))
	s.overlayParses.invalidate(string(snapshot.DocumentID))
	if s.logger != nil {
		s.logger.Info("expired abandoned document lease",
			"documentId", snapshot.DocumentID,
			"path", snapshot.Path,
			"dirty", snapshot.Dirty,
			"state", snapshot.State,
		)
	}
}

func (s *Server) syncSemanticOpen(ctx context.Context, snapshot overlay.OverlaySnapshot, language string) {
	if s.semanticSync == nil {
		return
	}
	err := s.semanticSync.OpenDocument(ctx, semantic.DocumentID(snapshot.DocumentID), semantic.DocumentVersion(snapshot.Version), snapshot.Path, language, snapshot.Content)
	s.logSemanticSyncError("open", snapshot, err)
}

func (s *Server) syncSemanticChange(ctx context.Context, snapshot overlay.OverlaySnapshot) {
	if s.semanticSync == nil {
		return
	}
	err := s.semanticSync.ChangeDocument(ctx, semantic.DocumentID(snapshot.DocumentID), semantic.DocumentVersion(snapshot.Version), snapshot.Path, snapshot.Content)
	s.logSemanticSyncError("change", snapshot, err)
}

func (s *Server) syncSemanticClose(ctx context.Context, snapshot overlay.OverlaySnapshot) {
	if s.semanticSync == nil || snapshot.DocumentID == "" {
		return
	}
	err := s.semanticSync.CloseDocument(ctx, semantic.DocumentID(snapshot.DocumentID), snapshot.Path)
	s.logSemanticSyncError("close", snapshot, err)
}

func (s *Server) logSemanticSyncError(operation string, snapshot overlay.OverlaySnapshot, err error) {
	if err == nil || errors.Is(err, semantic.ErrCapabilityUnsupported) || s.logger == nil {
		return
	}
	s.logger.Debug("overlay semantic sync unavailable", "operation", operation, "documentId", snapshot.DocumentID, "path", snapshot.Path, "version", snapshot.Version, "error", err)
}
