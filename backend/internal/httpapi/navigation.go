package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/overlay"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
)

func (s *Server) handleNavigationQuery(response http.ResponseWriter, request *http.Request) {
	var payload domain.NavigationRequest
	if err := decodeJSON(response, request, &payload, 64<<10); err != nil {
		s.writeAppError(response, request, err)
		return
	}

	var (
		result domain.NavigationResult
		err    error
	)
	if payload.DocumentID != "" {
		result, err = s.navigationOverlay(request.Context(), payload)
	} else {
		result, err = s.navigation.Query(request.Context(), payload)
	}
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	if result.DocumentID != "" {
		response.Header().Set("Cache-Control", "no-store")
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) navigationOverlay(ctx context.Context, payload domain.NavigationRequest) (domain.NavigationResult, error) {
	if payload.DocumentVersion <= 0 {
		return domain.NavigationResult{}, apperror.InvalidArgumentMessage("documentVersion", "documentVersion is required for an open document.", nil)
	}
	documentID := overlay.DocumentID(payload.DocumentID)
	version := overlay.DocumentVersion(payload.DocumentVersion)
	snapshot, err := s.overlays.Get(documentID, &version)
	if err != nil {
		return domain.NavigationResult{}, err
	}
	if payload.Path != "" && payload.Path != snapshot.Path {
		return domain.NavigationResult{}, apperror.InvalidArgumentMessage("path", "path does not match documentId.", nil)
	}
	payload.Path = snapshot.Path
	parsed, parseErr := s.parsedOverlayFile(snapshot)
	if parseErr != nil {
		return domain.NavigationResult{}, apperror.SourceParseFailed(snapshot.Path, parseErr)
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
			return domain.NavigationResult{}, apperror.FileChangedOnDisk(snapshot.Path, snapshot.BaseContentHash)
		}
		return domain.NavigationResult{}, apperror.InternalError(err)
	}
	defer composite.Close()

	result, err := s.navigation.QueryOnView(ctx, payload, composite, snapshot.Content, service.NavigationViewOptions{
		ViewHash:        composite.ViewHash(),
		DocumentID:      string(snapshot.DocumentID),
		DocumentVersion: int64(snapshot.Version),
	})
	if err != nil {
		return domain.NavigationResult{}, err
	}
	latest, err := s.overlays.Get(documentID, nil)
	if err != nil {
		return domain.NavigationResult{}, apperror.DocumentNotFound(string(documentID))
	}
	if latest.Version != snapshot.Version {
		return domain.NavigationResult{}, apperror.DocumentVersionConflict(snapshot.Path)
	}
	return result, nil
}
