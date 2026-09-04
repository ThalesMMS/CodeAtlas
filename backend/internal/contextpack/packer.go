package contextpack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

// Candidate is a scored retrieval candidate before budgeting. It carries the
// partially-built Evidence plus the ranking signals (graph distance, the source
// it came from, and per-list ranks for reciprocal-rank fusion).
type Candidate struct {
	Evidence       Evidence
	Distance       int
	Source         string
	LexicalRank    int // 1-based rank in the lexical list; 0 = not present
	PackageAPIRank int // 1-based package-export rank; 0 = not present
	IsTarget       bool
	Score          ScoreBreakdown
}

// ExpansionPolicy bounds graph expansion.
type ExpansionPolicy struct {
	MaxDepth  int
	MaxNodes  int
	MaxEdges  int
	EdgeTypes []string // allowlist; empty means all
}

// BudgetPolicy bounds the assembled pack.
type BudgetPolicy struct {
	MaxBytes            int
	MaxBytesPerEvidence int
	MaxEvidence         int
	MaxNodes            int
	MaxEdges            int
	ReserveBytes        int // reserved for instructions + model response
}

// ScoreBreakdown records the scoring components for debugging; only the total is
// used for ordering and only stable fields reach the LLM.
type ScoreBreakdown struct {
	Base          float64 `json:"base"`
	TargetBoost   float64 `json:"targetBoost"`
	GraphBoost    float64 `json:"graphBoost"`
	RelationBoost float64 `json:"relationBoost"`
	Penalty       float64 `json:"penalty"`
	Total         float64 `json:"total"`
}

// Policy parameterizes the packer per feature. Feature-specific policies arrive
// in #29–#32; this package ships a deterministic BasePolicy.
type Policy interface {
	Version() string
	Validate(ContextRequest) error
	Seeds(ctx context.Context, view repository.ReadView, request ContextRequest) ([]Candidate, error)
	Expansion() ExpansionPolicy
	Budget() BudgetPolicy
	Score(candidate Candidate, request ContextRequest) ScoreBreakdown
}

// OmissionProvider is an optional policy capability: it declares evidence
// categories that could not be supplied (e.g. an unimplemented source) as
// explicit omissions rather than omitting them silently.
type OmissionProvider interface {
	StaticOmissions(ContextRequest) []Omission
}

// Packer is the single retrieval engine. It dispatches to a registered policy by
// feature, fixes one ReadView for the whole build, and produces a canonical pack.
type Packer struct {
	store    repository.Store
	policies map[Feature]Policy
	cache    *packCache
	semantic SemanticSource
}

// NewPacker builds a packer over a store with per-feature policies and a small
// immutable cache keyed by snapshot + canonical request + policy version.
func NewPacker(repository repository.Store, policies map[Feature]Policy) *Packer {
	return &Packer{store: repository, policies: policies, cache: newPackCache(64)}
}

// Build checks the immutable cache from lightweight snapshot metadata before it
// materializes a ReadView. On a miss, one owned view sustains the whole build.
func (p *Packer) Build(ctx context.Context, request ContextRequest) (ContextPack, error) {
	policy, err := p.policyFor(request)
	if err != nil {
		return ContextPack{}, err
	}
	metadata, err := p.store.SnapshotMetadataContext(ctx)
	if err != nil {
		return ContextPack{}, err
	}
	if request.SnapshotID != "" && request.SnapshotID != metadata.ID {
		return ContextPack{}, apperror.StoreVersionConflict()
	}
	cacheKey := p.cacheKey(metadata.ID, request, policy)
	if cached, ok := p.cache.get(cacheKey); ok {
		return cached, nil
	}
	view, err := p.store.SnapshotContext(ctx)
	if err != nil {
		return ContextPack{}, err
	}
	defer view.Close() //nolint:errcheck
	return p.buildOnSnapshotView(ctx, request, policy, view)
}

// BuildOnSnapshotView builds and caches a pack on a caller-owned persisted view.
// Services that already pinned a view use this path so symbol resolution, graph
// expansion and pack evidence share one materialization and one snapshot.
func (p *Packer) BuildOnSnapshotView(ctx context.Context, request ContextRequest, view repository.ReadView) (ContextPack, error) {
	policy, err := p.policyFor(request)
	if err != nil {
		return ContextPack{}, err
	}
	return p.buildOnSnapshotView(ctx, request, policy, view)
}

func (p *Packer) buildOnSnapshotView(ctx context.Context, request ContextRequest, policy Policy, view repository.ReadView) (ContextPack, error) {
	metadata := view.Metadata()
	if request.SnapshotID != "" && request.SnapshotID != metadata.ID {
		return ContextPack{}, apperror.StoreVersionConflict()
	}
	cacheKey := p.cacheKey(metadata.ID, request, policy)
	if cached, ok := p.cache.get(cacheKey); ok {
		return cached, nil
	}
	pack, err := p.assemble(ctx, request, policy, view)
	if err != nil {
		return ContextPack{}, err
	}
	// A transient error is never cached; only a successful immutable pack is.
	p.cache.put(cacheKey, pack)
	return pack, nil
}

// BuildOnView builds a pack over a caller-provided transient view (for example an
// unsaved composite overlay). It is intentionally not cached because its content
// differs from the persisted snapshot named by its metadata.
func (p *Packer) BuildOnView(ctx context.Context, request ContextRequest, view repository.ReadView) (ContextPack, error) {
	policy, err := p.policyFor(request)
	if err != nil {
		return ContextPack{}, err
	}
	return p.assemble(ctx, request, policy, view)
}

func (p *Packer) policyFor(request ContextRequest) (Policy, error) {
	if err := ValidateRequest(request); err != nil {
		return nil, apperror.InvalidArgumentMessage("request", err.Error(), err)
	}
	policy, ok := p.policies[request.Feature]
	if !ok {
		return nil, apperror.InvalidArgumentMessage("feature", "no policy for feature "+string(request.Feature), nil)
	}
	if err := policy.Validate(request); err != nil {
		return nil, apperror.InvalidArgumentMessage("request", err.Error(), err)
	}
	return policy, nil
}

// assemble runs the deterministic seed→expand→rank→budget pipeline over a view.
func (p *Packer) assemble(ctx context.Context, request ContextRequest, policy Policy, view repository.ReadView) (ContextPack, error) {
	metadata := view.Metadata()
	if err := ctx.Err(); err != nil {
		return ContextPack{}, err
	}
	seeds, err := policy.Seeds(ctx, view, request)
	if err != nil {
		return ContextPack{}, err
	}
	if err := ctx.Err(); err != nil {
		return ContextPack{}, err
	}
	candidates, rawEdges := expandGraph(view, seeds, policy.Expansion())
	if err := ctx.Err(); err != nil {
		return ContextPack{}, err
	}
	ranked := rankAndDedup(candidates, request, policy)
	semanticOmissions := make([]Omission, 0)
	// Optional semantic enrichment (gopls/fused facts). The source uses this same
	// pinned view and applies its own deadline/fan-out; a mandatory-capability
	// failure fails the pack, an optional one is recorded as an omission. The
	// collector bounds its payload. Semantic facts join the same evidence budget
	// as AST candidates; a policy may explicitly rank its primary facts first.
	if semanticPolicy, ok := policy.(SemanticPolicy); ok && p.semantic != nil {
		methods, mandatory := semanticPolicy.SemanticMethods(request)
		if len(methods) > 0 {
			var documentVersion semantic.DocumentVersion
			if request.DocumentVersion != nil {
				documentVersion = semantic.DocumentVersion(*request.DocumentVersion)
			}
			collected, err := p.semantic.Collect(ctx, SemanticRequest{
				View: view, Feature: request.Feature, Location: request.Location, Methods: methods, Mandatory: mandatory,
				DocumentID: semantic.DocumentID(request.DocumentID), DocumentVersion: documentVersion,
				Content: request.Source, Preloaded: request.PreloadedSemantic,
			})
			if err != nil {
				if mandatory {
					return ContextPack{}, err
				}
				semanticOmissions = append(semanticOmissions, Omission{Reason: OmitSourceUnavailable, Ref: "semantic", Detail: "semantic source unavailable"})
			} else {
				priority, first := policy.(SemanticPriorityPolicy)
				primaryFirst := first && priority.SemanticEvidenceFirst()
				ranked = mergeSemanticEvidence(ranked, collected.Evidence, primaryFirst)
				semanticOmissions = append(semanticOmissions, collected.Omissions...)
			}
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return lessCandidate(ranked[i], ranked[j]) })
	evidence, graph, budget, omissions := applyBudget(ranked, rawEdges, policy.Budget())
	omissions = append(omissions, semanticOmissions...)
	if provider, ok := policy.(OmissionProvider); ok {
		omissions = append(omissions, provider.StaticOmissions(request)...)
	}
	return Finalize(ContextPack{
		Snapshot:      metadata,
		Feature:       request.Feature,
		Task:          Task{Query: request.Query},
		Target:        targetFor(request, seeds),
		Evidence:      evidence,
		Graph:         graph,
		Omissions:     omissions,
		Budget:        budget,
		PolicyVersion: policy.Version(),
	}), nil
}

func mergeSemanticEvidence(ranked []Candidate, evidence []Evidence, primaryFirst bool) []Candidate {
	hasPrimary := false
	for i := range evidence {
		if primaryFirst && evidence[i].Relation == "type" {
			evidence[i].Relevance = 1
			hasPrimary = true
		} else if primaryFirst && evidence[i].Relevance > 0.7 {
			evidence[i].Relevance = 0.7
		}
		ranked = append(ranked, Candidate{Evidence: evidence[i], Source: "semantic"})
	}
	if hasPrimary {
		for i := range ranked {
			if ranked[i].Source != "semantic" && ranked[i].Evidence.Relevance > 0.99 {
				ranked[i].Evidence.Relevance = 0.99
			}
		}
	}
	return ranked
}

// rankFusionConstant is the documented RRF constant (k=60, the common default).
const rankFusionConstant = 60.0

// rankAndDedup fuses the lexical/dense ranks via Reciprocal Rank Fusion, applies
// the policy's boosts/penalties, deduplicates by EvidenceID (merging provenance)
// and orders by a stable key.
func rankAndDedup(candidates []Candidate, request ContextRequest, policy Policy) []Candidate {
	byID := make(map[EvidenceID]*Candidate, len(candidates))
	order := make([]EvidenceID, 0, len(candidates))
	for i := range candidates {
		candidate := candidates[i]
		if candidate.Evidence.ID == "" {
			candidate.Evidence.ID = DeriveEvidenceID(candidate.Evidence)
		}
		id := candidate.Evidence.ID
		existing, ok := byID[id]
		if !ok {
			candidate.Evidence.Relevance = reciprocalRankFusion(candidate)
			candidate.Score = policy.Score(candidate, request)
			candidate.Evidence.Relevance = clamp01(candidate.Evidence.Relevance + candidate.Score.Total)
			stored := candidate
			byID[id] = &stored
			order = append(order, id)
			continue
		}
		// Duplicate: merge provenance and keep the closer/stronger signal.
		existing.Evidence.Provenance = append(existing.Evidence.Provenance, candidate.Evidence.Provenance...)
		if candidate.Distance < existing.Distance {
			existing.Distance = candidate.Distance
		}
	}
	result := make([]Candidate, 0, len(order))
	for _, id := range order {
		result = append(result, *byID[id])
	}
	sort.SliceStable(result, func(i, j int) bool { return lessCandidate(result[i], result[j]) })
	return result
}

// reciprocalRankFusion combines independent retrieval ranks; an absent list
// contributes nothing.
func reciprocalRankFusion(candidate Candidate) float64 {
	score := 0.0
	if candidate.LexicalRank > 0 {
		score += 1.0 / (rankFusionConstant + float64(candidate.LexicalRank))
	}
	if candidate.PackageAPIRank > 0 {
		score += 1.0 / (rankFusionConstant + float64(candidate.PackageAPIRank))
	}
	if candidate.IsTarget {
		score += 1.0 // the explicit target always ranks first
	}
	return score
}

// lessCandidate is the documented deterministic ordering: higher score first,
// then closer distance, then path, range, symbol/occurrence id.
func lessCandidate(a, b Candidate) bool {
	if a.Evidence.Relevance != b.Evidence.Relevance {
		return a.Evidence.Relevance > b.Evidence.Relevance
	}
	if a.Distance != b.Distance {
		return a.Distance < b.Distance
	}
	if a.Evidence.Path != b.Evidence.Path {
		return a.Evidence.Path < b.Evidence.Path
	}
	if a.Evidence.Range.Start.Line != b.Evidence.Range.Start.Line {
		return a.Evidence.Range.Start.Line < b.Evidence.Range.Start.Line
	}
	if a.Evidence.SymbolID != b.Evidence.SymbolID {
		return a.Evidence.SymbolID < b.Evidence.SymbolID
	}
	return a.Evidence.ID < b.Evidence.ID
}

// applyBudget enforces the byte/count budget. The target/signature evidence is
// never dropped for a marginally-higher candidate; every discard is an Omission.
func applyBudget(ranked []Candidate, rawEdges []domain.Edge, budget BudgetPolicy) ([]Evidence, CompactGraph, BudgetReport, []Omission) {
	available := budget.MaxBytes - budget.ReserveBytes
	if available <= 0 {
		available = budget.MaxBytes
	}
	used := 0
	evidence := make([]Evidence, 0, len(ranked))
	graph := CompactGraph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	omissions := make([]Omission, 0)
	dropped := map[string]int{}

	for _, candidate := range ranked {
		item := candidate.Evidence
		if budget.MaxBytesPerEvidence > 0 {
			item.Content = truncateContent(item.Content, budget.MaxBytesPerEvidence)
			wasTruncated := item.DisplayCodeTruncated
			var budgetTruncated bool
			item.DisplayCode, budgetTruncated = truncateCodeContent(item.DisplayCode, budget.MaxBytesPerEvidence)
			item.DisplayCodeTruncated = wasTruncated || budgetTruncated
		}
		size := len(item.Content) + len(item.Title)
		switch {
		case budget.MaxEvidence > 0 && len(evidence) >= budget.MaxEvidence && !candidate.IsTarget:
			dropped[OmitBudget]++
			continue
		case used+size > available && !candidate.IsTarget:
			dropped[OmitBudget]++
			continue
		}
		used += size
		evidence = append(evidence, item)
		if candidate.Source != "semantic" {
			node := GraphNode{EvidenceID: item.ID, SymbolID: item.SymbolID, Label: item.Title}
			if budget.MaxNodes == 0 || len(graph.Nodes) < budget.MaxNodes {
				graph.Nodes = append(graph.Nodes, node)
			} else {
				dropped[OmitBudget]++
			}
		}
	}

	// Graph edges reference EvidenceIDs and only connect included graph nodes;
	// an unresolved relation never invents a symbol or a dangling endpoint.
	idBySymbol := make(map[domain.SymbolID]EvidenceID, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if node.SymbolID != "" {
			idBySymbol[node.SymbolID] = node.EvidenceID
		}
	}
	for _, edge := range rawEdges {
		from, okFrom := idBySymbol[domain.SymbolID(edge.FromSymbolID)]
		to, okTo := idBySymbol[domain.SymbolID(edge.ToSymbolID)]
		if !okFrom || !okTo {
			continue
		}
		if budget.MaxEdges > 0 && len(graph.Edges) >= budget.MaxEdges {
			dropped[OmitBudget]++
			continue
		}
		graph.Edges = append(graph.Edges, GraphEdge{From: from, To: to, Type: edge.Type})
	}

	for reason, count := range dropped {
		omissions = append(omissions, Omission{Reason: reason, Detail: countDetail(count)})
	}
	return evidence, graph, BudgetReport{
		RequestedBytes:  budget.MaxBytes,
		UsedBytes:       used,
		EstimatedTokens: estimateTokens(used),
	}, omissions
}

// estimateTokens is a conservative, deterministic local estimate (~4 chars per
// token) — never a remote tokenizer.
func estimateTokens(bytes int) int { return (bytes + 3) / 4 }

func countDetail(count int) string {
	return strconv.Itoa(count) + " items"
}

// truncateContent trims to a byte budget on a UTF-8 boundary, preferring a line
// boundary, never splitting a rune.
func truncateContent(content string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(content) <= maxBytes {
		return content
	}
	trimmed := textutil.TruncateUTF8(content, maxBytes)
	if newline := strings.LastIndexByte(trimmed, '\n'); newline > maxBytes/2 {
		trimmed = trimmed[:newline]
	}
	return trimmed + "\n…"
}

// truncateCodeContent returns source bytes only through the last complete line
// within the budget. It never inserts a marker into the code or cuts a line; the
// renderer communicates truncation in the backend-owned block header.
func truncateCodeContent(content string, maxBytes int) (string, bool) {
	if content == "" || maxBytes <= 0 || len(content) <= maxBytes {
		return content, false
	}
	trimmed := textutil.TruncateUTF8(content, maxBytes)
	if newline := strings.LastIndexByte(trimmed, '\n'); newline >= 0 {
		return trimmed[:newline], true
	}
	return "", true
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// targetFor extracts the target symbol/occurrence from the seeds when one was
// marked as the explicit target.
func targetFor(request ContextRequest, seeds []Candidate) *Target {
	for _, seed := range seeds {
		if seed.IsTarget {
			rng := seed.Evidence.Range
			return &Target{
				SymbolID:     seed.Evidence.SymbolID,
				OccurrenceID: seed.Evidence.OccurrenceID,
				Path:         seed.Evidence.Path,
				Range:        &rng,
			}
		}
	}
	if request.Location != nil && request.Location.SymbolID != "" {
		return &Target{SymbolID: request.Location.SymbolID, Path: request.Location.Path}
	}
	return nil
}

// cacheKey segments the cache by snapshot, canonical request and policy version.
func (p *Packer) cacheKey(snapshotID domain.SnapshotID, request ContextRequest, policy Policy) string {
	canonical, _ := json.Marshal(request)
	digest := sha256.Sum256([]byte(string(snapshotID) + "\x00" + policy.Version() + "\x00" + string(canonical)))
	return hex.EncodeToString(digest[:])
}

// packCache is a small bounded LRU-ish cache of immutable packs.
type packCache struct {
	mu      sync.Mutex
	entries map[string]ContextPack
	order   []string
	limit   int
}

func newPackCache(limit int) *packCache {
	return &packCache{entries: make(map[string]ContextPack), limit: limit}
}

func (c *packCache) get(key string) (ContextPack, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pack, ok := c.entries[key]
	return pack, ok
}

func (c *packCache) put(key string, pack ContextPack) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		if len(c.order) >= c.limit {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
		c.order = append(c.order, key)
	}
	c.entries[key] = pack
}
