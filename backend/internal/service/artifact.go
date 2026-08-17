package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// Prompt versions per artifact type. Any incompatible change to a prompt must
// bump its version so existing artifacts become stale.
const (
	PromptVersionExplain  = "explain.v2"
	PromptVersionCodemap  = "codemap-v2"
	PromptVersionDeepWiki = "deepwiki-v4"
)

// Artifact types.
const (
	ArtifactTypeExplain  = "explain"
	ArtifactTypeCodemap  = "codemap"
	ArtifactTypeDeepWiki = "deepwiki"
)

// dependencyKindSnapshot marks a global dependency on the whole structural
// snapshot (used by repo-wide artifacts), which a SnapshotID change invalidates.
const dependencyKindSnapshot = "snapshot"

// SymbolLookup resolves a symbol by its handle for artifact freshness checks.
type SymbolLookup interface {
	GetSymbol(id string) (domain.Symbol, bool)
}

// dependencyForSymbol builds a deterministic dependency from a flat symbol.
func dependencyForSymbol(symbol domain.Symbol) domain.Dependency {
	return domain.Dependency{
		Kind:         "symbol",
		SymbolID:     domain.SymbolID(symbol.ID),
		OccurrenceID: domain.OccurrenceID(symbol.OccurrenceID),
		Path:         symbol.Path,
		ContentHash:  symbolContentHash(symbol),
	}
}

// snapshotDependency is the global dependency for a repo-wide artifact.
func snapshotDependency(snapshotID domain.SnapshotID) domain.Dependency {
	return domain.Dependency{Kind: dependencyKindSnapshot, ContentHash: string(snapshotID)}
}

func symbolContentHash(symbol domain.Symbol) string {
	digest := sha256.Sum256([]byte(symbol.QualifiedName + "\x00" + symbol.Signature + "\x00" + symbol.DocComment + "\x00" + symbol.Code))
	return hex.EncodeToString(digest[:])[:16]
}

func explainArtifactKey(symbol domain.Symbol) string {
	if symbol.ID != "" {
		return symbol.ID
	}
	payload := strings.Join([]string{
		symbol.Path, symbol.Name, symbol.Kind, symbol.Signature,
		fmt.Sprintf("%d:%d-%d:%d", symbol.Range.Start.Line, symbol.Range.Start.Column, symbol.Range.End.Line, symbol.Range.End.Column),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return "semantic:" + hex.EncodeToString(digest[:12])
}

// sortDependencies orders and de-duplicates dependencies so the ContextPackHash
// is independent of evidence order.
func sortDependencies(deps []domain.Dependency) []domain.Dependency {
	seen := make(map[string]struct{}, len(deps))
	unique := make([]domain.Dependency, 0, len(deps))
	for _, dep := range deps {
		key := dep.Kind + "\x00" + string(dep.SymbolID) + "\x00" + string(dep.OccurrenceID) + "\x00" + dep.Path + "\x00" + dep.ContentHash
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, dep)
	}
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Kind != unique[j].Kind {
			return unique[i].Kind < unique[j].Kind
		}
		if unique[i].SymbolID != unique[j].SymbolID {
			return unique[i].SymbolID < unique[j].SymbolID
		}
		return unique[i].ContentHash < unique[j].ContentHash
	})
	return unique
}

// contextPackHash is the deterministic digest of the (sorted, deduped) evidence
// an artifact was built from. Identical evidence over any snapshot yields the
// same hash, so an artifact can be re-pinned to a new snapshot without
// regeneration when its evidence is unchanged.
func contextPackHash(deps []domain.Dependency) string {
	builder := strings.Builder{}
	for _, dep := range deps {
		builder.WriteString(dep.Kind)
		builder.WriteByte(0)
		builder.WriteString(string(dep.SymbolID))
		builder.WriteByte(0)
		builder.WriteString(dep.ContentHash)
		builder.WriteByte(0)
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return "ctx:" + hex.EncodeToString(digest[:16])
}

// buildArtifactMetadata assembles provenance for a freshly generated artifact.
// The ArtifactID is content-addressed over the key, input snapshot and context
// pack, so a re-generation over identical evidence is idempotent.
func buildArtifactMetadata(artifactType, key, promptVersion, provider, model string, inputSnapshotID domain.SnapshotID, revision domain.ArtifactRevision, deps []domain.Dependency, now time.Time) domain.ArtifactMetadata {
	sorted := sortDependencies(deps)
	packHash := contextPackHash(sorted)
	idDigest := sha256.Sum256([]byte(artifactType + "\x00" + key + "\x00" + string(inputSnapshotID) + "\x00" + packHash + "\x00" + promptVersion))
	return domain.ArtifactMetadata{
		ID:              domain.ArtifactID("art:" + hex.EncodeToString(idDigest[:12])),
		Type:            artifactType,
		Key:             key,
		Revision:        revision,
		InputSnapshotID: inputSnapshotID,
		ContextPackHash: packHash,
		PromptVersion:   promptVersion,
		Provider:        provider,
		Model:           model,
		Status:          domain.ArtifactCurrent,
		Dependencies:    sorted,
		CreatedAt:       now,
	}
}

// evaluateArtifact recomputes an artifact's status against its current runtime
// contract. It returns only stable, sanitized stale reasons.
func evaluateArtifact(metadata domain.ArtifactMetadata, lookup SymbolLookup, currentSnapshotID domain.SnapshotID, currentProvider, currentModel, currentPromptVersion string) (string, []string) {
	if metadata.Status == domain.ArtifactGenerating || metadata.Status == domain.ArtifactFailed {
		return metadata.Status, metadata.StaleReasons
	}
	reasons := make([]string, 0)
	if metadata.PromptVersion != currentPromptVersion {
		reasons = append(reasons, "prompt version changed")
	}
	if metadata.Provider != currentProvider || metadata.Model != currentModel {
		reasons = append(reasons, "provider or model changed")
	}
	for _, dep := range metadata.Dependencies {
		switch dep.Kind {
		case dependencyKindSnapshot:
			if dep.ContentHash != string(currentSnapshotID) {
				reasons = append(reasons, "structural snapshot changed")
			}
		case "symbol":
			symbol, ok := lookup.GetSymbol(string(dep.SymbolID))
			if !ok {
				reasons = append(reasons, "dependency no longer exists: "+string(dep.SymbolID))
				continue
			}
			if symbolContentHash(symbol) != dep.ContentHash {
				reasons = append(reasons, "dependency content changed: "+string(dep.SymbolID))
			}
		}
	}
	if len(reasons) > 0 {
		return domain.ArtifactStale, reasons
	}
	return domain.ArtifactCurrent, nil
}
