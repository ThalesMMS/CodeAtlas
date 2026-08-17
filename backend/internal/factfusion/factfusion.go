// Package factfusion deterministically merges semantic facts from multiple
// sources (Tree-sitter AST, gopls, future runtime traces) into canonical logical
// relations. It preserves every source's provenance, confidence and limitation:
// it never collapses provenances into one string, never sums confidences, and
// records conflicts instead of silently choosing. Stale-version facts are dropped
// before fusion.
package factfusion

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// PolicyVersion versions the canonicalization + confidence rules.
const PolicyVersion = "fusion.v1"

// maxEvidencePerRelation bounds evidences kept per relation.
const maxEvidencePerRelation = 32

// RelationKind is the canonical logical relation. `contains` stays a separate
// structural relation and is not produced here.
type RelationKind string

const (
	RelCalls      RelationKind = "calls"
	RelReferences RelationKind = "references"
	RelImplements RelationKind = "implements"
	RelDefinition RelationKind = "definition"
)

// RelationKey identifies a logical relation. Direction is part of the key, so
// A→B is never equal to B→A. An unresolved end uses an explicit External key and
// never gets an invented SymbolID.
type RelationKey struct {
	Kind      RelationKind    `json:"kind"`
	Direction string          `json:"direction"`
	SubjectID domain.SymbolID `json:"subjectId,omitempty"`
	ObjectID  domain.SymbolID `json:"objectId,omitempty"`
	External  string          `json:"external,omitempty"`
}

// FactEvidence is one source's observation backing a relation. Each keeps its own
// provenance and confidence.
type FactEvidence struct {
	FactID          string                    `json:"factId,omitempty"`
	Provenance      semantic.Provenance       `json:"provenance"`
	Confidence      float64                   `json:"confidence"`
	Location        semantic.SourceLocation   `json:"location"`
	Related         []semantic.SourceLocation `json:"related,omitempty"`
	Detail          string                    `json:"detail,omitempty"`
	VersionKnown    bool                      `json:"versionKnown"`
	DocumentVersion semantic.DocumentVersion  `json:"documentVersion,omitempty"`
}

// FactConflict records incompatible observations that fusion refuses to resolve.
type FactConflict struct {
	Reason  string `json:"reason"`
	Subject string `json:"subject,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// FusedRelation is one canonical relation with all its evidences.
type FusedRelation struct {
	Key               RelationKey        `json:"key"`
	Subject           semantic.SymbolRef `json:"subject"`
	Object            semantic.SymbolRef `json:"object"`
	Evidences         []FactEvidence     `json:"evidences"`
	SupportCount      int                `json:"supportCount"`
	RankingConfidence float64            `json:"rankingConfidence"`
	Resolution        string             `json:"resolution"`
	Conflicts         []FactConflict     `json:"conflicts,omitempty"`
}

// FusionInput is the immutable input. Facts must already be scoped to this view.
type FusionInput struct {
	ViewHash        string
	SnapshotID      domain.SnapshotID
	DocumentID      semantic.DocumentID
	DocumentVersion semantic.DocumentVersion
	ASTFacts        []semantic.SemanticFact
	ProviderFacts   []semantic.SemanticFact
}

// FusionResult is the immutable output.
type FusionResult struct {
	Relations     []FusedRelation         `json:"relations"`
	NonRelational []semantic.SemanticFact `json:"nonRelational"`
	Conflicts     []FactConflict          `json:"conflicts"`
	Omissions     []string                `json:"omissions"`
	PolicyVersion string                  `json:"policyVersion"`
}

const (
	resolutionResolved   = "resolved"
	resolutionUnresolved = "unresolved"
	resolutionBySemantic = "resolved_by_semantic_provider"
)

// Merge fuses AST and provider facts into canonical relations.
func Merge(input FusionInput) (FusionResult, error) {
	result := FusionResult{PolicyVersion: PolicyVersion, Relations: []FusedRelation{}, NonRelational: []semantic.SemanticFact{}, Conflicts: []FactConflict{}, Omissions: []string{}}

	groups := map[RelationKey]*FusedRelation{}
	add := func(fact semantic.SemanticFact) {
		if !p.inScope(input, fact) {
			result.Omissions = append(result.Omissions, "dropped out-of-scope fact "+fact.Kind)
			return
		}
		key, subject, object, relational := relationFor(fact)
		if !relational {
			result.NonRelational = append(result.NonRelational, copyFact(fact))
			return
		}
		relation, ok := groups[key]
		if !ok {
			relation = &FusedRelation{Key: key, Subject: subject, Object: object, Resolution: resolutionFor(key)}
			groups[key] = relation
		}
		relation.Evidences = append(relation.Evidences, evidenceOf(fact))
	}
	for _, fact := range input.ASTFacts {
		add(fact)
	}
	for _, fact := range input.ProviderFacts {
		add(fact)
	}

	// Correlate an AST call resolved only by name with a gopls-resolved call from
	// the same subject when the candidate name matches — a strong, observable link.
	correlateUnresolved(groups)

	for _, relation := range groups {
		finalizeRelation(relation)
		result.Relations = append(result.Relations, *relation)
		result.Conflicts = append(result.Conflicts, relation.Conflicts...)
	}
	sortRelations(result.Relations)
	sort.Strings(result.Omissions)
	return result, nil
}

// inScope rejects facts from a different snapshot/view or document version, or a
// stale overlay version. Diagnostics with VersionKnown=false are allowed (per #46)
// only while the document version matches the input.
type scope struct{}

var p scope

func (scope) inScope(input FusionInput, fact semantic.SemanticFact) bool {
	if fact.SnapshotID != "" && input.SnapshotID != "" && fact.SnapshotID != input.SnapshotID {
		return false
	}
	if fact.DocumentID != "" {
		if fact.DocumentID != input.DocumentID || fact.DocumentVersion != input.DocumentVersion {
			return false
		}
	}
	return true
}

// relationFor canonicalizes a fact into a relation key + refs, or marks it
// non-relational (hover/diagnostic).
func relationFor(fact semantic.SemanticFact) (RelationKey, semantic.SymbolRef, semantic.SymbolRef, bool) {
	switch fact.Kind {
	case semantic.KindSyntacticCall, semantic.KindCallOutgoing:
		// subject (caller) → object (callee)
		return callKey(fact.Subject, refOf(fact.Object, fact)), fact.Subject, refOf(fact.Object, fact), true
	case semantic.KindCallIncoming:
		// object (caller) → subject (target)
		caller := refOf(fact.Object, fact)
		return callKey(caller, fact.Subject), caller, fact.Subject, true
	case semantic.KindReference:
		object := fact.Subject // the referenced symbol
		subject := semantic.SymbolRef{Name: locationName(fact.Location)}
		return RelationKey{Kind: RelReferences, Direction: "ref", SubjectID: subject.SymbolID, ObjectID: object.SymbolID, External: externalOf(subject, object)}, subject, object, true
	case semantic.KindImplementation:
		return RelationKey{Kind: RelImplements, Direction: "impl", SubjectID: fact.Subject.SymbolID, ObjectID: objectID(fact.Object), External: implExternal(fact)}, fact.Subject, refOf(fact.Object, fact), true
	case semantic.KindDefinition:
		object := semantic.SymbolRef{Name: locationName(fact.Location)}
		return RelationKey{Kind: RelDefinition, Direction: "def", SubjectID: fact.Subject.SymbolID, External: fact.Location.Path}, fact.Subject, object, true
	default:
		return RelationKey{}, semantic.SymbolRef{}, semantic.SymbolRef{}, false
	}
}

func callKey(caller, callee semantic.SymbolRef) RelationKey {
	return RelationKey{Kind: RelCalls, Direction: "caller_to_callee", SubjectID: caller.SymbolID, ObjectID: callee.SymbolID, External: externalOf(caller, callee)}
}

func objectID(ref *semantic.SymbolRef) domain.SymbolID {
	if ref != nil {
		return ref.SymbolID
	}
	return ""
}

func refOf(ref *semantic.SymbolRef, _ semantic.SemanticFact) semantic.SymbolRef {
	if ref != nil {
		return *ref
	}
	return semantic.SymbolRef{}
}

// externalOf returns an explicit external key when the object is unresolved.
func externalOf(_ semantic.SymbolRef, object semantic.SymbolRef) string {
	if object.SymbolID == "" {
		return object.Name
	}
	return ""
}

func implExternal(fact semantic.SemanticFact) string {
	if fact.Object != nil && fact.Object.SymbolID == "" {
		return fact.Object.Name
	}
	return ""
}

func resolutionFor(key RelationKey) string {
	if key.ObjectID == "" && key.External != "" {
		return resolutionUnresolved
	}
	return resolutionResolved
}

// correlateUnresolved merges an unresolved call relation into a resolved one when
// the caller matches and the resolved callee name equals the unresolved external
// name. The AST evidence is preserved; resolution becomes resolved_by_semantic_provider.
func correlateUnresolved(groups map[RelationKey]*FusedRelation) {
	for key, unresolved := range groups {
		if key.Kind != RelCalls || key.ObjectID != "" || key.External == "" {
			continue
		}
		for resolvedKey, resolved := range groups {
			if resolvedKey.Kind != RelCalls || resolvedKey.ObjectID == "" {
				continue
			}
			if resolvedKey.SubjectID != key.SubjectID {
				continue
			}
			if strings.EqualFold(resolved.Object.Name, key.External) {
				resolved.Evidences = append(resolved.Evidences, unresolved.Evidences...)
				resolved.Resolution = resolutionBySemantic
				delete(groups, key)
				break
			}
		}
	}
}

// finalizeRelation dedupes/orders evidences, computes the ranking confidence as
// the MAX of valid evidences (never a sum), applies conflict/version penalties,
// and detects conflicting resolved objects.
func finalizeRelation(relation *FusedRelation) {
	relation.Evidences = dedupeEvidences(relation.Evidences)
	sortEvidences(relation.Evidences)
	truncated := false
	if len(relation.Evidences) > maxEvidencePerRelation {
		relation.Evidences = relation.Evidences[:maxEvidencePerRelation]
		truncated = true
	}
	relation.SupportCount = len(relation.Evidences)

	best := 0.0
	versionUnknown := false
	for _, evidence := range relation.Evidences {
		if evidence.Confidence > best {
			best = evidence.Confidence
		}
		if !evidence.VersionKnown && evidence.DocumentVersion != 0 {
			versionUnknown = true
		}
	}
	relation.RankingConfidence = best
	if len(relation.Conflicts) > 0 {
		relation.RankingConfidence *= 0.5 // conflicting relations rank lower
	}
	if versionUnknown && relation.RankingConfidence > 0.6 {
		relation.RankingConfidence = 0.6 // unprovable version caps confidence
	}
	if truncated {
		relation.Conflicts = append(relation.Conflicts, FactConflict{Reason: "evidence_truncated", Detail: fmt.Sprintf("kept %d evidences", maxEvidencePerRelation)})
	}
}

func dedupeEvidences(evidences []FactEvidence) []FactEvidence {
	seen := map[string]struct{}{}
	out := evidences[:0]
	for _, evidence := range evidences {
		key := evidence.FactID
		if key == "" {
			key = fmt.Sprintf("%s|%s|%d:%d", evidence.Provenance.Source, evidence.Location.Path, evidence.Location.Range.Start.Line, evidence.Location.Range.Start.Column)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, evidence)
	}
	return out
}

func evidenceOf(fact semantic.SemanticFact) FactEvidence {
	return FactEvidence{
		FactID: fact.ID, Provenance: fact.Provenance, Confidence: fact.Confidence,
		Location: fact.Location, Related: append([]semantic.SourceLocation(nil), fact.Related...),
		Detail: fact.Detail, VersionKnown: fact.VersionKnown, DocumentVersion: fact.DocumentVersion,
	}
}

func copyFact(fact semantic.SemanticFact) semantic.SemanticFact {
	fact.Related = append([]semantic.SourceLocation(nil), fact.Related...)
	return fact
}

func locationName(loc semantic.SourceLocation) string { return loc.Path }

func sortEvidences(evidences []FactEvidence) {
	sort.SliceStable(evidences, func(i, j int) bool {
		a, b := evidences[i], evidences[j]
		if a.Provenance.Source != b.Provenance.Source {
			return a.Provenance.Source < b.Provenance.Source
		}
		if a.Provenance.Method != b.Provenance.Method {
			return a.Provenance.Method < b.Provenance.Method
		}
		if a.Location.Path != b.Location.Path {
			return a.Location.Path < b.Location.Path
		}
		return a.FactID < b.FactID
	})
}

func sortRelations(relations []FusedRelation) {
	sort.SliceStable(relations, func(i, j int) bool {
		a, b := relations[i].Key, relations[j].Key
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.SubjectID != b.SubjectID {
			return a.SubjectID < b.SubjectID
		}
		if a.ObjectID != b.ObjectID {
			return a.ObjectID < b.ObjectID
		}
		return a.External < b.External
	})
}
