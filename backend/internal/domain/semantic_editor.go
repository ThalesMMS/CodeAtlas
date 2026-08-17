package domain

type SemanticOmission struct {
	Reason string `json:"reason"`
	Ref    string `json:"ref,omitempty"`
	Detail string `json:"detail,omitempty"`
	Count  int    `json:"count,omitempty"`
}

type SemanticProvenance struct {
	ProviderID  string `json:"providerId"`
	ToolVersion string `json:"toolVersion,omitempty"`
	Method      string `json:"method,omitempty"`
}

type DiagnosticSeverity string

const (
	DiagnosticSeverityError   DiagnosticSeverity = "error"
	DiagnosticSeverityWarning DiagnosticSeverity = "warning"
	DiagnosticSeverityInfo    DiagnosticSeverity = "info"
	DiagnosticSeverityHint    DiagnosticSeverity = "hint"
)

type DiagnosticRelatedLocation struct {
	Path    string `json:"path"`
	Range   Range  `json:"range"`
	Message string `json:"message,omitempty"`
}

type Diagnostic struct {
	DiagnosticID string                      `json:"diagnosticId"`
	Path         string                      `json:"path"`
	Range        Range                       `json:"range"`
	Severity     DiagnosticSeverity          `json:"severity"`
	Code         string                      `json:"code,omitempty"`
	Source       string                      `json:"source"`
	Message      string                      `json:"message"`
	Tags         []string                    `json:"tags"`
	Related      []DiagnosticRelatedLocation `json:"related"`
	VersionKnown bool                        `json:"versionKnown"`
	Provenance   SemanticProvenance          `json:"provenance"`
}

type DiagnosticsResult struct {
	DocumentID       string             `json:"documentId"`
	DocumentVersion  int64              `json:"documentVersion"`
	ContentHash      string             `json:"contentHash"`
	SnapshotID       SnapshotID         `json:"snapshotId"`
	ViewHash         string             `json:"viewHash"`
	ProviderSession  string             `json:"providerSession,omitempty"`
	Diagnostics      []Diagnostic       `json:"diagnostics"`
	SemanticCoverage map[string]any     `json:"semanticCoverage"`
	Truncated        bool               `json:"truncated"`
	Omissions        []SemanticOmission `json:"omissions,omitempty"`
}

// SemanticToken is the provider-neutral editor token returned by CodeAtlas.
// Ranges are always 1-based UTF-16 so the DTO can be applied directly to the
// acknowledged Monaco model without leaking an LSP legend index.
type SemanticToken struct {
	Range     Range    `json:"range"`
	TokenType string   `json:"tokenType"`
	Modifiers []string `json:"modifiers"`
}

// SemanticTokensResult is bound to one exact overlay version, content hash and
// provider process session. A non-available provider state is a successful,
// explicit result with no tokens and an omission; it is never confused with a
// legitimate available zero-token response.
type SemanticTokensResult struct {
	LegendVersion    string             `json:"legendVersion"`
	DocumentID       string             `json:"documentId"`
	DocumentVersion  int64              `json:"documentVersion"`
	ContentHash      string             `json:"contentHash"`
	SnapshotID       SnapshotID         `json:"snapshotId"`
	ViewHash         string             `json:"viewHash"`
	ProviderSession  string             `json:"providerSession,omitempty"`
	Tokens           []SemanticToken    `json:"tokens"`
	SemanticCoverage map[string]any     `json:"semanticCoverage"`
	Truncated        bool               `json:"truncated"`
	Omissions        []SemanticOmission `json:"omissions,omitempty"`
}
