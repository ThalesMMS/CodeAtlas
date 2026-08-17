// Package semantic defines domain contracts for semantic queries, capabilities
// and facts, independent of JSON-RPC/LSP or any specific tool (gopls, the
// TypeScript language server, future runtime traces). Services depend on these
// contracts, not on tool structs, so AST, LSP and other sources coexist with
// explicit provenance/confidence and no source silently erases another.
package semantic

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

// Identifier types. DocumentID/DocumentVersion mirror the overlay store's
// identifiers; the boundary converts between them (both are plain values).
type (
	SemanticProviderID string
	DocumentID         string
	DocumentVersion    int64
)

// PositionEncoding is the explicit column encoding of a SourceLocation. LSP
// positions are UTF-16 by default; the AST/Tree-sitter model is UTF-8 bytes. A
// location must declare its encoding so it can be converted before reaching the
// internal model.
type PositionEncoding string

const (
	EncodingUTF8  PositionEncoding = "utf-8"
	EncodingUTF16 PositionEncoding = "utf-16"
)

// SourceLocation is a path+range with an explicit encoding.
type SourceLocation struct {
	Path     string           `json:"path"`
	Range    domain.Range     `json:"range"`
	Encoding PositionEncoding `json:"encoding"`
}

// SymbolRef references a symbol. When a provider returns only a location, the ref
// is partial (Resolved=false, empty SymbolID) and a later step resolves it; a
// SymbolID is never minted from a uri:range without the v1 resolver.
type SymbolRef struct {
	SymbolID      domain.SymbolID `json:"symbolId,omitempty"`
	Name          string          `json:"name,omitempty"`
	QualifiedName string          `json:"qualifiedName,omitempty"`
	Resolved      bool            `json:"resolved"`
}

// SemanticQuery is a semantic lookup. It must declare whether it targets the
// persisted snapshot or an open document: a non-empty DocumentID means the open
// overlay at DocumentVersion, otherwise the persisted SnapshotID is used.
type SemanticQuery struct {
	SnapshotID      domain.SnapshotID `json:"snapshotId"`
	DocumentID      DocumentID        `json:"documentId,omitempty"`
	DocumentVersion DocumentVersion   `json:"documentVersion,omitempty"`
	Path            string            `json:"path"`
	Position        domain.Position   `json:"position"`
	SymbolID        domain.SymbolID   `json:"symbolId,omitempty"`
	// Content is the exact open-buffer source used for position/range conversion.
	// It is process-local and must never be serialized as part of a fact/query DTO.
	Content []byte `json:"-"`
}

// UsesOpenDocument reports whether the query targets an open overlay buffer rather
// than the persisted snapshot.
func (q SemanticQuery) UsesOpenDocument() bool { return q.DocumentID != "" }

// SemanticCapabilities declares which queries a provider supports plus its sync
// and position-encoding contract.
type SemanticCapabilities struct {
	Hover                  bool     `json:"hover"`
	Definition             bool     `json:"definition"`
	References             bool     `json:"references"`
	Implementation         bool     `json:"implementation"`
	CallHierarchy          bool     `json:"callHierarchy"`
	Diagnostics            bool     `json:"diagnostics"`
	SemanticTokensFull     bool     `json:"semanticTokensFull"`
	SemanticTokensDelta    bool     `json:"semanticTokensDelta"`
	SemanticTokenTypes     []string `json:"semanticTokenTypes,omitempty"`
	SemanticTokenModifiers []string `json:"semanticTokenModifiers,omitempty"`
	DocumentSyncKind       string   `json:"documentSyncKind"`
	PositionEncoding       string   `json:"positionEncoding"`
}

// Fact kinds. Direction is documented per kind.
const (
	KindHoverType      = "hover_type"     // type/signature info at a position
	KindDefinition     = "definition"     // subject is defined at Object/Location
	KindReference      = "reference"      // subject is referenced at Location
	KindImplementation = "implementation" // subject (interface) is implemented by Object
	KindCallIncoming   = "call_incoming"  // Object calls Subject (caller -> subject)
	KindCallOutgoing   = "call_outgoing"  // Subject calls Object (subject -> callee)
	KindDiagnostic     = "diagnostic"     // a tool reported a diagnostic at Location
	KindTypeHierarchy  = "type_hierarchy" // reserved
	KindSyntacticCall  = "syntactic_call" // AST-only call fact (name-based)
)

// Provenance records where a fact came from. It is set by the producing layer and
// is never inferred from the confidence score.
type Provenance struct {
	Source      string             `json:"source"` // tree-sitter, gopls, typescript-lsp, runtime
	ProviderID  SemanticProviderID `json:"providerId"`
	ToolVersion string             `json:"toolVersion,omitempty"`
	Method      string             `json:"method,omitempty"` // e.g. textDocument/references
	ObservedAt  time.Time          `json:"observedAt"`
}

// maxDetail bounds the sanitized Detail; raw LSP payloads are never stored whole.
const maxDetail = 2000

// SemanticFact is one provenance-tagged semantic observation. It is only valid for
// the snapshot/document version it names.
type SemanticFact struct {
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	Subject         SymbolRef         `json:"subject"`
	Object          *SymbolRef        `json:"object,omitempty"`
	Location        SourceLocation    `json:"location"`
	Related         []SourceLocation  `json:"related,omitempty"`
	Detail          string            `json:"detail,omitempty"`
	Provenance      Provenance        `json:"provenance"`
	Confidence      float64           `json:"confidence"`
	SnapshotID      domain.SnapshotID `json:"snapshotId,omitempty"`
	DocumentID      DocumentID        `json:"documentId,omitempty"`
	DocumentVersion DocumentVersion   `json:"documentVersion,omitempty"`
	ContentHash     string            `json:"contentHash,omitempty"`
	ProviderSession string            `json:"providerSession,omitempty"`
	Diagnostic      *DiagnosticFact   `json:"diagnostic,omitempty"`
	// VersionKnown is false when the producing protocol could not prove the exact
	// document version (e.g. a diagnostic notification without a version); such a
	// fact is valid only while the document has not changed since it was observed.
	VersionKnown bool `json:"versionKnown,omitempty"`
}

// DiagnosticFact is the normalized, bounded subset of a protocol diagnostic.
// Raw protocol DTOs, markup, commands and workspace edits never cross this
// boundary.
type DiagnosticFact struct {
	Severity string `json:"severity"`
	Code     string `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// CanonicalSemanticTokenTypes is the CodeAtlas legend. Providers map their
// negotiated legend by name into these values; provider legend indexes are
// never exposed or assumed to match each other.
var CanonicalSemanticTokenTypes = []string{
	"namespace", "type", "class", "enum", "interface", "struct",
	"typeParameter", "parameter", "variable", "property", "enumMember",
	"event", "function", "method", "macro", "label", "comment", "string",
	"number", "regexp", "operator", "decorator", "keyword", "modifier",
}

var CanonicalSemanticTokenModifiers = []string{
	"declaration", "definition", "readonly", "static", "deprecated",
	"abstract", "async", "modification", "documentation", "defaultLibrary", "local",
}

// SemanticTokenLegendVersion identifies the public CodeAtlas-owned token
// vocabulary. Clients must reject unknown versions instead of guessing how a
// provider-specific legend maps to editor decorations.
const SemanticTokenLegendVersion = "codeatlas-semantic-tokens/v1"

// SemanticToken is one normalized token in CodeAtlas' internal 1-based byte
// coordinate system. The HTTP service converts it to UTF-16 for the editor.
type SemanticToken struct {
	Range     domain.Range `json:"range"`
	TokenType string       `json:"tokenType"`
	Modifiers []string     `json:"modifiers"`
}

// SemanticTokenSet is the normalized response from one provider process.
type SemanticTokenSet struct {
	DocumentID      DocumentID      `json:"documentId"`
	DocumentVersion DocumentVersion `json:"documentVersion"`
	ContentHash     string          `json:"contentHash"`
	ProviderSession string          `json:"providerSession"`
	ResultID        string          `json:"resultId,omitempty"`
	Tokens          []SemanticToken `json:"tokens"`
	Provenance      Provenance      `json:"provenance"`
	Truncated       bool            `json:"truncated"`
	OmittedCount    int             `json:"omittedCount,omitempty"`
}

// SanitizeDetail bounds and trims a free-text detail so a provider never stores a
// raw, unbounded payload.
func SanitizeDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) > maxDetail {
		detail = textutil.TruncateUTF8(detail, maxDetail) + "…"
	}
	return detail
}

// ErrCapabilityUnsupported is the typed error a provider returns for an
// unsupported query, distinct from an empty result. Maps to
// SEMANTIC_CAPABILITY_UNSUPPORTED at the API boundary.
var ErrCapabilityUnsupported = errors.New("SEMANTIC_CAPABILITY_UNSUPPORTED")
var ErrProviderRestarted = errors.New("SEMANTIC_PROVIDER_RESTARTED")
var ErrProviderPayloadInvalid = errors.New("SEMANTIC_PROVIDER_PAYLOAD_INVALID")
var ErrProviderStale = errors.New("SEMANTIC_PROVIDER_STALE")

const (
	ProviderStateAvailable   = "available"
	ProviderStateDisabled    = "disabled"
	ProviderStateUnavailable = "unavailable"
)

type ProviderState struct {
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// CapabilityUnsupported wraps ErrCapabilityUnsupported with the method name.
func CapabilityUnsupported(method string) error {
	return fmt.Errorf("%s: %w", method, ErrCapabilityUnsupported)
}
