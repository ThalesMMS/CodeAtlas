package httpapi

import (
	"net/http"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
)

// ReadinessReader is the minimal read-only view of the readiness coordinator the
// health endpoints and the readiness gate depend on. Handlers use this interface
// rather than the concrete coordinator type.
type ReadinessReader interface {
	Snapshot() readiness.Snapshot
	CurrentState() readiness.State
	Fail(code, message string) error
}

// CapabilityReader is the minimal read-only view of the capability registry used
// by the diagnostics endpoint.
type CapabilityReader interface {
	Results() []capabilities.Result
}

type liveResponse struct {
	Status string    `json:"status"`
	Time   time.Time `json:"time"`
}

type readyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type readyResponse struct {
	Status         string      `json:"status"`
	State          string      `json:"state"`
	Stage          string      `json:"stage,omitempty"`
	LastTransition time.Time   `json:"lastTransition"`
	SnapshotID     string      `json:"snapshotId,omitempty"`
	Revision       uint64      `json:"revision,omitempty"`
	Error          *readyError `json:"error,omitempty"`
}

type capabilityResponse struct {
	ID          string            `json:"id"`
	Requirement string            `json:"requirement"`
	State       string            `json:"state"`
	DurationMs  int64             `json:"durationMs"`
	CheckedAt   time.Time         `json:"checkedAt"`
	ErrorCode   string            `json:"errorCode,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// handleLive reports process liveness. It performs no remote I/O, parsing,
// workspace access or store writes, so it stays cheap and always answers while
// the HTTP server accepts connections.
func (s *Server) handleLive(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, liveResponse{Status: "alive", Time: time.Now().UTC()})
}

// handleReady reports operational readiness. It returns 200 only in the READY
// state and 503 otherwise, with the current state, stage, last transition and a
// sanitized fatal cause when present.
func (s *Server) handleReady(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if s.readiness == nil {
		writeJSON(response, http.StatusServiceUnavailable, readyResponse{Status: "not_ready", State: "UNKNOWN"})
		return
	}
	snapshot := s.readiness.Snapshot()
	body := readyResponse{
		State:          string(snapshot.State),
		Stage:          snapshot.CurrentStep,
		LastTransition: snapshot.LastTransition.UTC(),
		SnapshotID:     snapshot.IndexRevision,
	}
	if snapshot.FatalError != nil {
		body.Error = &readyError{Code: snapshot.FatalError.Code, Message: snapshot.FatalError.Message}
	}
	if snapshot.State == readiness.StateReady {
		body.Status = "ready"
		// Once READY, surface the published content-addressed snapshot provenance.
		if s.store != nil {
			metadata, err := s.store.SnapshotMetadataContext(request.Context())
			if err != nil {
				body.Status = "not_ready"
				body.Error = &readyError{Code: "STORE_UNAVAILABLE", Message: "store metadata unavailable"}
				writeJSON(response, http.StatusServiceUnavailable, body)
				return
			}
			body.SnapshotID = string(metadata.ID)
			body.Revision = uint64(metadata.Revision)
		}
		writeJSON(response, http.StatusOK, body)
		return
	}
	body.Status = "not_ready"
	writeJSON(response, http.StatusServiceUnavailable, body)
}

// handleCapabilities returns the capability registry as a diagnostics payload. It
// answers 200 even when the app is not ready and never includes secrets: the
// results carry only sanitized, public fields.
func (s *Server) handleCapabilities(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	capabilitiesList := []capabilityResponse{}
	if s.capabilities != nil {
		for _, result := range s.capabilities.Results() {
			capabilitiesList = append(capabilitiesList, capabilityResponse{
				ID:          string(result.ID),
				Requirement: string(result.Requirement),
				State:       string(result.State),
				DurationMs:  result.Duration.Milliseconds(),
				CheckedAt:   result.CheckedAt.UTC(),
				ErrorCode:   result.ErrorCode,
				Metadata:    result.Metadata,
			})
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"capabilities": capabilitiesList})
}
