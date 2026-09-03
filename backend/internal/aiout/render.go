package aiout

import (
	"html"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// SourceReference is a backend-resolved source location. The model only emits
// EvidenceIDs; paths and ranges enter rendered output through this type.
type SourceReference struct {
	Label     string
	Path      string
	StartLine int
	EndLine   int
}

// Resolver maps an EvidenceID to a backend-owned source location. A reference
// without a path represents an external or otherwise non-navigable boundary.
type Resolver func(evidenceID string) (SourceReference, bool)

// CodeBlock is renderer-only source selected by an allowlisted EvidenceID. Its
// content, language and location all come from backend evidence, never the model.
type CodeBlock struct {
	Reference SourceReference
	Language  string
	Content   string
	Truncated bool
}

type CodeResolver func(evidenceID string) (CodeBlock, bool)

// MermaidBlock is trusted renderer-only diagram data derived from the factual
// graph. Source is never model-authored; Sources are navigable backend refs.
type MermaidBlock struct {
	Title   string
	Source  string
	Sources []SourceReference
}

// WikiLink is backend-owned navigation resolved from the validated manifest.
type WikiLink struct {
	Slug     string
	Title    string
	Relation string
}

// RenderOptions controls presentation derived from trusted backend context. It
// never expands what the model is allowed to claim or cite.
type RenderOptions struct {
	RelevantFiles       []SourceReference
	CodeResolver        CodeResolver
	Mermaid             *MermaidBlock
	ExplanationSections bool
	DefinitionCodeIDs   []string
	UsageCodeIDs        []string
	SeeAlso             []SourceReference
	MinInference        float64
	PreludeMarkdown     string
	WikiLinks           []WikiLink
}

// SafeText neutralizes control characters and HTML. Contextual helpers such as
// RenderReference and SafeTableCell additionally escape Markdown delimiters.
func SafeText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	return html.EscapeString(strings.TrimSpace(value))
}

func renderOptions(options []RenderOptions) RenderOptions {
	if len(options) == 0 {
		return RenderOptions{}
	}
	return options[0]
}

// citations renders the resolved references for a claim. Unknown IDs are
// dropped (validation already rejects them, so this is defense in depth).
func citations(resolve Resolver, ids []string) string {
	refs := resolveReferences(resolve, ids)
	if len(refs) == 0 {
		return ""
	}
	rendered := make([]string, 0, len(refs))
	for _, ref := range refs {
		rendered = append(rendered, RenderReference(ref))
	}
	return " (" + strings.Join(rendered, ", ") + ")"
}

func resolveReferences(resolve Resolver, ids []string) []SourceReference {
	if resolve == nil {
		return nil
	}
	refs := make([]SourceReference, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		ref, ok := resolve(id)
		if !ok {
			continue
		}
		key := referenceKey(ref)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

func referenceKey(ref SourceReference) string {
	return strings.Join([]string{ref.Path, ref.Label, lineFragment(ref)}, "\x00")
}

// RenderReference renders one backend-owned source location. Its label is
// escaped for Markdown link context and its target is derived only from Path.
func RenderReference(ref SourceReference) string {
	label := safeMarkdownLabel(ref.Label)
	if label == "" {
		label = safeMarkdownLabel(locationLabel(ref.Path, ref.StartLine, ref.EndLine))
	}
	if ref.Path == "" {
		label = strings.ReplaceAll(label, "`", "'")
		return "`" + label + "`"
	}
	return "[" + label + "](" + sourceTarget(ref) + ")"
}

func sourceTarget(ref SourceReference) string {
	parts := strings.Split(ref.Path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/") + lineFragment(ref)
}

func lineFragment(ref SourceReference) string {
	if ref.StartLine <= 0 {
		return ""
	}
	fragment := "#L" + itoa(ref.StartLine)
	if ref.EndLine > ref.StartLine {
		fragment += "-L" + itoa(ref.EndLine)
	}
	return fragment
}

func locationLabel(path string, startLine, endLine int) string {
	if path == "" || startLine <= 0 {
		return path
	}
	label := path + ":" + itoa(startLine)
	if endLine > startLine {
		label += "-" + itoa(endLine)
	}
	return label
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

// RenderExplanation renders a Hover/See More Explanation into safe Markdown.
// See More passes RelevantFiles; Hover leaves the option empty.
func RenderExplanation(exp Explanation, resolve Resolver, options ...RenderOptions) string {
	var b strings.Builder
	config := renderOptions(options)
	WriteRelevantFiles(&b, config.RelevantFiles)
	if summary := SafeText(exp.Summary); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	if config.ExplanationSections {
		writeCodeBlocks(&b, "Definition", config.DefinitionCodeIDs, config.CodeResolver)
		writeClaims(&b, "Key Components and Tests", exp.Observations, resolve)
		writeCodeBlocks(&b, "Example Usages", config.UsageCodeIDs, config.CodeResolver)
		writeExplanationNotes(&b, exp.Inferences, exp.Uncertainties, resolve, config.MinInference)
		writeClaims(&b, "Change impact", exp.ChangeImpact, resolve)
		writeSeeAlso(&b, config.SeeAlso)
		return strings.TrimSpace(b.String())
	}
	writeCodeBlocks(&b, "Selected code", exp.CodeEvidenceIDs, config.CodeResolver)
	writeClaims(&b, "Observations", exp.Observations, resolve)
	writeInferences(&b, exp.Inferences, resolve)
	writeUncertainties(&b, exp.Uncertainties, resolve)
	writeClaims(&b, "Change impact", exp.ChangeImpact, resolve)
	return strings.TrimSpace(b.String())
}

func writeExplanationNotes(b *strings.Builder, inferences []Inference, uncertainties []Uncertainty, resolve Resolver, minimum float64) {
	kept := make([]Inference, 0, len(inferences))
	for _, inference := range inferences {
		if inference.Confidence >= minimum {
			kept = append(kept, inference)
		}
	}
	if len(kept) == 0 && len(uncertainties) == 0 {
		return
	}
	b.WriteString("### Notes\n\n")
	for _, inference := range kept {
		b.WriteString("- _(inference; confidence ")
		b.WriteString(itoa(int(math.Round(inference.Confidence * 100))))
		b.WriteString("%)_ ")
		b.WriteString(SafeText(inference.Text))
		b.WriteString(citations(resolve, inference.EvidenceIDs))
		b.WriteString("\n")
	}
	for _, uncertainty := range uncertainties {
		b.WriteString("- _(uncertainty)_ ")
		b.WriteString(SafeText(uncertainty.Text))
		if reason := SafeText(uncertainty.Reason); reason != "" {
			b.WriteString(" — ")
			b.WriteString(reason)
		}
		b.WriteString(citations(resolve, uncertainty.EvidenceIDs))
		b.WriteString("\n")
	}
	ids := append(inferenceEvidenceIDs(kept), uncertaintyEvidenceIDs(uncertainties)...)
	writeSources(b, resolve, ids)
	b.WriteString("\n")
}

func writeSeeAlso(b *strings.Builder, refs []SourceReference) {
	if len(refs) == 0 {
		return
	}
	b.WriteString("### See Also\n\n")
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		key := referenceKey(ref)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		b.WriteString("- ")
		b.WriteString(RenderReference(ref))
		b.WriteString("\n")
	}
}

// RenderWiki renders a wiki page into safe Markdown. Its relevant-files block
// is derived from the scoped ContextPack, never from model prose.
func RenderWiki(page WikiPageContent, resolve Resolver, options ...RenderOptions) string {
	var b strings.Builder
	config := renderOptions(options)
	WriteRelevantFiles(&b, config.RelevantFiles)
	if config.PreludeMarkdown != "" {
		b.WriteString(strings.TrimSpace(config.PreludeMarkdown))
		b.WriteString("\n\n")
	}
	writeMermaidBlock(&b, config.Mermaid)
	for _, section := range page.Sections {
		if heading := SafeText(section.Heading); heading != "" {
			b.WriteString("## ")
			b.WriteString(heading)
			b.WriteString("\n\n")
		}
		for _, claim := range section.Claims {
			// Claims render as documentation prose, not as a bullet inventory:
			// a claim is a paragraph unless the model authored it as a bullet
			// list ("- " lines), which is kept verbatim. Citations follow the
			// block either way.
			text := SafeText(claim.Text)
			if strings.HasPrefix(strings.TrimSpace(text), "- ") {
				// The section-level **Sources:** block already aggregates this
				// claim's evidence; inline citations would break the list.
				b.WriteString(strings.TrimSpace(text))
				b.WriteString("\n\n")
			} else {
				b.WriteString(text)
				b.WriteString(citations(resolve, claim.EvidenceIDs))
				b.WriteString("\n\n")
			}
		}
		writeCodeBlocks(&b, "", section.CodeEvidenceIDs, config.CodeResolver)
		writeWikiTables(&b, section.Tables)
		sourceIDs := append(claimEvidenceIDs(section.Claims), section.CodeEvidenceIDs...)
		for _, table := range section.Tables {
			sourceIDs = append(sourceIDs, table.EvidenceIDs...)
		}
		writeSources(&b, resolve, sourceIDs)
		b.WriteString("\n")
	}
	writeInferences(&b, page.Inferences, resolve)
	writeUncertainties(&b, page.Limitations, resolve)
	writeWikiLinks(&b, config.WikiLinks)
	return strings.TrimSpace(b.String())
}

func writeWikiTables(b *strings.Builder, tables []WikiTable) {
	for _, table := range tables {
		if table.Kind != "table" || len(table.Columns) == 0 {
			continue
		}
		b.WriteString("\n")
		b.WriteString("| ")
		for i, column := range table.Columns {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(SafeTableCell(column))
		}
		b.WriteString(" |\n| ")
		for i := range table.Columns {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString("---")
		}
		b.WriteString(" |\n")
		for _, row := range table.Rows {
			if len(row) != len(table.Columns) {
				continue
			}
			b.WriteString("| ")
			for i, cell := range row {
				if i > 0 {
					b.WriteString(" | ")
				}
				b.WriteString(SafeTableCell(cell))
			}
			b.WriteString(" |\n")
		}
	}
}

// SafeTableCell renders untrusted text as data inside a Markdown table cell.
func SafeTableCell(value string) string {
	return strings.ReplaceAll(safeMarkdownLabel(value), "|", "/")
}

func safeMarkdownLabel(value string) string {
	value = SafeText(value)
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`[`, `\[`,
		`]`, `\]`,
		`(`, `\(`,
		`)`, `\)`,
	)
	return replacer.Replace(value)
}

func writeWikiLinks(b *strings.Builder, links []WikiLink) {
	if len(links) == 0 {
		return
	}
	b.WriteString("\n## Related pages\n\n")
	for _, link := range links {
		if link.Slug == "" || link.Title == "" {
			continue
		}
		label := safeMarkdownLabel(link.Title)
		if relation := safeMarkdownLabel(link.Relation); relation != "" {
			label += " (" + relation + ")"
		}
		b.WriteString("- [")
		b.WriteString(label)
		b.WriteString("](wiki:")
		b.WriteString(link.Slug)
		b.WriteString(")\n")
	}
}

// RenderCodemap renders the Codemap narrative prose (the factual graph stays a
// separate structured field on the API response).
func RenderCodemap(narr CodemapNarrative, resolve Resolver) string {
	var b strings.Builder
	if overview := SafeText(narr.Overview); overview != "" {
		b.WriteString(overview)
		b.WriteString("\n\n")
	}
	if motivation := SafeText(narr.Motivation); motivation != "" {
		b.WriteString("**Reason:** ")
		b.WriteString(motivation)
		b.WriteString("\n\n")
	}
	if details := SafeText(narr.Details); details != "" {
		b.WriteString(details)
		b.WriteString("\n\n")
	}
	writeClaims(&b, "Observations", narr.Claims, resolve)
	writeInferences(&b, narr.Inferences, resolve)
	writeUncertainties(&b, narr.Uncertainties, resolve)
	return strings.TrimSpace(b.String())
}

func writeClaims(b *strings.Builder, heading string, claims []Claim, resolve Resolver) {
	if len(claims) == 0 {
		return
	}
	b.WriteString("### ")
	b.WriteString(heading)
	b.WriteString("\n\n")
	for _, claim := range claims {
		b.WriteString("- ")
		b.WriteString(SafeText(claim.Text))
		b.WriteString(citations(resolve, claim.EvidenceIDs))
		b.WriteString("\n")
	}
	writeSources(b, resolve, claimEvidenceIDs(claims))
	b.WriteString("\n")
}

func writeInferences(b *strings.Builder, inferences []Inference, resolve Resolver) {
	if len(inferences) == 0 {
		return
	}
	b.WriteString("### Inferences\n\n")
	for _, inference := range inferences {
		b.WriteString("- _(inference)_ ")
		b.WriteString("**Confidence: ")
		b.WriteString(itoa(int(math.Round(inference.Confidence * 100))))
		b.WriteString("%.** ")
		b.WriteString(SafeText(inference.Text))
		b.WriteString(citations(resolve, inference.EvidenceIDs))
		b.WriteString("\n")
	}
	writeSources(b, resolve, inferenceEvidenceIDs(inferences))
	b.WriteString("\n")
}

func writeUncertainties(b *strings.Builder, uncertainties []Uncertainty, resolve Resolver) {
	if len(uncertainties) == 0 {
		return
	}
	b.WriteString("### Uncertainties\n\n")
	for _, uncertainty := range uncertainties {
		b.WriteString("- _(uncertainty)_ ")
		b.WriteString(SafeText(uncertainty.Text))
		if reason := SafeText(uncertainty.Reason); reason != "" {
			b.WriteString(" — ")
			b.WriteString(reason)
		}
		b.WriteString(citations(resolve, uncertainty.EvidenceIDs))
		b.WriteString("\n")
	}
	writeSources(b, resolve, uncertaintyEvidenceIDs(uncertainties))
	b.WriteString("\n")
}

func writeSources(b *strings.Builder, resolve Resolver, ids []string) {
	refs := resolveReferences(resolve, ids)
	if len(refs) == 0 {
		return
	}
	b.WriteString("\n**Sources:** ")
	for i, ref := range refs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(RenderReference(ref))
	}
	b.WriteString("\n")
}

// WriteRelevantFiles emits the canonical, deduplicated source-file disclosure
// used by every safe Markdown renderer.
func WriteRelevantFiles(b *strings.Builder, refs []SourceReference) {
	files := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Path != "" {
			files[ref.Path] = struct{}{}
		}
	}
	if len(files) == 0 {
		return
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	b.WriteString("<details>\n<summary>Relevant source files</summary>\n\n")
	b.WriteString("The following files contributed evidence to this response:\n\n")
	for _, path := range paths {
		b.WriteString("- ")
		b.WriteString(RenderReference(SourceReference{Label: path, Path: path}))
		b.WriteString("\n")
	}
	b.WriteString("\n</details>\n\n")
}

func writeCodeBlocks(b *strings.Builder, heading string, ids []string, resolve CodeResolver) {
	if len(ids) == 0 || resolve == nil {
		return
	}
	wroteHeading := false
	for _, id := range ids {
		block, ok := resolve(id)
		if !ok || block.Content == "" {
			continue
		}
		if heading != "" && !wroteHeading {
			b.WriteString("### ")
			b.WriteString(heading)
			b.WriteString("\n\n")
			wroteHeading = true
		}
		b.WriteString("**")
		b.WriteString(SafeText(block.Reference.Label))
		b.WriteString("**")
		if block.Truncated {
			b.WriteString(" _(truncated to complete lines)_")
		}
		b.WriteString("\n\n")
		fence := codeFence(block.Content)
		b.WriteString(fence)
		b.WriteString(safeCodeLanguage(block.Language))
		b.WriteString("\n")
		b.WriteString(block.Content)
		if !strings.HasSuffix(block.Content, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(fence)
		b.WriteString("\n\n")
	}
}

func writeMermaidBlock(b *strings.Builder, block *MermaidBlock) {
	if block == nil || strings.TrimSpace(block.Source) == "" {
		return
	}
	title := SafeText(block.Title)
	if title == "" {
		title = "Architecture diagram"
	}
	b.WriteString("### ")
	b.WriteString(title)
	b.WriteString("\n\n")
	fence := codeFence(block.Source)
	b.WriteString(fence)
	b.WriteString("mermaid\n")
	b.WriteString(block.Source)
	if !strings.HasSuffix(block.Source, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(fence)
	b.WriteString("\n")
	refs := sortedSourceReferences(block.Sources)
	if len(refs) > 0 {
		b.WriteString("\n**Diagram sources:** ")
		for i, ref := range refs {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(RenderReference(ref))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func sortedSourceReferences(refs []SourceReference) []SourceReference {
	result := make([]SourceReference, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Path == "" {
			continue
		}
		key := referenceKey(ref)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, ref)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		if result[i].StartLine != result[j].StartLine {
			return result[i].StartLine < result[j].StartLine
		}
		return result[i].Label < result[j].Label
	})
	return result
}

func codeFence(content string) string {
	longest := 0
	for _, run := range strings.FieldsFunc(content, func(r rune) bool { return r != '`' }) {
		if len(run) > longest {
			longest = len(run)
		}
	}
	if longest < 3 {
		longest = 3
	} else {
		longest++
	}
	return strings.Repeat("`", longest)
}

func safeCodeLanguage(language string) string {
	for _, r := range language {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_+-#", r) {
			continue
		}
		return ""
	}
	return language
}

func claimEvidenceIDs(claims []Claim) []string {
	var ids []string
	for _, claim := range claims {
		ids = append(ids, claim.EvidenceIDs...)
	}
	return ids
}

func inferenceEvidenceIDs(inferences []Inference) []string {
	var ids []string
	for _, inference := range inferences {
		ids = append(ids, inference.EvidenceIDs...)
	}
	return ids
}

func uncertaintyEvidenceIDs(uncertainties []Uncertainty) []string {
	var ids []string
	for _, uncertainty := range uncertainties {
		ids = append(ids, uncertainty.EvidenceIDs...)
	}
	return ids
}
