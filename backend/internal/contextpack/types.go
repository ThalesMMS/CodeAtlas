// Package contextpack defines the executable ContextPack v1 contract: the typed
// ContextRequest every feature builds, the Evidence it cites, and the canonical,
// hashable ContextPack sent to a provider. The retrieval/ranking algorithm is
// #28; this package is only the contract, its validation, canonical hash and the
// trust-boundary serialization. Code/comment content is always carried in the
// Evidence.Content field and never concatenated into instructions.
package contextpack

import (
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// RelationUsageSite marks evidence captured from the immediate source context
// where the resolved target is used.
const RelationUsageSite = "usage_site"

// SchemaVersion is the contract version embedded in every pack and the JSON
// Schema (docs/schemas/context-pack-v1.schema.json).
const SchemaVersion = "context-pack/v1"

// Feature is the capability a pack is built for.
type Feature string

const (
	FeatureHover    Feature = "hover"
	FeatureSeeMore  Feature = "see_more"
	FeatureCodemap  Feature = "codemap"
	FeatureDeepWiki Feature = "deepwiki"
)

// DeepWiki scopes.
const (
	ScopeRepository = "repository"
	ScopeModule     = "module"
	ScopeSymbol     = "symbol"
)

// Evidence kinds distinguish the nature of a cited fact.
const (
	KindASTObservation     = "ast_observation"
	KindLSPFact            = "lsp_fact"
	KindComment            = "comment"
	KindTest               = "test"
	KindConfig             = "config"
	KindRetrievedInference = "retrieved_inference"
)

// Omission reasons.
const (
	OmitBudget            = "budget"
	OmitLowRelevance      = "low_relevance"
	OmitDuplicate         = "duplicate"
	OmitUnsupported       = "unsupported"
	OmitStale             = "stale"
	OmitSourceUnavailable = "source_unavailable"
)

// SourceLocation is a position in a file, optionally already resolved to a symbol.
type SourceLocation struct {
	Path     string          `json:"path"`
	Line     int             `json:"line"`
	Column   int             `json:"column"`
	SymbolID domain.SymbolID `json:"symbolId,omitempty"`
}

// ContextOptions are the per-request knobs. Unknown options are rejected at
// decode time via DisallowUnknownFields in the HTTP layer; the validator also
// bounds them.
type ContextOptions struct {
	MaxEvidence         int    `json:"maxEvidence,omitempty"`
	MaxBytesPerEvidence int    `json:"maxBytesPerEvidence,omitempty"`
	Scope               string `json:"scope,omitempty"`
	PinActiveView       bool   `json:"pinActiveView,omitempty"`
}

// RequestScope narrows a pack to explicit paths/symbols, validated against the
// pinned ReadView. Empty means repository-wide.
type RequestScope struct {
	Paths     []string          `json:"paths,omitempty"`
	SymbolIDs []domain.SymbolID `json:"symbolIds,omitempty"`
}

// ContextRequest is what every feature submits before any provider call.
type ContextRequest struct {
	Feature         Feature           `json:"feature"`
	SnapshotID      domain.SnapshotID `json:"snapshotId,omitempty"`
	Query           string            `json:"query,omitempty"`
	Location        *SourceLocation   `json:"location,omitempty"`
	SelectedRange   *domain.Range     `json:"selectedRange,omitempty"`
	Scope           *RequestScope     `json:"scope,omitempty"`
	DocumentVersion *int64            `json:"documentVersion,omitempty"`
	Options         ContextOptions    `json:"options"`

	// The fields below are request-local inputs supplied by services after exact
	// cursor resolution. They never cross the ContextPack JSON boundary. A
	// semantic-only target intentionally has no persisted SymbolID.
	ResolvedTarget    *ResolvedTarget            `json:"-"`
	DocumentID        string                     `json:"-"`
	Source            []byte                     `json:"-"`
	PreloadedSemantic map[string]SemanticOutcome `json:"-"`
}

// ResolvedTarget lets a service seed a policy with the exact target it already
// resolved. This prevents policies from independently falling back to a broader
// containing symbol when local variables or external declarations are absent
// from the structural index.
type ResolvedTarget struct {
	Symbol   domain.Symbol
	Evidence Evidence
}

// SemanticOutcome memoizes one semantic method for the duration of a ContextPack
// build. Facts used to resolve the target are reused as evidence instead of
// issuing duplicate LSP requests.
type SemanticOutcome struct {
	Facts []semantic.SemanticFact
	Err   error
}

// EvidenceID is deterministic within a pack, derived from the evidence reference
// and content — never from the slice index.
type EvidenceID string

// Provenance records where a fact came from; multiple sources never erase the
// origin.
type Provenance struct {
	Source string `json:"source"`
	Detail string `json:"detail,omitempty"`
}

// Evidence is one untrusted piece of context. Content is opaque and stays
// separate from instructions; Confidence is finite in [0,1].
type Evidence struct {
	ID           EvidenceID          `json:"id"`
	Kind         string              `json:"kind"`
	SymbolID     domain.SymbolID     `json:"symbolId,omitempty"`
	OccurrenceID domain.OccurrenceID `json:"occurrenceId,omitempty"`
	Path         string              `json:"path"`
	Range        domain.Range        `json:"range"`
	ContentHash  string              `json:"contentHash"`
	Title        string              `json:"title"`
	Content      string              `json:"content"`
	Relation     string              `json:"relation,omitempty"`
	Relevance    float64             `json:"relevance"`
	Provenance   []Provenance        `json:"provenance,omitempty"`
	Confidence   float64             `json:"confidence"`

	// DisplayCode is trusted renderer input derived from the indexed occurrence,
	// not model-visible ContextPack data. It deliberately stays out of JSON so the
	// prompt carries one code copy (Content) and the model can select only by ID.
	DisplayCode          string `json:"-"`
	DisplayLanguage      string `json:"-"`
	DisplayCodeTruncated bool   `json:"-"`
}

// Task is the sanitized instruction/query the pack answers.
type Task struct {
	Query       string `json:"query"`
	Instruction string `json:"instruction,omitempty"`
}

// Target is the symbol/occurrence the pack is about, when applicable.
type Target struct {
	SymbolID     domain.SymbolID     `json:"symbolId,omitempty"`
	OccurrenceID domain.OccurrenceID `json:"occurrenceId,omitempty"`
	Path         string              `json:"path,omitempty"`
	Range        *domain.Range       `json:"range,omitempty"`
}

// CompactGraph references evidence by EvidenceID, not by symbol, so the prompt
// stays self-contained.
type GraphNode struct {
	EvidenceID EvidenceID      `json:"evidenceId"`
	SymbolID   domain.SymbolID `json:"symbolId,omitempty"`
	Label      string          `json:"label,omitempty"`
}

type GraphEdge struct {
	From EvidenceID `json:"from"`
	To   EvidenceID `json:"to"`
	Type string     `json:"type"`
}

type CompactGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// Omission records an item left out and why.
type Omission struct {
	Reason string `json:"reason"`
	Ref    string `json:"ref,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// BudgetReport is the requested/used/estimated accounting. Estimates are
// excluded from the canonical hash (they are advisory, not content).
type BudgetReport struct {
	RequestedBytes  int `json:"requestedBytes"`
	UsedBytes       int `json:"usedBytes"`
	EstimatedTokens int `json:"estimatedTokens"`
}

// ContextPack is the canonical, hashable bundle every feature sends to a provider.
type ContextPack struct {
	SchemaVersion string                  `json:"schemaVersion"`
	ID            string                  `json:"id"`
	Hash          string                  `json:"hash"`
	Snapshot      domain.SnapshotMetadata `json:"snapshot"`
	Feature       Feature                 `json:"feature"`
	Task          Task                    `json:"task"`
	Target        *Target                 `json:"target,omitempty"`
	Evidence      []Evidence              `json:"evidence"`
	Graph         CompactGraph            `json:"graph"`
	Omissions     []Omission              `json:"omissions"`
	Budget        BudgetReport            `json:"budget"`
	PolicyVersion string                  `json:"policyVersion"`
	GeneratedAt   time.Time               `json:"generatedAt"`
}
