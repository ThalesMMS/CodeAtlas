package rustlsp

import (
	"encoding/json"
)

// handlePublishDiagnostics ingests a publishDiagnostics notification, replacing the
// prior set for that URI (bounded; untrusted message; empty clears).
func (m *Manager) handlePublishDiagnostics(params json.RawMessage) {
	m.diagnostics.Ingest(params)
}
