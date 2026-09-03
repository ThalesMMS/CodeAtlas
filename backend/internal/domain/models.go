package domain

import "time"

type File struct {
	Path             string    `json:"path"`
	Language         string    `json:"language"`
	Hash             string    `json:"hash"`
	Size             int64     `json:"size"`
	ModifiedAt       time.Time `json:"modifiedAt"`
	IndexedAt        time.Time `json:"indexedAt"`
	Summary          string    `json:"summary,omitempty"`
	Content          string    `json:"content,omitempty"`
	ContentTruncated bool      `json:"contentTruncated,omitempty"`
}

type Position struct {
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Encoding string `json:"encoding,omitempty"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Symbol is the flat read/transport DTO. Its ID is the logical v1 SymbolID (or
// the OccurrenceID for an occurrence-only symbol); OccurrenceID identifies the
// physical occurrence the path/range/code came from. A semantic-only Explain
// target such as a local variable or external method deliberately leaves both
// IDs empty and carries its exact usage path/range plus LSP name/kind/signature.
// Navigation should open by OccurrenceID/path/range, not store the range as
// identity.
type Symbol struct {
	ID            string `json:"id"`
	OccurrenceID  string `json:"occurrenceId,omitempty"`
	Path          string `json:"path"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualifiedName"`
	Kind          string `json:"kind"`
	Language      string `json:"language"`
	Range         Range  `json:"range"`
	Signature     string `json:"signature,omitempty"`
	Code          string `json:"code,omitempty"`
	DocComment    string `json:"docComment,omitempty"`
	Summary       string `json:"summary,omitempty"`
}

type Edge struct {
	ID           int64   `json:"id,omitempty"`
	FromSymbolID string  `json:"fromSymbolId"`
	ToSymbolID   string  `json:"toSymbolId,omitempty"`
	ToName       string  `json:"toName"`
	Type         string  `json:"type"`
	Path         string  `json:"path"`
	Line         int     `json:"line"`
	Confidence   float64 `json:"confidence"`
}

type ParsedFile struct {
	File    File     `json:"file"`
	Symbols []Symbol `json:"symbols"`
	Edges   []Edge   `json:"edges"`
}

// SymbolID identifies the logical symbol (the entity "method Service.Submit"),
// independent of where or how it currently appears. OccurrenceID identifies one
// physical appearance of that identity in a particular index state.
type SymbolID string
type OccurrenceID string

// SymbolIdentity is the logical symbol. It deliberately carries no path, range,
// body or timestamp, so it survives line shifts and body edits; a rename, parent
// change or incompatible signature may yield a new identity per #24's policy.
type SymbolIdentity struct {
	ID                   SymbolID `json:"id"`
	Language             string   `json:"language"`
	Kind                 string   `json:"kind"`
	Name                 string   `json:"name"`
	QualifiedName        string   `json:"qualifiedName"`
	ParentID             SymbolID `json:"parentId,omitempty"`
	SignatureFingerprint string   `json:"signatureFingerprint,omitempty"`
}

// SymbolOccurrence is where an identity appears in one index state: its location
// and textual evidence. It changes when the range, body, displayed signature or
// file changes. OccurrenceID is deterministic from non-circular data
// (SymbolID+path+range+bodyHash) and never embeds the SnapshotID. An occurrence
// without a reliable logical identity is marked OccurrenceOnly with an empty
// SymbolID.
type SymbolOccurrence struct {
	ID             OccurrenceID `json:"id"`
	SymbolID       SymbolID     `json:"symbolId,omitempty"`
	OccurrenceOnly bool         `json:"occurrenceOnly,omitempty"`
	Path           string       `json:"path"`
	Range          Range        `json:"range"`
	Signature      string       `json:"signature,omitempty"`
	Code           string       `json:"code,omitempty"`
	DocComment     string       `json:"docComment,omitempty"`
	Summary        string       `json:"summary,omitempty"`
	BodyHash       string       `json:"bodyHash"`
	FileHash       string       `json:"fileHash,omitempty"`
}

// ResolvedSymbol composes an identity with one occurrence. Internal code works
// with the split model; boundaries that need the legacy flat shape call ToSymbol.
type ResolvedSymbol struct {
	Identity   SymbolIdentity   `json:"identity"`
	Occurrence SymbolOccurrence `json:"occurrence"`
}

// ToSymbol flattens a ResolvedSymbol into the legacy monolithic Symbol for the
// edges (search hits, UI DTOs) that still expect it. The occurrence id becomes
// the flat Symbol.ID when the occurrence is identity-less, so a flat consumer
// still has a stable handle.
func (r ResolvedSymbol) ToSymbol() Symbol {
	id := string(r.Occurrence.SymbolID)
	if id == "" {
		id = string(r.Occurrence.ID)
	}
	return Symbol{
		ID:            id,
		OccurrenceID:  string(r.Occurrence.ID),
		Path:          r.Occurrence.Path,
		Name:          r.Identity.Name,
		QualifiedName: r.Identity.QualifiedName,
		Kind:          r.Identity.Kind,
		Language:      r.Identity.Language,
		Range:         r.Occurrence.Range,
		Signature:     r.Occurrence.Signature,
		Code:          r.Occurrence.Code,
		DocComment:    r.Occurrence.DocComment,
		Summary:       r.Occurrence.Summary,
	}
}

type FileTreeNode struct {
	Name     string         `json:"name"`
	Path     string         `json:"path"`
	Type     string         `json:"type"`
	Language string         `json:"language,omitempty"`
	Children []FileTreeNode `json:"children,omitempty"`
}

type SearchHit struct {
	Symbol     Symbol     `json:"symbol"`
	Snippet    string     `json:"snippet"`
	Score      float64    `json:"score"`
	Source     string     `json:"source"`
	SnapshotID SnapshotID `json:"snapshotId,omitempty"`
}

type EvidenceProvenance struct {
	Source string `json:"source"`
	Detail string `json:"detail,omitempty"`
}

type Evidence struct {
	ID            string               `json:"id,omitempty"`
	Kind          string               `json:"kind,omitempty"`
	Path          string               `json:"path"`
	Range         Range                `json:"range"`
	SymbolID      string               `json:"symbolId,omitempty"`
	OccurrenceID  string               `json:"occurrenceId,omitempty"`
	Title         string               `json:"title"`
	Content       string               `json:"content"`
	Code          string               `json:"code,omitempty"`
	Language      string               `json:"language,omitempty"`
	CodeTruncated bool                 `json:"codeTruncated,omitempty"`
	Relation      string               `json:"relation,omitempty"`
	Relevance     float64              `json:"relevance,omitempty"`
	Confidence    float64              `json:"confidence,omitempty"`
	Provenance    []EvidenceProvenance `json:"provenance,omitempty"`
}

type ExplainFeature string

const (
	ExplainFeatureHover   ExplainFeature = "hover"
	ExplainFeatureSeeMore ExplainFeature = "see_more"
)

type ExplainTarget struct {
	SymbolID     string `json:"symbolId,omitempty"`
	OccurrenceID string `json:"occurrenceId,omitempty"`
}

type ExplainRequest struct {
	Feature         ExplainFeature `json:"feature,omitempty"`
	DocumentID      string         `json:"documentId,omitempty"`
	DocumentVersion int64          `json:"documentVersion,omitempty"`
	Path            string         `json:"path"`
	Position        *Position      `json:"position,omitempty"`
	Target          *ExplainTarget `json:"target,omitempty"`
	Line            int            `json:"line,omitempty"`   // legacy transport
	Column          int            `json:"column,omitempty"` // legacy transport
	Depth           string         `json:"depth,omitempty"`  // legacy transport
}

type NavigationKind string

const (
	NavigationKindDefinition     NavigationKind = "definition"
	NavigationKindReferences     NavigationKind = "references"
	NavigationKindImplementation NavigationKind = "implementation"
	NavigationKindIncomingCalls  NavigationKind = "incoming_calls"
	NavigationKindOutgoingCalls  NavigationKind = "outgoing_calls"
)

type NavigationPosition struct {
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Encoding string `json:"encoding"`
}

type NavigationRequest struct {
	Kind            NavigationKind      `json:"kind"`
	DocumentID      string              `json:"documentId,omitempty"`
	DocumentVersion int64               `json:"documentVersion,omitempty"`
	Path            string              `json:"path,omitempty"`
	Position        *NavigationPosition `json:"position,omitempty"`
	SymbolID        string              `json:"symbolId,omitempty"`
	OccurrenceID    string              `json:"occurrenceId,omitempty"`
	Limit           int                 `json:"limit,omitempty"`
}

type NavigationSubject struct {
	SymbolID      string `json:"symbolId,omitempty"`
	OccurrenceID  string `json:"occurrenceId,omitempty"`
	Path          string `json:"path,omitempty"`
	Range         Range  `json:"range,omitempty"`
	Name          string `json:"name,omitempty"`
	QualifiedName string `json:"qualifiedName,omitempty"`
	Kind          string `json:"kind,omitempty"`
}

type NavigationTarget struct {
	TargetID       string               `json:"targetId"`
	SymbolID       string               `json:"symbolId,omitempty"`
	OccurrenceID   string               `json:"occurrenceId,omitempty"`
	Path           string               `json:"path,omitempty"`
	Range          Range                `json:"range,omitempty"`
	SelectionRange Range                `json:"selectionRange,omitempty"`
	Label          string               `json:"label"`
	Kind           string               `json:"kind,omitempty"`
	Relationship   string               `json:"relationship"`
	Provenance     []EvidenceProvenance `json:"provenance,omitempty"`
	Confidence     float64              `json:"confidence"`
	External       bool                 `json:"external"`
}

type NavigationOmission struct {
	Reason string `json:"reason"`
	Ref    string `json:"ref,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type NavigationResult struct {
	Kind             NavigationKind       `json:"kind"`
	SnapshotID       SnapshotID           `json:"snapshotId"`
	ViewHash         string               `json:"viewHash,omitempty"`
	DocumentID       string               `json:"documentId,omitempty"`
	DocumentVersion  int64                `json:"documentVersion,omitempty"`
	Subject          NavigationSubject    `json:"subject"`
	Targets          []NavigationTarget   `json:"targets"`
	SemanticCoverage map[string]any       `json:"semanticCoverage"`
	Total            int                  `json:"total"`
	Truncated        bool                 `json:"truncated"`
	Omissions        []NavigationOmission `json:"omissions"`
}

type ExplanationClaim struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidenceIds"`
}

type ExplanationInference struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidenceIds"`
	Confidence  float64  `json:"confidence"`
}

type ExplanationUncertainty struct {
	Text        string   `json:"text"`
	Reason      string   `json:"reason,omitempty"`
	EvidenceIDs []string `json:"evidenceIds,omitempty"`
}

type ExplanationResult struct {
	SchemaVersion   string                   `json:"schemaVersion"`
	Summary         string                   `json:"summary"`
	CodeEvidenceIDs []string                 `json:"codeEvidenceIds,omitempty"`
	Observations    []ExplanationClaim       `json:"observations"`
	Inferences      []ExplanationInference   `json:"inferences"`
	Uncertainties   []ExplanationUncertainty `json:"uncertainties"`
	ChangeImpact    []ExplanationClaim       `json:"changeImpact"`
}

type ProviderInfo struct {
	ID    string `json:"id"`
	Model string `json:"model,omitempty"`
}

type Explanation struct {
	Feature             ExplainFeature    `json:"feature,omitempty"`
	Symbol              Symbol            `json:"symbol"`
	Summary             string            `json:"summary"`
	Markdown            string            `json:"markdown"`
	Evidence            []Evidence        `json:"evidence"`
	Provider            string            `json:"provider"`
	ProviderInfo        ProviderInfo      `json:"providerInfo,omitempty"`
	SemanticCoverage    map[string]any    `json:"semanticCoverage,omitempty"`
	Result              ExplanationResult `json:"result,omitempty"`
	GeneratedAt         time.Time         `json:"generatedAt"`
	SnapshotID          SnapshotID        `json:"snapshotId,omitempty"`
	Revision            Revision          `json:"revision,omitempty"`
	OccurrenceID        string            `json:"occurrenceId,omitempty"`
	ContextPackHash     string            `json:"contextPackHash,omitempty"`
	PolicyVersion       string            `json:"policyVersion,omitempty"`
	PromptVersion       string            `json:"promptVersion,omitempty"`
	OutputSchemaVersion string            `json:"outputSchemaVersion,omitempty"`
	Ephemeral           bool              `json:"ephemeral,omitempty"`
	ViewHash            string            `json:"viewHash,omitempty"`
	DocumentID          string            `json:"documentId,omitempty"`
	DocumentVersion     int64             `json:"documentVersion,omitempty"`
	Artifact            ArtifactMetadata  `json:"artifact"`
}

type CodemapRequest struct {
	Query    string `json:"query"`
	MaxNodes int    `json:"maxNodes,omitempty"`
}

type CodemapNode struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Kind      string  `json:"kind"`
	Path      string  `json:"path"`
	Range     Range   `json:"range"`
	Summary   string  `json:"summary"`
	Snippet   string  `json:"snippet,omitempty"`
	Group     string  `json:"group"`
	Relevance float64 `json:"relevance"`
}

type CodemapEdge struct {
	ID         string  `json:"id"`
	Source     string  `json:"source"`
	Target     string  `json:"target"`
	Type       string  `json:"type"`
	Label      string  `json:"label"`
	Path       string  `json:"path,omitempty"`
	Line       int     `json:"line,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Snippet    string  `json:"snippet,omitempty"`
}

// CodemapFlowStep is a validated narrative step with a source anchor and code
// line selected by the backend from the factual graph. Depth is the derived
// call nesting level (0 = flow root; a step called by the previous anchored
// caller is one level deeper). Notes are short model-authored clarifications
// rendered as leaf labels under the step.
type CodemapFlowStep struct {
	Label   string   `json:"label"`
	NodeID  string   `json:"nodeId"`
	Text    string   `json:"text"`
	Path    string   `json:"path,omitempty"`
	Line    int      `json:"line,omitempty"`
	Snippet string   `json:"snippet,omitempty"`
	Depth   int      `json:"depth,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// CodemapFlow is one presentation section: a titled run of anchored steps with
// an optional per-section narrative (summary line plus motivation/details
// guide) authored by the model and validated by the backend.
type CodemapFlow struct {
	Title       string            `json:"title"`
	EntryNodeID string            `json:"entryNodeId"`
	Summary     string            `json:"summary,omitempty"`
	Motivation  string            `json:"motivation,omitempty"`
	Details     string            `json:"details,omitempty"`
	Steps       []CodemapFlowStep `json:"steps"`
}

// DiagramSource is a backend-owned source reference for a deterministic
// diagram node. It lets clients navigate the factual inputs without parsing
// Mermaid text.
type DiagramSource struct {
	NodeID string `json:"nodeId"`
	Label  string `json:"label"`
	Path   string `json:"path"`
	Range  Range  `json:"range"`
}

// MermaidDiagram is a deterministic presentation derived from the factual
// graph. The model never authors Source or Sources.
type MermaidDiagram struct {
	Version string          `json:"version"`
	Kind    string          `json:"kind"`
	Source  string          `json:"source"`
	Sources []DiagramSource `json:"sources"`
}

type Codemap struct {
	Query               string           `json:"query"`
	Title               string           `json:"title"`
	Summary             string           `json:"summary,omitempty"`
	Motivation          string           `json:"motivation,omitempty"`
	Details             string           `json:"details,omitempty"`
	Overview            string           `json:"overview"`
	Trace               []string         `json:"trace"`
	Flows               []CodemapFlow    `json:"flows,omitempty"`
	Nodes               []CodemapNode    `json:"nodes"`
	Edges               []CodemapEdge    `json:"edges"`
	Diagram             *MermaidDiagram  `json:"diagram,omitempty"`
	Provider            string           `json:"provider"`
	GeneratedAt         time.Time        `json:"generatedAt"`
	SnapshotID          SnapshotID       `json:"snapshotId,omitempty"`
	ContextPackHash     string           `json:"contextPackHash,omitempty"`
	PolicyVersion       string           `json:"policyVersion,omitempty"`
	OutputSchemaVersion string           `json:"outputSchemaVersion,omitempty"`
	Artifact            ArtifactMetadata `json:"artifact"`
}

// CodemapSummary is a lightweight listing entry for a published Codemap head.
// It lets clients reopen previously generated maps without transferring full
// payloads; Status/StaleReason mirror the artifact head so outdated maps are
// never presented as current.
type CodemapSummary struct {
	ArtifactID  ArtifactID `json:"artifactId"`
	Title       string     `json:"title"`
	Query       string     `json:"query"`
	Status      string     `json:"status"`
	StaleReason string     `json:"staleReason,omitempty"`
	Revision    int        `json:"revision"`
	NodeCount   int        `json:"nodeCount"`
	EdgeCount   int        `json:"edgeCount"`
	Provider    string     `json:"provider,omitempty"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type WikiPage struct {
	Slug                string          `json:"slug"`
	Title               string          `json:"title"`
	Kind                string          `json:"kind,omitempty"`
	Archetype           string          `json:"archetype,omitempty"`
	ModuleSlug          string          `json:"moduleSlug,omitempty"`
	ParentSlug          string          `json:"parentSlug,omitempty"`
	ScopePaths          []string        `json:"scopePaths,omitempty"`
	RelatedPages        []WikiPageLink  `json:"relatedPages,omitempty"`
	Markdown            string          `json:"markdown"`
	Diagram             *MermaidDiagram `json:"diagram,omitempty"`
	SourceHash          string          `json:"sourceHash"`
	ContextPackHash     string          `json:"contextPackHash,omitempty"`
	PolicyVersion       string          `json:"policyVersion,omitempty"`
	OutputSchemaVersion string          `json:"outputSchemaVersion,omitempty"`
	Provider            string          `json:"provider"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

type WikiPageLink struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Relation string `json:"relation"`
}

// WikiModule is a deterministically detected DeepWiki scope: a package/directory
// grouped by versioned heuristics, with a stable slug and an explicit reason it
// forms a boundary. Ignored/vendored/generated paths never become modules.
type WikiModule struct {
	Slug           string   `json:"slug"`
	Name           string   `json:"name"`
	Language       string   `json:"language"`
	Paths          []string `json:"paths"`
	BoundaryReason string   `json:"boundaryReason"`
	Entrypoint     bool     `json:"entrypoint,omitempty"`
	FileCount      int      `json:"fileCount"`
	SymbolCount    int      `json:"symbolCount"`
}

// WikiManifestEntry declares a page the manifest expects, whether it is required,
// and — when omitted — the reason, decided before generation.
type WikiManifestEntry struct {
	Slug       string   `json:"slug"`
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	Archetype  string   `json:"archetype,omitempty"`
	ParentSlug string   `json:"parentSlug,omitempty"`
	ModuleSlug string   `json:"moduleSlug,omitempty"`
	ScopePaths []string `json:"scopePaths,omitempty"`
	Required   bool     `json:"required"`
	Omitted    bool     `json:"omitted,omitempty"`
	OmitReason string   `json:"omitReason,omitempty"`
}

// WikiManifest is the versioned plan of a DeepWiki collection: detected modules
// and the required/optional pages with their omission reasons.
type WikiManifest struct {
	Version   string              `json:"version"`
	Modules   []WikiModule        `json:"modules"`
	Pages     []WikiManifestEntry `json:"pages"`
	Omissions []string            `json:"omissions,omitempty"`
}

// DeepWikiStatus is the explicit lifecycle state of the DeepWiki collection.
type DeepWikiStatus string

const (
	// DeepWikiNotGenerated: the capability exists but no version was ever created.
	DeepWikiNotGenerated DeepWikiStatus = "not_generated"
	// DeepWikiReady: a valid version exists for the current snapshot.
	DeepWikiReady DeepWikiStatus = "ready"
	// DeepWikiStale: a previous version exists but the index it was built from changed.
	DeepWikiStale DeepWikiStatus = "stale"
	// DeepWikiGenerating: a generation job is currently active.
	DeepWikiGenerating DeepWikiStatus = "generating"
	// DeepWikiFailed: the last generation attempt failed; a prior valid version may remain.
	DeepWikiFailed DeepWikiStatus = "failed"
)

// DeepWikiGeneration identifies an in-flight DeepWiki generation.
type DeepWikiGeneration struct {
	ID             string    `json:"id"`
	StartedAt      time.Time `json:"startedAt"`
	SnapshotID     string    `json:"snapshotId,omitempty"`
	CurrentPage    string    `json:"currentPage,omitempty"`
	CompletedPages int       `json:"completedPages,omitempty"`
	TotalPages     int       `json:"totalPages,omitempty"`
}

// DeepWikiCollection is the on-demand artifact envelope returned by the API. A
// caller can distinguish absence, staleness, generation, success and failure
// without inferring anything from an empty page list.
type DeepWikiCollection struct {
	Status     DeepWikiStatus      `json:"status"`
	SnapshotID string              `json:"snapshotId"`
	Pages      []WikiPage          `json:"pages"`
	Manifest   *WikiManifest       `json:"manifest,omitempty"`
	Generation *DeepWikiGeneration `json:"generation"`
	LastError  string              `json:"lastError,omitempty"`
	Artifact   ArtifactMetadata    `json:"artifact"`
}

// Revision is the local, monotonic commit counter used for optimistic control
// and ordering. It is a distinct type so it is never accidentally swapped with a
// raw uint64. It starts at 0 for an unpublished empty store; the first real
// commit produces 1; no-ops and aborted commits never advance it.
type Revision uint64

// SnapshotID is the content-addressed identifier of the repository intelligence
// state, in the form "sha256:<hex>". Canonically identical state always yields
// the same id, regardless of timestamps, map order or generated artifacts.
type SnapshotID string

// SnapshotMetadata is the published provenance of a store state: its
// content-addressed id, its monotonic revision, when it was created and the
// schema version that defines the canonical hash.
type SnapshotMetadata struct {
	ID                SnapshotID `json:"snapshotId"`
	Revision          Revision   `json:"revision"`
	CreatedAt         time.Time  `json:"createdAt"`
	Schema            int        `json:"schema"`
	IdentityAlgorithm string     `json:"identityAlgorithm"`
}

// ArtifactID identifies one generated-artifact version (a DeepWiki page, a
// Codemap, an explanation). ArtifactRevision is the artifact's own monotonic
// version, independent of the structural Revision.
type ArtifactID string
type ArtifactRevision uint64

// Artifact lifecycle states. They are independent of readiness and of the
// structural snapshot.
const (
	ArtifactCurrent    = "current"
	ArtifactStale      = "stale"
	ArtifactGenerating = "generating"
	ArtifactFailed     = "failed"
)

// ArtifactMetadata is the provenance of a generated artifact, separate from the
// structural SnapshotID. It declares which snapshot, prompt, model and explicit
// dependencies produced the content, so staleness is detectable without
// re-running the model.
type ArtifactMetadata struct {
	ID              ArtifactID       `json:"artifactId"`
	Type            string           `json:"type"`
	Key             string           `json:"key"`
	Revision        ArtifactRevision `json:"artifactRevision"`
	InputSnapshotID SnapshotID       `json:"inputSnapshotId"`
	ContextPackHash string           `json:"contextPackHash"`
	PromptVersion   string           `json:"promptVersion"`
	OutputSchema    string           `json:"outputSchema,omitempty"`
	Provider        string           `json:"provider"`
	Model           string           `json:"model"`
	Status          string           `json:"status"`
	Dependencies    []Dependency     `json:"dependencies,omitempty"`
	StaleReasons    []string         `json:"staleReasons,omitempty"`
	CreatedAt       time.Time        `json:"createdAt"`
}

// Dependency is one piece of evidence an artifact was built from. ContentHash
// lets staleness be detected when the underlying occurrence/file changes.
type Dependency struct {
	Kind         string       `json:"kind"`
	SymbolID     SymbolID     `json:"symbolId,omitempty"`
	OccurrenceID OccurrenceID `json:"occurrenceId,omitempty"`
	Path         string       `json:"path,omitempty"`
	ContentHash  string       `json:"contentHash"`
}

// EmbeddingIndexMetadata describes the single compatible configuration of a
// persisted dense index. It never contains the API key or a credentialed URL;
// Provider is a stable technical identifier.
type EmbeddingIndexMetadata struct {
	Enabled         bool      `json:"enabled"`
	Provider        string    `json:"provider"`
	Model           string    `json:"model"`
	Dimension       int       `json:"dimension"`
	TemplateVersion string    `json:"templateVersion"`
	Distance        string    `json:"distance"`
	BuiltAt         time.Time `json:"builtAt"`
}

type WorkspaceStats struct {
	Workspace   string    `json:"workspace"`
	Files       int       `json:"files"`
	Symbols     int       `json:"symbols"`
	Edges       int       `json:"edges"`
	WikiPages   int       `json:"wikiPages"`
	IndexedAt   time.Time `json:"indexedAt"`
	Indexing    bool      `json:"indexing"`
	LastError   string    `json:"lastError,omitempty"`
	LLMProvider string    `json:"llmProvider"`
}

// WatchOperation is the scheduler-facing bitmask for normalized filesystem
// hints. Filesystem operations are diagnostic hints only; reconciliation always
// reads the final state from disk before committing an index ChangeSet.
type WatchOperation uint32

const (
	OpCreate WatchOperation = 1 << iota
	OpWrite
	OpRemove
	OpRename
	OpChmod
	OpRescanRequired
)

func (op WatchOperation) Has(flag WatchOperation) bool { return op&flag != 0 }

const (
	ReconcilePaths   = "paths"
	ReconcileSubtree = "subtree"
	ReconcileFull    = "full"
)

type ChangeHint struct {
	Sequence   uint64         `json:"sequence"`
	Path       string         `json:"path"`
	Operations WatchOperation `json:"operations"`
	Kind       string         `json:"kind"`
	ReasonCode string         `json:"reasonCode,omitempty"`
}

type ReconcileScope struct {
	Mode        string   `json:"mode"`
	Paths       []string `json:"paths,omitempty"`
	Root        string   `json:"root,omitempty"`
	ReasonCodes []string `json:"reasonCodes,omitempty"`
	MinSequence uint64   `json:"minSequence,omitempty"`
	MaxSequence uint64   `json:"maxSequence,omitempty"`
}

type ReconcileRequest struct {
	ID        string         `json:"id"`
	Scope     ReconcileScope `json:"scope"`
	CreatedAt time.Time      `json:"createdAt"`
	Priority  int            `json:"priority,omitempty"`
}

const (
	SchedulerIdle           = "idle"
	SchedulerDebouncing     = "debouncing"
	SchedulerQueued         = "queued"
	SchedulerReconciling    = "reconciling"
	SchedulerDesynchronized = "desynchronized"
	SchedulerFailed         = "failed"
)

type SchedulerState struct {
	Mode                  string         `json:"mode"`
	EffectiveMode         string         `json:"effectiveMode"`
	State                 string         `json:"state"`
	CurrentRequestID      string         `json:"currentRequestId,omitempty"`
	CurrentScope          ReconcileScope `json:"currentScope,omitempty"`
	PendingPathCount      int            `json:"pendingPathCount"`
	PendingFull           bool           `json:"pendingFull"`
	LastCommit            time.Time      `json:"lastCommit,omitempty"`
	LastNoop              time.Time      `json:"lastNoop,omitempty"`
	LastError             string         `json:"lastError,omitempty"`
	LastPeriodicReconcile time.Time      `json:"lastPeriodicReconcile,omitempty"`
	WatcherMissedChanges  int            `json:"watcherMissedChanges"`
	FallbackReason        string         `json:"fallbackReason,omitempty"`
	PollInterval          time.Duration  `json:"pollInterval,omitempty"`
	ReconcileInterval     time.Duration  `json:"reconcileInterval,omitempty"`
}

// PublicError is a sanitized error surfaced in an event. It never carries file
// content or secrets.
type PublicError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type JobID string
type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobCanceling JobState = "canceling"
	JobCanceled  JobState = "canceled"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobStale     JobState = "stale"
)

type Progress struct {
	Completed     int64    `json:"completed,omitempty"`
	Total         int64    `json:"total,omitempty"`
	Unit          string   `json:"unit,omitempty"`
	Percent       *float64 `json:"percent,omitempty"`
	Indeterminate bool     `json:"indeterminate,omitempty"`
}

type JobSnapshot struct {
	ID               JobID        `json:"id"`
	Type             string       `json:"type"`
	Key              string       `json:"key"`
	State            JobState     `json:"state"`
	Stage            string       `json:"stage,omitempty"`
	Message          string       `json:"message,omitempty"`
	Progress         Progress     `json:"progress"`
	InputSnapshotID  SnapshotID   `json:"inputSnapshotId,omitempty"`
	ResultArtifactID string       `json:"resultArtifactId,omitempty"`
	ResultSnapshotID SnapshotID   `json:"resultSnapshotId,omitempty"`
	Error            *PublicError `json:"error,omitempty"`
	ClientRequestID  string       `json:"clientRequestId,omitempty"`
	CreatedAt        time.Time    `json:"createdAt"`
	StartedAt        time.Time    `json:"startedAt,omitempty"`
	FinishedAt       time.Time    `json:"finishedAt,omitempty"`
	Revision         uint64       `json:"revision"`
}

type JobFilter struct {
	Type  string
	State JobState
	Limit int
}

type JobEvent struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"`
	Job       JobSnapshot `json:"job"`
	Timestamp time.Time   `json:"timestamp"`
}

// IndexEvent is the commit-aware event envelope. Progress events describe work
// in flight and promise no visible change; commit events are emitted only after
// the store and on-disk snapshot have advanced. Every event has a unique,
// monotonically increasing ID so a client can detect a gap.
type IndexEvent struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	OperationID  string         `json:"operationId,omitempty"`
	ChangeSetID  string         `json:"changeSetId,omitempty"`
	StoreVersion uint64         `json:"storeVersion,omitempty"`
	SnapshotID   SnapshotID     `json:"snapshotId,omitempty"`
	Revision     Revision       `json:"revision,omitempty"`
	Paths        []string       `json:"paths,omitempty"`
	Counts       map[string]int `json:"counts,omitempty"`
	Error        *PublicError   `json:"error,omitempty"`
	// Path and Message remain for compact progress events and backward compat.
	Path      string    `json:"path,omitempty"`
	Message   string    `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}
