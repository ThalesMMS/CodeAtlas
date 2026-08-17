package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// ValidateSnapshot reports whether a persisted snapshot at path is loadable
// without mutating any in-memory state. A missing file is treated as a valid
// empty initial state and returns nil. A present file must decode as a snapshot
// of a supported version; otherwise a descriptive error is returned.
//
// It deliberately does not build the lexical index or resolve edges: it only
// proves the snapshot is structurally readable, which is what a readiness probe
// needs.
func ValidateSnapshot(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var snapshot diskSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("decode index snapshot: %w", err)
	}
	if snapshot.Version != snapshotVersion {
		return fmt.Errorf("unsupported index snapshot version %d", snapshot.Version)
	}
	return nil
}

// Severity classifies a Violation. An Error that is not Repairable blocks a load
// or a commit; a Warning is recorded and logged but never blocks.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Entity identifies which correlated structure a Violation belongs to.
const (
	EntityFile      = "file"
	EntitySymbol    = "symbol"
	EntityEdge      = "edge"
	EntityEmbedding = "embedding"
	EntityWiki      = "wiki"
	EntityLexical   = "lexical"
	EntityState     = "state"
)

// Stable violation codes. Authoritative (blocking) codes describe referential
// corruption that yields silently wrong results; the rest are quality warnings
// or repairable derived-index drift.
const (
	// Authoritative — block load/commit.
	ViolationFileKeyMismatch       = "FILE_KEY_MISMATCH"
	ViolationSymbolKeyMismatch     = "SYMBOL_KEY_MISMATCH"
	ViolationSymbolIDEmpty         = "SYMBOL_ID_EMPTY"
	ViolationSymbolOrphan          = "SYMBOL_ORPHAN_FILE"
	ViolationWikiKeyMismatch       = "WIKI_KEY_MISMATCH"
	ViolationEmbeddingOrphan       = "EMBEDDING_ORPHAN_SYMBOL"
	ViolationEmbeddingNonFinite    = "EMBEDDING_NON_FINITE"
	ViolationEmbeddingDimMixed     = "EMBEDDING_DIMENSION_MIXED"
	ViolationEmbeddingDimMetadata  = "EMBEDDING_DIMENSION_METADATA"
	ViolationEdgeConfidenceInvalid = "EDGE_CONFIDENCE_INVALID"

	// Quality warnings — recorded, never blocking.
	ViolationFilePathFormat    = "FILE_PATH_FORMAT"
	ViolationFilePathForbidden = "FILE_PATH_FORBIDDEN"
	ViolationFileHashFormat    = "FILE_HASH_FORMAT"
	ViolationFileSizeNegative  = "FILE_SIZE_NEGATIVE"
	ViolationFileLanguage      = "FILE_LANGUAGE_UNSUPPORTED"
	ViolationFileTimestamp     = "FILE_TIMESTAMP_ZERO"
	ViolationSymbolLanguage    = "SYMBOL_LANGUAGE_MISMATCH"
	ViolationSymbolRange       = "SYMBOL_RANGE_INVALID"
	ViolationSymbolNameKind    = "SYMBOL_NAME_KIND_EMPTY"
	ViolationSymbolOversized   = "SYMBOL_OVERSIZED"
	ViolationEdgeFromMissing   = "EDGE_FROM_MISSING"
	ViolationEdgeToMissing     = "EDGE_TO_MISSING"
	ViolationEdgeUnresolved    = "EDGE_UNRESOLVED_NO_NAME"
	ViolationEdgePathMissing   = "EDGE_PATH_MISSING"
	ViolationEdgeType          = "EDGE_TYPE_UNSUPPORTED"
	ViolationWikiSlugFormat    = "WIKI_SLUG_FORMAT"
	ViolationWikiTitleEmpty    = "WIKI_TITLE_EMPTY"
	ViolationWikiProvider      = "WIKI_PROVIDER_EMPTY"
	ViolationWikiTimestamp     = "WIKI_TIMESTAMP_ZERO"

	// Repairable — derived indexes can be rebuilt from authoritative sources.
	ViolationLexicalOrphan   = "LEXICAL_ORPHAN_SYMBOL"
	ViolationLexicalCoverage = "LEXICAL_COVERAGE"
	ViolationLexicalDocCount = "LEXICAL_DOC_COUNT"

	// Informational.
	ViolationTruncated = "VALIDATION_TRUNCATED"
)

// Violation is a single, structured invariant breach. It carries no file content
// or secret; Path is the entity's already-relative workspace path.
type Violation struct {
	Code       string
	Severity   string
	Entity     string
	Path       string
	Message    string
	Repairable bool
}

// maxViolations caps the returned slice so a pathologically corrupt snapshot
// cannot exhaust memory; the total count is preserved via a truncation marker.
const maxViolations = 1000

// Symbol content sanity ceilings. These are absurdity guards (corruption, not
// large-but-legitimate code); breaching them is a warning, never blocking.
const (
	maxSymbolCodeBytes       = 1_000_000
	maxSymbolDocCommentBytes = 4_000
	maxSymbolSignatureBytes  = 4_000
	maxSymbolSummaryBytes    = 100_000
)

var supportedLanguages = map[string]struct{}{
	"go": {}, "javascript": {}, "javascriptreact": {}, "typescript": {}, "typescriptreact": {}, "swift": {}, "python": {}, "rust": {},
	"dockerfile": {}, "dotenv": {}, "gomod": {}, "json": {}, "makefile": {}, "markdown": {}, "text": {},
}

var supportedEdgeTypes = map[string]struct{}{
	"contains": {}, "calls": {}, "imports": {}, "inherits": {}, "implements": {},
}

// forbiddenPathSegments mirrors the indexer's ignore set; the store cannot import
// the indexer (it would cycle), so the list is duplicated here intentionally.
var forbiddenPathSegments = map[string]struct{}{
	".git": {}, ".codeatlas": {}, "node_modules": {}, "vendor": {}, "dist": {}, "build": {},
	".venv": {}, "venv": {}, ".tox": {}, "__pycache__": {}, ".mypy_cache": {}, ".pytest_cache": {}, ".ruff_cache": {},
	"coverage": {}, ".next": {}, ".idea": {}, ".vscode": {}, "target": {}, "bin": {}, "obj": {},
}

type violationCollector struct {
	violations []Violation
	total      int
}

func (c *violationCollector) add(v Violation) {
	c.total++
	if len(c.violations) < maxViolations {
		c.violations = append(c.violations, v)
	}
}

func (c *violationCollector) result() []Violation {
	if c.total <= len(c.violations) {
		return c.violations
	}
	return append(c.violations, Violation{
		Code:     ViolationTruncated,
		Severity: SeverityWarning,
		Entity:   EntityState,
		Message:  fmt.Sprintf("violation list truncated: %d of %d shown", len(c.violations), c.total),
	})
}

// ValidateState is a pure auditor of a finalized state: it never mutates or
// repairs, it only reports. It checks every correlated structure — files,
// symbols, edges, embeddings, wiki and the derived lexical index — against the
// documented invariants and returns stable, capped violations. Callers decide
// policy: HasBlockingViolations gates a load or a commit; warnings and repairable
// derived drift are logged and (for derived state) rebuilt by finalize.
func ValidateState(st *state) []Violation {
	c := &violationCollector{}
	validateFiles(st, c)
	validateSymbols(st, c)
	validateEdges(st, c)
	validateEmbeddings(st, c)
	validateWiki(st, c)
	validateDerived(st, c)
	return c.result()
}

func validateFiles(st *state, c *violationCollector) {
	for key, file := range st.files {
		if key != file.Path {
			c.add(Violation{Code: ViolationFileKeyMismatch, Severity: SeverityError, Entity: EntityFile, Path: key,
				Message: fmt.Sprintf("file map key %q != File.Path %q", key, file.Path)})
		}
		if filepath.IsAbs(file.Path) || pathEscapes(file.Path) {
			c.add(Violation{Code: ViolationFilePathFormat, Severity: SeverityWarning, Entity: EntityFile, Path: file.Path,
				Message: "file path is not relative/canonical"})
		}
		if isForbiddenPath(file.Path) {
			c.add(Violation{Code: ViolationFilePathForbidden, Severity: SeverityWarning, Entity: EntityFile, Path: file.Path,
				Message: "file indexed under an ignored directory"})
		}
		if file.Hash != "" && !isSupportedHash(file.Hash) {
			c.add(Violation{Code: ViolationFileHashFormat, Severity: SeverityWarning, Entity: EntityFile, Path: file.Path,
				Message: "file hash is not a supported sha256 hex digest"})
		}
		if file.Size < 0 {
			c.add(Violation{Code: ViolationFileSizeNegative, Severity: SeverityWarning, Entity: EntityFile, Path: file.Path,
				Message: fmt.Sprintf("file size %d is negative", file.Size)})
		}
		if file.Language != "" && !isSupportedLanguage(file.Language) {
			c.add(Violation{Code: ViolationFileLanguage, Severity: SeverityWarning, Entity: EntityFile, Path: file.Path,
				Message: fmt.Sprintf("unsupported language %q", file.Language)})
		}
		if file.ModifiedAt.IsZero() || file.IndexedAt.IsZero() {
			c.add(Violation{Code: ViolationFileTimestamp, Severity: SeverityWarning, Entity: EntityFile, Path: file.Path,
				Message: "file timestamps are zero"})
		}
	}
}

func validateSymbols(st *state, c *violationCollector) {
	for key, symbol := range st.symbols {
		if symbol.ID == "" {
			c.add(Violation{Code: ViolationSymbolIDEmpty, Severity: SeverityError, Entity: EntitySymbol, Path: symbol.Path,
				Message: "symbol has an empty ID"})
		}
		if key != symbol.ID {
			c.add(Violation{Code: ViolationSymbolKeyMismatch, Severity: SeverityError, Entity: EntitySymbol, Path: symbol.Path,
				Message: fmt.Sprintf("symbol map key %q != Symbol.ID %q", key, symbol.ID)})
		}
		file, ok := st.files[symbol.Path]
		if !ok {
			c.add(Violation{Code: ViolationSymbolOrphan, Severity: SeverityError, Entity: EntitySymbol, Path: symbol.Path,
				Message: fmt.Sprintf("symbol %q references a file not in the index", symbol.ID)})
		} else if symbol.Language != "" && file.Language != "" && symbol.Language != file.Language {
			c.add(Violation{Code: ViolationSymbolLanguage, Severity: SeverityWarning, Entity: EntitySymbol, Path: symbol.Path,
				Message: fmt.Sprintf("symbol language %q != file language %q", symbol.Language, file.Language)})
		}
		if !isValidRange(symbol.Range) {
			c.add(Violation{Code: ViolationSymbolRange, Severity: SeverityWarning, Entity: EntitySymbol, Path: symbol.Path,
				Message: "symbol range has non-positive coordinates or start > end"})
		}
		if symbol.Name == "" || symbol.Kind == "" {
			c.add(Violation{Code: ViolationSymbolNameKind, Severity: SeverityWarning, Entity: EntitySymbol, Path: symbol.Path,
				Message: "symbol name or kind is empty"})
		}
		if len(symbol.Code) > maxSymbolCodeBytes || len(symbol.DocComment) > maxSymbolDocCommentBytes ||
			len(symbol.Signature) > maxSymbolSignatureBytes || len(symbol.Summary) > maxSymbolSummaryBytes {
			c.add(Violation{Code: ViolationSymbolOversized, Severity: SeverityWarning, Entity: EntitySymbol, Path: symbol.Path,
				Message: "symbol code/doc-comment/signature/summary exceeds the sanity ceiling"})
		}
	}
}

func validateEdges(st *state, c *violationCollector) {
	for _, edge := range st.edges {
		path := edge.Path
		if _, ok := st.symbols[edge.FromSymbolID]; !ok {
			c.add(Violation{Code: ViolationEdgeFromMissing, Severity: SeverityWarning, Entity: EntityEdge, Path: path,
				Message: fmt.Sprintf("edge from %q references a missing symbol", edge.FromSymbolID)})
		}
		if edge.ToSymbolID != "" {
			if _, ok := st.symbols[edge.ToSymbolID]; !ok {
				c.add(Violation{Code: ViolationEdgeToMissing, Severity: SeverityWarning, Entity: EntityEdge, Path: path,
					Message: fmt.Sprintf("edge to %q references a missing symbol", edge.ToSymbolID)})
			}
		} else if edge.ToName == "" {
			c.add(Violation{Code: ViolationEdgeUnresolved, Severity: SeverityWarning, Entity: EntityEdge, Path: path,
				Message: "unresolved edge has no ToName"})
		}
		if edge.Path != "" {
			if _, ok := st.files[edge.Path]; !ok {
				c.add(Violation{Code: ViolationEdgePathMissing, Severity: SeverityWarning, Entity: EntityEdge, Path: path,
					Message: "edge path references a file not in the index"})
			}
		}
		if edge.Type != "" && !isSupportedEdgeType(edge.Type) {
			c.add(Violation{Code: ViolationEdgeType, Severity: SeverityWarning, Entity: EntityEdge, Path: path,
				Message: fmt.Sprintf("unsupported edge type %q", edge.Type)})
		}
		if math.IsNaN(edge.Confidence) || math.IsInf(edge.Confidence, 0) || edge.Confidence < 0 || edge.Confidence > 1 {
			c.add(Violation{Code: ViolationEdgeConfidenceInvalid, Severity: SeverityError, Entity: EntityEdge, Path: path,
				Message: fmt.Sprintf("edge confidence %v is not within [0,1]", edge.Confidence)})
		}
	}
}

func validateEmbeddings(st *state, c *violationCollector) {
	dimension := 0
	// Iterate deterministically so a "mixed dimension" report is stable.
	ids := make([]string, 0, len(st.embeddings))
	for id := range st.embeddings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		vector := st.embeddings[id]
		if _, ok := st.symbols[id]; !ok {
			c.add(Violation{Code: ViolationEmbeddingOrphan, Severity: SeverityError, Entity: EntityEmbedding, Path: id,
				Message: fmt.Sprintf("embedding %q has no matching symbol", id)})
		}
		if dimension == 0 {
			dimension = len(vector)
		} else if len(vector) != dimension {
			c.add(Violation{Code: ViolationEmbeddingDimMixed, Severity: SeverityError, Entity: EntityEmbedding, Path: id,
				Message: fmt.Sprintf("embedding dimension %d != index dimension %d", len(vector), dimension)})
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				c.add(Violation{Code: ViolationEmbeddingNonFinite, Severity: SeverityError, Entity: EntityEmbedding, Path: id,
					Message: "embedding contains a non-finite value"})
				break
			}
		}
	}
	if st.embeddingMetadata.Enabled && dimension != 0 && st.embeddingMetadata.Dimension != dimension {
		c.add(Violation{Code: ViolationEmbeddingDimMetadata, Severity: SeverityError, Entity: EntityEmbedding, Path: "",
			Message: fmt.Sprintf("embedding dimension %d != metadata dimension %d", dimension, st.embeddingMetadata.Dimension)})
	}
}

func validateWiki(st *state, c *violationCollector) {
	for slug, page := range st.wiki {
		if slug != page.Slug {
			c.add(Violation{Code: ViolationWikiKeyMismatch, Severity: SeverityError, Entity: EntityWiki, Path: slug,
				Message: fmt.Sprintf("wiki map key %q != WikiPage.Slug %q", slug, page.Slug)})
		}
		if !isNormalizedSlug(page.Slug) {
			c.add(Violation{Code: ViolationWikiSlugFormat, Severity: SeverityWarning, Entity: EntityWiki, Path: page.Slug,
				Message: "wiki slug is empty or not normalized"})
		}
		if page.Title == "" {
			c.add(Violation{Code: ViolationWikiTitleEmpty, Severity: SeverityWarning, Entity: EntityWiki, Path: page.Slug,
				Message: "wiki page has an empty title"})
		}
		if page.Provider == "" {
			c.add(Violation{Code: ViolationWikiProvider, Severity: SeverityWarning, Entity: EntityWiki, Path: page.Slug,
				Message: "wiki page has an empty provider"})
		}
		if page.UpdatedAt.IsZero() {
			c.add(Violation{Code: ViolationWikiTimestamp, Severity: SeverityWarning, Entity: EntityWiki, Path: page.Slug,
				Message: "wiki page UpdatedAt is zero"})
		}
	}
}

// validateDerived audits the lexical index against the authoritative symbols.
// These violations are Repairable: rebuildLexical reconstructs the index.
func validateDerived(st *state, c *violationCollector) {
	if st.lexical == nil {
		return
	}
	for token, posting := range st.lexical.postings {
		for id := range posting {
			if _, ok := st.symbols[id]; !ok {
				c.add(Violation{Code: ViolationLexicalOrphan, Severity: SeverityError, Entity: EntityLexical, Path: id, Repairable: true,
					Message: fmt.Sprintf("lexical posting token %q references missing symbol %q", token, id)})
			}
		}
	}
	for id := range st.lexical.docLen {
		if _, ok := st.symbols[id]; !ok {
			c.add(Violation{Code: ViolationLexicalOrphan, Severity: SeverityError, Entity: EntityLexical, Path: id, Repairable: true,
				Message: fmt.Sprintf("lexical docLen references missing symbol %q", id)})
		}
	}
	for id := range st.symbols {
		if _, ok := st.lexical.docLen[id]; !ok {
			c.add(Violation{Code: ViolationLexicalCoverage, Severity: SeverityError, Entity: EntityLexical, Path: id, Repairable: true,
				Message: fmt.Sprintf("symbol %q is missing from the lexical index", id)})
		}
	}
	if st.lexical.docCount != len(st.symbols) {
		c.add(Violation{Code: ViolationLexicalDocCount, Severity: SeverityError, Entity: EntityLexical, Path: "", Repairable: true,
			Message: fmt.Sprintf("lexical docCount %d != symbol count %d", st.lexical.docCount, len(st.symbols))})
	}
}

// HasBlockingViolations reports whether any violation is an authoritative
// (Error, non-repairable) breach that must prevent a load or a commit.
func HasBlockingViolations(violations []Violation) bool {
	for _, v := range violations {
		if v.Severity == SeverityError && !v.Repairable {
			return true
		}
	}
	return false
}

// ViolationSummary renders a compact, sanitized summary for an error or log line:
// counts by severity plus the first few authoritative codes.
func ViolationSummary(violations []Violation) string {
	var errs, warns, repairable int
	codes := make([]string, 0, 4)
	for _, v := range violations {
		switch {
		case v.Repairable:
			repairable++
		case v.Severity == SeverityError:
			errs++
			if len(codes) < 4 {
				codes = append(codes, fmt.Sprintf("%s@%s", v.Code, v.Path))
			}
		case v.Severity == SeverityWarning:
			warns++
		}
	}
	summary := fmt.Sprintf("%d errors, %d warnings, %d repairable", errs, warns, repairable)
	if len(codes) > 0 {
		summary += " [" + strings.Join(codes, ", ") + "]"
	}
	return summary
}

// Validate runs the auditor over the active state under a read lock.
func (s *Store) Validate() []Violation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ValidateState(s.current)
}

func isSupportedLanguage(language string) bool {
	_, ok := supportedLanguages[language]
	return ok
}

func isSupportedEdgeType(edgeType string) bool {
	_, ok := supportedEdgeTypes[edgeType]
	return ok
}

func isForbiddenPath(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if _, bad := forbiddenPathSegments[segment]; bad {
			return true
		}
	}
	return false
}

func pathEscapes(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func isValidRange(r domain.Range) bool {
	if r.Start.Line < 1 || r.Start.Column < 1 || r.End.Line < 1 || r.End.Column < 1 {
		return false
	}
	if r.Start.Line > r.End.Line {
		return false
	}
	if r.Start.Line == r.End.Line && r.Start.Column > r.End.Column {
		return false
	}
	return true
}

// isSupportedHash accepts both pipeline forms: a bare 64-char sha256 hex digest
// (scanner) and a "sha256:"-prefixed digest (editor save path).
func isSupportedHash(hash string) bool {
	digest := strings.TrimPrefix(hash, "sha256:")
	if len(digest) != 64 {
		return false
	}
	for _, r := range digest {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func isNormalizedSlug(slug string) bool {
	if slug == "" {
		return false
	}
	for _, r := range slug {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}
