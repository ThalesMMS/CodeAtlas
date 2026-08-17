package httpapi

import (
	"net/http"
	"strconv"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
)

func (s *Server) handleDocumentDiagnostics(response http.ResponseWriter, request *http.Request) {
	documentID := request.PathValue("documentId")
	rawVersion := request.URL.Query().Get("version")
	if rawVersion == "" {
		s.writeAppError(response, request, apperror.InvalidArgumentMessage("version", "version is required.", nil))
		return
	}
	version, err := strconv.ParseInt(rawVersion, 10, 64)
	if err != nil {
		s.writeAppError(response, request, apperror.InvalidArgumentMessage("version", "invalid version.", err))
		return
	}
	result, err := s.semanticEditor.Diagnostics(request.Context(), documentID, version)
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handleDocumentSemanticTokens(response http.ResponseWriter, request *http.Request) {
	documentID := request.PathValue("documentId")
	rawVersion := request.URL.Query().Get("version")
	if rawVersion == "" {
		s.writeAppError(response, request, apperror.InvalidArgumentMessage("version", "version is required.", nil))
		return
	}
	version, err := strconv.ParseInt(rawVersion, 10, 64)
	if err != nil {
		s.writeAppError(response, request, apperror.InvalidArgumentMessage("version", "invalid version.", err))
		return
	}
	result, err := s.semanticEditor.SemanticTokens(request.Context(), documentID, version)
	if err != nil {
		s.writeAppError(response, request, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, result)
}
