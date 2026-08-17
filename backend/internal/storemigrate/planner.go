package storemigrate

// Counts is an estimated entity census, for diagnostics only.
type Counts struct {
	Files      int `json:"files"`
	Symbols    int `json:"symbols"`
	Edges      int `json:"edges"`
	Embeddings int `json:"embeddings"`
	WikiPages  int `json:"wikiPages"`
}

// MigrationPlan is the deterministic plan produced before any write.
type MigrationPlan struct {
	Mode               Mode     `json:"mode"`
	SourceSchema       int      `json:"sourceSchema"`
	SourceSnapshotID   string   `json:"sourceSnapshotId"`
	TargetSchema       int      `json:"targetSchema"`
	RequiresEmbeddings bool     `json:"requiresEmbeddings"`
	ImportArtifacts    bool     `json:"importArtifacts"`
	Reasons            []string `json:"reasons"`
	EstimatedCounts    Counts   `json:"estimatedCounts"`
}

// PlanInput is the observed state the planner reasons over. It is gathered without
// writing anything.
type PlanInput struct {
	JSONExists          bool
	JSONSchema          int // the JSON snapshot's schema version
	SupportedJSONSchema int // the newest JSON schema this build can import
	SnapshotID          string
	IdentityCompatible  bool // SymbolIdentity/Occurrence v1 + identity algorithm match
	EmbeddingCompatible bool // embedding metadata compatible (when embeddings enabled)
	EmbeddingsEnabled   bool
	ForceRebuild        bool // user passed --rebuild
	TargetSchema        int
	Counts              Counts
}

// Plan selects the migration mode deterministically. The most conservative mode
// wins: a user may force a rebuild but can never force an incompatible import; any
// doubt about preserving logical meaning falls back to rebuild.
func Plan(input PlanInput) MigrationPlan {
	plan := MigrationPlan{
		SourceSchema: input.JSONSchema, SourceSnapshotID: input.SnapshotID,
		TargetSchema: input.TargetSchema, RequiresEmbeddings: input.EmbeddingsEnabled,
		ImportArtifacts: false, EstimatedCounts: input.Counts,
	}

	switch {
	case !input.JSONExists:
		plan.Mode = ModeFresh
		plan.Reasons = []string{"no prior JSON snapshot; fresh database with full initial index"}
	case input.ForceRebuild:
		plan.Mode = ModeRebuild
		plan.Reasons = []string{"rebuild requested by the user"}
	case input.JSONSchema <= 0 || input.JSONSchema > input.SupportedJSONSchema:
		plan.Mode = ModeRebuild
		plan.Reasons = []string{"JSON snapshot schema is unknown or newer than supported"}
	case !input.IdentityCompatible:
		plan.Mode = ModeRebuild
		plan.Reasons = []string{"identity/occurrence model incompatible; line-based ids would not preserve meaning"}
	case input.EmbeddingsEnabled && !input.EmbeddingCompatible:
		plan.Mode = ModeRebuild
		plan.Reasons = []string{"embedding metadata missing or incompatible while embeddings are required"}
	default:
		plan.Mode = ModeImport
		plan.ImportArtifacts = input.Counts.WikiPages > 0
		plan.Reasons = []string{"JSON snapshot is schema/identity compatible; compatible import"}
	}
	return plan
}
