package service

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func deterministicGlossaryPage(
	entry domain.WikiManifestEntry,
	manifest domain.WikiManifest,
	symbols []domain.Symbol,
	files []domain.File,
	hash string,
	provider string,
) domain.WikiPage {
	exported := make([]domain.Symbol, 0)
	for _, symbol := range symbols {
		if wikiGlossarySymbol(symbol) {
			exported = append(exported, symbol)
		}
	}
	sort.Slice(exported, func(i, j int) bool {
		if exported[i].Name == exported[j].Name {
			return exported[i].Path < exported[j].Path
		}
		return exported[i].Name < exported[j].Name
	})
	const maxGlossaryEntries = 200
	omitted := 0
	if len(exported) > maxGlossaryEntries {
		omitted = len(exported) - maxGlossaryEntries
		exported = exported[:maxGlossaryEntries]
	}

	var markdown strings.Builder
	aiout.WriteRelevantFiles(&markdown, wikiFileReferences(files))
	markdown.WriteString("## Exported symbols\n\n")
	markdown.WriteString("| Term | Kind | Definition |\n| --- | --- | --- |\n")
	for _, symbol := range exported {
		description := strings.TrimSpace(symbol.DocComment)
		if description == "" {
			description = strings.TrimSpace(symbol.Signature)
		}
		if description == "" {
			description = strings.TrimSpace(symbol.Summary)
		}
		markdown.WriteString("| ")
		markdown.WriteString(aiout.SafeTableCell(symbol.Name))
		markdown.WriteString(" | ")
		markdown.WriteString(aiout.SafeTableCell(symbol.Kind))
		markdown.WriteString(" | ")
		markdown.WriteString(aiout.SafeTableCell(description))
		markdown.WriteString(" |\n")
	}
	if omitted > 0 {
		markdown.WriteString(fmt.Sprintf("\n_%d additional exported symbols were omitted by the glossary size budget._\n", omitted))
	}
	if len(exported) > 0 {
		markdown.WriteString("\n**Sources:** ")
		for i, symbol := range exported {
			if i > 0 {
				markdown.WriteString(", ")
			}
			markdown.WriteString(aiout.RenderReference(aiout.SourceReference{
				Label: symbol.Name, Path: symbol.Path,
				StartLine: symbol.Range.Start.Line, EndLine: symbol.Range.End.Line,
			}))
		}
		markdown.WriteString("\n")
	} else if len(files) > 0 {
		markdown.WriteString("\n_No exported symbols were found in this page scope._\n\n**Sources:** ")
		paths := make([]string, 0, len(files))
		for _, file := range files {
			paths = append(paths, file.Path)
		}
		for index, filePath := range sortedUniqueStrings(paths) {
			if index > 0 {
				markdown.WriteString(", ")
			}
			markdown.WriteString(aiout.RenderReference(aiout.SourceReference{Label: filePath, Path: filePath}))
		}
		markdown.WriteString("\n")
	}
	links := wikiLinksForManifest(manifest, entry.Slug, nil)
	return domain.WikiPage{
		Slug: entry.Slug, Title: entry.Title, Kind: entry.Kind, Archetype: entry.Archetype,
		ModuleSlug: entry.ModuleSlug, ParentSlug: entry.ParentSlug, ScopePaths: entry.ScopePaths,
		RelatedPages: links, Markdown: strings.TrimSpace(markdown.String()), SourceHash: hash,
		ContextPackHash: "deterministic:" + hash, PolicyVersion: contextpack.DeepWikiPolicyVersion,
		OutputSchemaVersion: aiout.WikiPageSchemaVersion, Provider: provider, UpdatedAt: time.Now().UTC(),
	}
}

func wikiGlossarySymbol(symbol domain.Symbol) bool {
	if symbol.Name == "" || symbol.Kind == domain.KindFile || symbol.Kind == domain.KindImport {
		return false
	}
	if symbol.Language == "go" {
		r, _ := utf8.DecodeRuneInString(symbol.Name)
		return unicode.IsUpper(r)
	}
	return symbol.Kind == "class" || symbol.Kind == "interface" || symbol.Kind == "type" || symbol.Kind == "function"
}

func gettingStartedPrelude(pack contextpack.ContextPack) string {
	var moduleEvidence *contextpack.Evidence
	var entryEvidence *contextpack.Evidence
	for index := range pack.Evidence {
		evidence := &pack.Evidence[index]
		if moduleEvidence == nil && evidence.Kind == contextpack.KindConfig && path.Base(evidence.Path) == "go.mod" {
			moduleEvidence = evidence
		}
		if entryEvidence == nil && strings.HasPrefix(evidence.Path, "cmd/") && path.Base(evidence.Path) == "main.go" {
			entryEvidence = evidence
		}
	}
	if moduleEvidence == nil && entryEvidence == nil {
		return ""
	}
	var markdown strings.Builder
	markdown.WriteString("## Build and run\n\n")
	markdown.WriteString("| Task | Command |\n| --- | --- |\n")
	if moduleEvidence != nil {
		markdown.WriteString("| Validate the repository | `go test ./...` |\n")
	}
	if entryEvidence != nil {
		directory := path.Dir(entryEvidence.Path)
		markdown.WriteString("| Run the entrypoint | `go run ./")
		markdown.WriteString(aiout.SafeTableCell(directory))
		markdown.WriteString("` |\n")
	}
	markdown.WriteString("\n**Sources:** ")
	var sources []string
	for _, evidence := range []*contextpack.Evidence{moduleEvidence, entryEvidence} {
		if evidence == nil {
			continue
		}
		sources = append(sources, aiout.RenderReference(aiout.SourceReference{
			Label: evidence.Title, Path: evidence.Path,
			StartLine: evidence.Range.Start.Line, EndLine: evidence.Range.End.Line,
		}))
	}
	markdown.WriteString(strings.Join(sources, ", "))
	markdown.WriteString("\n")
	return markdown.String()
}

func ensureWikiSectionEvidence(content *aiout.WikiPageContent, pack contextpack.ContextPack) {
	if len(pack.Evidence) == 0 {
		return
	}
	if len(content.Sections) == 0 {
		content.Sections = []aiout.WikiSection{{Heading: "Overview"}}
	}
	for index := range content.Sections {
		section := &content.Sections[index]
		if len(section.Claims) > 0 || len(section.CodeEvidenceIDs) > 0 || len(section.Tables) > 0 {
			continue
		}
		evidence := pack.Evidence[index%len(pack.Evidence)]
		section.Claims = []aiout.Claim{{
			Text:        "This section is grounded in indexed evidence for " + evidence.Title + ".",
			EvidenceIDs: []string{string(evidence.ID)},
		}}
	}
}

// sanitizeWikiPageReferences removes model-authored references that the backend
// cannot resolve. Unsupported factual items are dropped instead of being
// published with invented citations or failing an otherwise usable page.
func sanitizeWikiPageReferences(content *aiout.WikiPageContent, pack contextpack.ContextPack, allowedRelated map[string]struct{}) {
	allowedEvidence := packAllowSet(pack)
	renderableEvidence := make(map[string]struct{}, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		if evidence.Path != "" && evidence.DisplayCode != "" {
			renderableEvidence[string(evidence.ID)] = struct{}{}
		}
	}
	for sectionIndex := range content.Sections {
		section := &content.Sections[sectionIndex]
		section.Claims = groundedWikiClaims(section.Claims, allowedEvidence)
		section.CodeEvidenceIDs = allowedWikiIDs(section.CodeEvidenceIDs, renderableEvidence)
		tables := section.Tables[:0]
		for tableIndex := range section.Tables {
			table := section.Tables[tableIndex]
			table.EvidenceIDs = allowedWikiIDs(table.EvidenceIDs, allowedEvidence)
			if len(table.EvidenceIDs) > 0 {
				tables = append(tables, table)
			}
		}
		section.Tables = tables
	}
	content.RelatedPages = allowedWikiIDs(content.RelatedPages, allowedRelated)
	inferences := content.Inferences[:0]
	for inferenceIndex := range content.Inferences {
		inference := content.Inferences[inferenceIndex]
		inference.EvidenceIDs = allowedWikiIDs(inference.EvidenceIDs, allowedEvidence)
		if len(inference.EvidenceIDs) > 0 {
			inferences = append(inferences, inference)
		}
	}
	content.Inferences = inferences
	for limitationIndex := range content.Limitations {
		content.Limitations[limitationIndex].EvidenceIDs = allowedWikiIDs(content.Limitations[limitationIndex].EvidenceIDs, allowedEvidence)
	}
}

func groundedWikiClaims(claims []aiout.Claim, allowed map[string]struct{}) []aiout.Claim {
	grounded := claims[:0]
	for claimIndex := range claims {
		claim := claims[claimIndex]
		claim.EvidenceIDs = allowedWikiIDs(claim.EvidenceIDs, allowed)
		if len(claim.EvidenceIDs) > 0 {
			grounded = append(grounded, claim)
		}
	}
	return grounded
}

func allowedWikiIDs(ids []string, allowed map[string]struct{}) []string {
	result := ids[:0]
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func aioutWikiLinks(links []domain.WikiPageLink) []aiout.WikiLink {
	result := make([]aiout.WikiLink, 0, len(links))
	for _, link := range links {
		result = append(result, aiout.WikiLink{Slug: link.Slug, Title: link.Title, Relation: link.Relation})
	}
	return result
}

func wikiFileReferences(files []domain.File) []aiout.SourceReference {
	refs := make([]aiout.SourceReference, 0, len(files))
	for _, file := range files {
		if file.Path == "" {
			continue
		}
		refs = append(refs, aiout.SourceReference{Label: file.Path, Path: file.Path})
	}
	return refs
}
