package httpapi

import (
	"errors"
	"net/http"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
)

func (s *Server) handleGetFile(response http.ResponseWriter, request *http.Request) {
	path := request.URL.Query().Get("path")
	if path == "" {
		s.writeAppError(response, request, apperror.InvalidArgumentMessage("path", "path is required", nil))
		return
	}
	content, contentHash, storeVersion, err := s.saver.ReadView(path)
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"path":         path,
		"content":      string(content),
		"contentHash":  contentHash,
		"storeVersion": storeVersion,
	})
}

func (s *Server) handlePutFile(response http.ResponseWriter, request *http.Request) {
	var payload struct {
		Path                string `json:"path"`
		Content             string `json:"content"`
		ExpectedContentHash string `json:"expectedContentHash"`
	}
	if err := decodeJSON(response, request, &payload, 3<<20); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	if payload.Path == "" {
		s.writeAppError(response, request, apperror.InvalidArgumentMessage("path", "path is required", nil))
		return
	}

	prepared, err := s.saver.Prepare(request.Context(), service.SaveRequest{
		Path:                payload.Path,
		Content:             []byte(payload.Content),
		ExpectedContentHash: payload.ExpectedContentHash,
	})
	if err != nil {
		code := "SAVE_PREPARE_FAILED"
		var saveErr *service.SaveError
		if errors.As(err, &saveErr) {
			code = saveErr.Code
		}
		s.publishIndexEvent(domain.IndexEvent{
			Type: "save.prepare.failed", Path: payload.Path,
			Error: &domain.PublicError{Code: code, Message: "save preparation failed"},
		})
		s.writeAppError(response, request, err)
		return
	}
	if prepared.IsNoop() {
		writeJSON(response, http.StatusOK, map[string]any{
			"saved": true, "noop": true, "path": payload.Path,
			"contentHash": prepared.NewContentHash(), "storeVersion": s.store.Version(),
		})
		return
	}
	storeVersion, err := s.saver.CommitContext(request.Context(), prepared, nil)
	if err != nil {
		var rollbackErr *service.RollbackFailedError
		if errors.As(err, &rollbackErr) {
			// An irreversible mid-commit failure drives the app to FAILED so
			// functional routes are blocked until an explicit recovery.
			if s.readiness != nil {
				_ = s.readiness.Fail(service.CodeRecoveryFailed, "save rollback failed")
			}
			s.writeAppError(response, request, apperror.PersistenceFailed(err))
			return
		}
		s.writeAppError(response, request, err)
		return
	}
	// The save success event is emitted only after the journaled commit completes.
	s.publishIndexEvent(domain.IndexEvent{
		Type: "save.commit.succeeded", Path: payload.Path, StoreVersion: storeVersion,
		Message: "File saved and indexed",
	})
	writeJSON(response, http.StatusOK, map[string]any{
		"saved": true, "path": payload.Path,
		"contentHash": prepared.NewContentHash(), "storeVersion": storeVersion,
	})
}
