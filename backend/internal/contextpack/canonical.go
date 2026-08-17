package contextpack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// validScopes / validKinds / validOmissions bound the enum fields.
var (
	validScopes    = map[string]struct{}{ScopeRepository: {}, ScopeModule: {}, ScopeSymbol: {}}
	validKinds     = map[string]struct{}{KindASTObservation: {}, KindLSPFact: {}, KindComment: {}, KindTest: {}, KindConfig: {}, KindRetrievedInference: {}}
	validOmissions = map[string]struct{}{OmitBudget: {}, OmitLowRelevance: {}, OmitDuplicate: {}, OmitUnsupported: {}, OmitStale: {}, OmitSourceUnavailable: {}}
	validFeatures  = map[Feature]struct{}{FeatureHover: {}, FeatureSeeMore: {}, FeatureCodemap: {}, FeatureDeepWiki: {}}
)

// ValidateRequest enforces the per-feature contract. A request that pins the
// active view may omit SnapshotID (the packer fixes it before retrieval).
func ValidateRequest(request ContextRequest) error {
	if _, ok := validFeatures[request.Feature]; !ok {
		return fmt.Errorf("unknown feature %q", request.Feature)
	}
	if request.SnapshotID == "" && !request.Options.PinActiveView {
		return fmt.Errorf("snapshotId is required unless options.pinActiveView is set")
	}
	if request.Options.MaxEvidence < 0 || request.Options.MaxBytesPerEvidence < 0 {
		return fmt.Errorf("budget options must be non-negative")
	}
	switch request.Feature {
	case FeatureHover, FeatureSeeMore:
		if request.Location == nil || (request.Location.Path == "" && request.Location.SymbolID == "") {
			return fmt.Errorf("%s requires a location with a path or symbolId", request.Feature)
		}
	case FeatureCodemap:
		if strings.TrimSpace(request.Query) == "" {
			return fmt.Errorf("codemap requires a non-empty query")
		}
	case FeatureDeepWiki:
		if _, ok := validScopes[request.Options.Scope]; !ok {
			return fmt.Errorf("deepwiki requires options.scope in {repository, module, symbol}")
		}
	}
	return nil
}

// ValidatePack checks a fully built pack for contract violations (used by tests
// and before serialization). It rejects invalid enums, out-of-range confidence
// and dangling graph references.
func ValidatePack(pack ContextPack) error {
	if pack.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion = %q, want %q", pack.SchemaVersion, SchemaVersion)
	}
	if _, ok := validFeatures[pack.Feature]; !ok {
		return fmt.Errorf("unknown feature %q", pack.Feature)
	}
	ids := make(map[EvidenceID]struct{}, len(pack.Evidence))
	for i, evidence := range pack.Evidence {
		if evidence.ID == "" {
			return fmt.Errorf("evidence[%d] has no id", i)
		}
		if _, dup := ids[evidence.ID]; dup {
			return fmt.Errorf("duplicate evidence id %q", evidence.ID)
		}
		ids[evidence.ID] = struct{}{}
		if _, ok := validKinds[evidence.Kind]; !ok {
			return fmt.Errorf("evidence %q has unknown kind %q", evidence.ID, evidence.Kind)
		}
		if !isFinite01(evidence.Confidence) || !isFinite01(evidence.Relevance) {
			return fmt.Errorf("evidence %q has confidence/relevance outside [0,1]", evidence.ID)
		}
	}
	nodeIDs := make(map[EvidenceID]struct{}, len(pack.Graph.Nodes))
	for _, node := range pack.Graph.Nodes {
		if _, ok := ids[node.EvidenceID]; !ok {
			return fmt.Errorf("graph node references unknown evidence %q", node.EvidenceID)
		}
		nodeIDs[node.EvidenceID] = struct{}{}
	}
	for _, edge := range pack.Graph.Edges {
		if _, ok := nodeIDs[edge.From]; !ok {
			return fmt.Errorf("graph edge from missing node %q", edge.From)
		}
		if _, ok := nodeIDs[edge.To]; !ok {
			return fmt.Errorf("graph edge to missing node %q", edge.To)
		}
	}
	for _, omission := range pack.Omissions {
		if _, ok := validOmissions[omission.Reason]; !ok {
			return fmt.Errorf("unknown omission reason %q", omission.Reason)
		}
	}
	return nil
}

func isFinite01(value float64) bool {
	return value == value && value >= 0 && value <= 1 // NaN != NaN
}

// DeriveEvidenceID is deterministic from the evidence reference and content,
// never from a slice index, so re-ordering evidence cannot change ids.
func DeriveEvidenceID(evidence Evidence) EvidenceID {
	payload := strings.Join([]string{
		evidence.Kind, evidence.Path, rangeKey(evidence.Range),
		string(evidence.SymbolID), string(evidence.OccurrenceID), evidence.ContentHash,
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return EvidenceID("ev:" + hex.EncodeToString(digest[:10]))
}

// Finalize assigns missing evidence ids, orders evidence (relevance desc, then
// id), graph and omissions deterministically, and stamps the canonical hash and
// content-addressed id. Two calls with the same content produce the same hash.
func Finalize(pack ContextPack) ContextPack {
	pack.SchemaVersion = SchemaVersion
	// Normalize nil collections to empty so the contract always emits [] (never
	// null) and the canonical hash is stable.
	if pack.Evidence == nil {
		pack.Evidence = []Evidence{}
	}
	if pack.Omissions == nil {
		pack.Omissions = []Omission{}
	}
	if pack.Graph.Nodes == nil {
		pack.Graph.Nodes = []GraphNode{}
	}
	if pack.Graph.Edges == nil {
		pack.Graph.Edges = []GraphEdge{}
	}
	for i := range pack.Evidence {
		if pack.Evidence[i].ID == "" {
			pack.Evidence[i].ID = DeriveEvidenceID(pack.Evidence[i])
		}
	}
	sort.SliceStable(pack.Evidence, func(i, j int) bool {
		if pack.Evidence[i].Relevance != pack.Evidence[j].Relevance {
			return pack.Evidence[i].Relevance > pack.Evidence[j].Relevance
		}
		return pack.Evidence[i].ID < pack.Evidence[j].ID
	})
	sort.SliceStable(pack.Graph.Nodes, func(i, j int) bool { return pack.Graph.Nodes[i].EvidenceID < pack.Graph.Nodes[j].EvidenceID })
	sort.SliceStable(pack.Graph.Edges, func(i, j int) bool {
		if pack.Graph.Edges[i].From != pack.Graph.Edges[j].From {
			return pack.Graph.Edges[i].From < pack.Graph.Edges[j].From
		}
		if pack.Graph.Edges[i].To != pack.Graph.Edges[j].To {
			return pack.Graph.Edges[i].To < pack.Graph.Edges[j].To
		}
		return pack.Graph.Edges[i].Type < pack.Graph.Edges[j].Type
	})
	sort.SliceStable(pack.Omissions, func(i, j int) bool {
		if pack.Omissions[i].Reason != pack.Omissions[j].Reason {
			return pack.Omissions[i].Reason < pack.Omissions[j].Reason
		}
		return pack.Omissions[i].Ref < pack.Omissions[j].Ref
	})
	pack.Hash = CanonicalHash(pack)
	pack.ID = "ctxpack:" + strings.TrimPrefix(pack.Hash, "sha256:")[:16]
	return pack
}

// CanonicalHash hashes the content-bearing fields of a pack, excluding volatile
// id/hash/timestamp, so the same request over the same ReadView and policy yields
// the same hash, and any change to sent evidence changes it.
func CanonicalHash(pack ContextPack) string {
	canonical := struct {
		SchemaVersion string                  `json:"schemaVersion"`
		Snapshot      domain.SnapshotMetadata `json:"snapshot"`
		Feature       Feature                 `json:"feature"`
		Task          Task                    `json:"task"`
		Target        *Target                 `json:"target,omitempty"`
		Evidence      []Evidence              `json:"evidence"`
		Graph         CompactGraph            `json:"graph"`
		Omissions     []Omission              `json:"omissions"`
		Budget        BudgetReport            `json:"budget"`
		PolicyVersion string                  `json:"policyVersion"`
	}{
		SchemaVersion: SchemaVersion, Snapshot: pack.Snapshot, Feature: pack.Feature, Task: pack.Task,
		Target: pack.Target, Evidence: pack.Evidence, Graph: pack.Graph, Omissions: pack.Omissions,
		Budget: pack.Budget, PolicyVersion: pack.PolicyVersion,
	}
	encoded, _ := json.Marshal(canonical)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// promptDelimiter wraps the pack in a single, clearly-bounded block. The pack is
// emitted as one JSON object, so every code/comment field is a JSON string value
// (escaped by the encoder) and can never break out into the system instructions.
const promptDelimiter = "CODEATLAS_CONTEXT_PACK"

// SerializeForPrompt renders the pack as a single delimited, JSON-escaped block
// for inclusion in a prompt. Untrusted content stays inside JSON string values.
func SerializeForPrompt(pack ContextPack) (string, error) {
	encoded, err := json.Marshal(pack)
	if err != nil {
		return "", err
	}
	builder := strings.Builder{}
	builder.WriteString("<" + promptDelimiter + ">\n")
	builder.Write(encoded)
	builder.WriteString("\n</" + promptDelimiter + ">")
	return builder.String(), nil
}

func rangeKey(r domain.Range) string {
	return strconv.Itoa(r.Start.Line) + ":" + strconv.Itoa(r.Start.Column) + "-" +
		strconv.Itoa(r.End.Line) + ":" + strconv.Itoa(r.End.Column)
}
