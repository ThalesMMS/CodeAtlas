package httpapi

import (
	"crypto/subtle"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/settings"
)

const (
	settingsTokenHeader = "X-CodeAtlas-Settings-Token"
	settingsBodyLimit   = int64(64 << 10)
)

type resetSettingsRequest struct {
	Revision uint64 `json:"revision"`
}

type restartSettingsRequest struct {
	Revision uint64 `json:"revision"`
}

// settingsSnapshotResponse adds process capabilities to the manager snapshot
// without touching the settings package: restartSupported tells the page
// whether the composition root can apply restart-only values in place.
type settingsSnapshotResponse struct {
	settings.SanitizedSnapshot
	RestartSupported bool `json:"restartSupported"`
}

type settingsUpdateResponse struct {
	settings.UpdateResult
	Snapshot settingsSnapshotResponse `json:"snapshot"`
}

func (s *Server) settingsSnapshotResponse(snapshot settings.SanitizedSnapshot) settingsSnapshotResponse {
	return settingsSnapshotResponse{SanitizedSnapshot: snapshot, RestartSupported: s.restartHandler != nil}
}

func (s *Server) settingsUpdateResponse(result settings.UpdateResult) settingsUpdateResponse {
	return settingsUpdateResponse{UpdateResult: result, Snapshot: s.settingsSnapshotResponse(result.Snapshot)}
}

func (s *Server) handleGetSettings(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !s.authorizeSettings(response, request, false) {
		return
	}
	writeJSON(response, http.StatusOK, s.settingsSnapshotResponse(s.settingsManager.Snapshot()))
}

func (s *Server) handlePutSettings(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !s.authorizeSettings(response, request, true) || !s.requireSettingsJSON(response, request) {
		return
	}
	var payload settings.UpdateRequest
	if err := decodeJSON(response, request, &payload, settingsBodyLimit); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	result, err := s.settingsManager.Update(request.Context(), payload)
	if err != nil {
		s.writeSettingsManagerError(response, request, err, result.Snapshot)
		return
	}
	writeJSON(response, http.StatusOK, s.settingsUpdateResponse(result))
}

func (s *Server) handleResetSettings(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !s.authorizeSettings(response, request, true) || !s.requireSettingsJSON(response, request) {
		return
	}
	var payload resetSettingsRequest
	if err := decodeJSON(response, request, &payload, settingsBodyLimit); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	result, err := s.settingsManager.Reset(request.Context(), payload.Revision)
	if err != nil {
		s.writeSettingsManagerError(response, request, err, result.Snapshot)
		return
	}
	writeJSON(response, http.StatusOK, s.settingsUpdateResponse(result))
}

// handleRestartSettings restarts the runtime in place so saved restart-only
// settings become the running values. The request carries the revision the
// page last saw so a stale window cannot restart on top of unseen changes.
func (s *Server) handleRestartSettings(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !s.authorizeSettings(response, request, true) || !s.requireSettingsJSON(response, request) {
		return
	}
	var payload restartSettingsRequest
	if err := decodeJSON(response, request, &payload, settingsBodyLimit); err != nil {
		s.writeAppError(response, request, err)
		return
	}
	if s.restartHandler == nil {
		writeErrorEnvelope(response, http.StatusServiceUnavailable, "SETTINGS_RESTART_UNAVAILABLE", "This CodeAtlas process cannot restart itself; restart it manually.", false, nil, requestIDFrom(request.Context()))
		return
	}
	snapshot := s.settingsManager.Snapshot()
	if payload.Revision != snapshot.Revision {
		details := map[string]any{"snapshot": s.settingsSnapshotResponse(snapshot)}
		writeErrorEnvelope(response, http.StatusConflict, settings.SettingsRevisionConflict, "Settings changed since they were loaded.", false, details, requestIDFrom(request.Context()))
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"restarting": true, "revision": snapshot.Revision})
	if flusher, ok := response.(http.Flusher); ok {
		flusher.Flush()
	}
	// The handler cancels the runtime context; run it off the request goroutine
	// so this response is fully written before the server begins shutting down.
	go s.restartHandler()
}

func (s *Server) authorizeSettings(response http.ResponseWriter, request *http.Request, mutation bool) bool {
	if s.settingsManager == nil || s.settingsToken == "" {
		writeErrorEnvelope(response, http.StatusServiceUnavailable, "SETTINGS_UNAVAILABLE", "Settings are unavailable.", true, nil, requestIDFrom(request.Context()))
		return false
	}
	provided := request.Header.Get(settingsTokenHeader)
	if subtle.ConstantTimeCompare([]byte(provided), []byte(s.settingsToken)) != 1 ||
		!isLoopbackPeer(request.RemoteAddr) || !isLoopbackHost(request.Host) ||
		!validSettingsOrigin(request, mutation) {
		writeErrorEnvelope(response, http.StatusForbidden, "SETTINGS_ACCESS_DENIED", "Settings access was denied.", false, nil, requestIDFrom(request.Context()))
		return false
	}
	return true
}

func isLoopbackPeer(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(remoteAddress), "[]")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func isLoopbackHost(rawHost string) bool {
	host := strings.TrimSpace(rawHost)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validSettingsOrigin(request *http.Request, required bool) bool {
	rawOrigin := strings.TrimSpace(request.Header.Get("Origin"))
	if rawOrigin == "" {
		return !required
	}
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return false
	}
	expectedScheme := "http"
	if request.TLS != nil {
		expectedScheme = "https"
	}
	return strings.EqualFold(origin.Scheme, expectedScheme) && strings.EqualFold(origin.Host, request.Host)
}

func (s *Server) requireSettingsJSON(response http.ResponseWriter, request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeErrorEnvelope(response, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type application/json is required.", false, nil, requestIDFrom(request.Context()))
		return false
	}
	return true
}

func (s *Server) writeSettingsManagerError(response http.ResponseWriter, request *http.Request, err error, fallback settings.SanitizedSnapshot) {
	var managerError *settings.ManagerError
	if !errors.As(err, &managerError) {
		s.writeAppError(response, request, err)
		return
	}
	snapshot := managerError.Snapshot
	if snapshot.Groups == nil {
		snapshot = fallback
	}
	details := map[string]any{"snapshot": snapshot}
	status := http.StatusServiceUnavailable
	message := "Settings could not be applied."
	switch managerError.Code {
	case settings.SettingsRevisionConflict:
		status = http.StatusConflict
		message = "Settings changed since they were loaded."
	case settings.SettingsValidationFailed:
		status = http.StatusBadRequest
		message = "Some settings are invalid."
		details["fields"] = managerError.Fields
	case settings.SettingsVaultFailed:
		message = "Credentials could not be stored."
	case settings.SettingsPrepareFailed:
		message = "The runtime rejected these settings."
		if len(managerError.Fields) > 0 {
			details["fields"] = managerError.Fields
		}
	case settings.SettingsSaveFailed:
		message = "Settings could not be saved."
	}
	s.logError(request, status, managerError.Code, managerError, requestIDFrom(request.Context()))
	writeErrorEnvelope(response, status, managerError.Code, message, status >= 500, details, requestIDFrom(request.Context()))
}
