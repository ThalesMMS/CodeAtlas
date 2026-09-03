// Package aiout defines the structured, grounded output contracts the model must
// return for each AI feature (Hover/See More, Codemap narrative, Wiki page). The
// model returns only these typed shapes referencing allowlisted EvidenceIDs — it
// never emits free Markdown, paths, ranges or links. The backend validates every
// reference against the ContextPack and renders safe Markdown/HTML itself.
//
// Each output is versioned independently of the ContextPack so an output-schema
// change does not perturb pack hashes.
package aiout

// Schema versions, embedded in every output and recorded in artifact metadata.
const (
	ExplanationSchemaVersion = "explanation/v2"
	CodemapSchemaVersion     = "codemap-narrative/v2"
	WikiPageSchemaVersion    = "wiki-page/v4"
	WikiPlanSchemaVersion    = "wiki-plan/v1"
)

// Field/size limits keep a single response bounded regardless of the provider.
const (
	MaxClaims                 = 24
	MaxClaimRunes             = 1200
	MaxEvidencePerClaim       = 8
	MaxCodeEvidence           = 3
	MaxSections               = 12
	MaxTraceSteps             = 16
	MaxCodemapFlows           = 8
	MaxFlowSteps              = 16
	MinCodemapOverviewRunes   = 96
	MinCodemapMotivationRunes = 180
	MinCodemapDetailsRunes    = 320
	MaxCodemapNarrativeRunes  = 4000
	MinFlowSummaryRunes       = 12
	MinFlowMotivationRunes    = 80
	MinFlowDetailsRunes       = 120
	MaxStepNotes              = 4
	MaxStepNoteRunes          = 200
	MaxAnchorTextRunes        = 240
	MaxWikiTables             = 6
	MaxTableColumns           = 6
	MaxTableRows              = 20
	MaxRelatedPages           = 12
)

// Claim is a factual statement grounded by one or more EvidenceIDs from the pack.
type Claim struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidenceIds"`
}

// Inference is a non-factual deduction: it still cites support and carries a
// finite confidence in [0,1].
type Inference struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidenceIds"`
	Confidence  float64  `json:"confidence"`
}

// Uncertainty is something the model could not determine; if it cites no
// evidence it must give a non-empty reason.
type Uncertainty struct {
	Text        string   `json:"text"`
	Reason      string   `json:"reason"`
	EvidenceIDs []string `json:"evidenceIds"`
}

// Explanation is the Hover/See More structured output. Hover may omit
// ChangeImpact; See More must populate it.
type Explanation struct {
	SchemaVersion   string        `json:"schemaVersion"`
	Summary         string        `json:"summary"`
	CodeEvidenceIDs []string      `json:"codeEvidenceIds,omitempty"`
	Observations    []Claim       `json:"observations"`
	Inferences      []Inference   `json:"inferences"`
	Uncertainties   []Uncertainty `json:"uncertainties"`
	ChangeImpact    []Claim       `json:"changeImpact"`
}

// CodemapFlowStep is a model-authored label over a backend-owned node. Source
// anchors and snippets are attached after validation; the model cannot provide
// paths, lines, or code bytes. AnchorText is an optional verbatim line copied
// from the step node's provided snippet: the backend re-locates it in the
// snippet to anchor the step at a specific line inside the symbol, and ignores
// it when it does not match. Notes are short clarifying leaf labels rendered
// under the step in the trace tree.
type CodemapFlowStep struct {
	Label      string   `json:"label"`
	NodeID     string   `json:"nodeId"`
	Text       string   `json:"text"`
	AnchorText string   `json:"anchorText,omitempty"`
	Notes      []string `json:"notes,omitempty"`
}

// CodemapFlow groups validated steps around one backend-suggested entrypoint.
// Summary, Motivation and Details are the per-section narrative rendered as
// each chapter's guide; older outputs may omit them.
type CodemapFlow struct {
	Title       string            `json:"title"`
	EntryNodeID string            `json:"entryNodeId"`
	Summary     string            `json:"summary,omitempty"`
	Motivation  string            `json:"motivation,omitempty"`
	Details     string            `json:"details,omitempty"`
	Steps       []CodemapFlowStep `json:"steps"`
}

// CodemapNarrative is the Codemap structured output. Flow entry/step IDs are
// validated node IDs, trace references are structural IDs, and claims cite
// EvidenceIDs. It never introduces nodes, paths, ranges, or snippets.
type CodemapNarrative struct {
	SchemaVersion string        `json:"schemaVersion"`
	Title         string        `json:"title"`
	Overview      string        `json:"overview"`
	Motivation    string        `json:"motivation"`
	Details       string        `json:"details"`
	Trace         []string      `json:"trace"`
	Flows         []CodemapFlow `json:"flows"`
	Claims        []Claim       `json:"claims"`
	Inferences    []Inference   `json:"inferences"`
	Uncertainties []Uncertainty `json:"uncertainties"`
}

// WikiSection is one typed section of a wiki page; its claims cite EvidenceIDs.
type WikiSection struct {
	Heading         string      `json:"heading"`
	Claims          []Claim     `json:"claims"`
	CodeEvidenceIDs []string    `json:"codeEvidenceIds,omitempty"`
	Tables          []WikiTable `json:"tables,omitempty"`
}

// WikiTable is a bounded, grounded tabular block. Cell text is model-authored,
// while evidenceIds must resolve against the page Context Pack.
type WikiTable struct {
	Kind        string     `json:"kind"`
	Columns     []string   `json:"columns"`
	Rows        [][]string `json:"rows"`
	EvidenceIDs []string   `json:"evidenceIds"`
}

// WikiPageContent is the DeepWiki structured output. Links are logical references
// resolved by the backend, never written by the model.
type WikiPageContent struct {
	SchemaVersion string        `json:"schemaVersion"`
	Title         string        `json:"title"`
	Sections      []WikiSection `json:"sections"`
	RelatedPages  []string      `json:"relatedPages"`
	Inferences    []Inference   `json:"inferences"`
	Limitations   []Uncertainty `json:"limitations"`
}

// WikiPagePlan is one planner-proposed page. Scope paths and hierarchy are
// validated by the service against the pinned repository snapshot.
type WikiPagePlan struct {
	Slug       string   `json:"slug"`
	Title      string   `json:"title"`
	ParentSlug string   `json:"parentSlug"`
	ScopePaths []string `json:"scopePaths"`
	Archetype  string   `json:"archetype"`
}

type WikiPlan struct {
	SchemaVersion string         `json:"schemaVersion"`
	Pages         []WikiPagePlan `json:"pages"`
}
