// Package changeset defines an immutable, validated description of a complete
// index update: files to upsert, paths to delete and embeddings produced during
// preparation. A ChangeSet never writes files, persists the store, calls the
// embedding provider or publishes events; it only describes a change so a later
// phase can apply it atomically.
package changeset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/pathutil"
)

// Severity classifies a diagnostic produced while preparing a change.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Diagnostic is sanitized audit metadata about one preparation step. It never
// carries prompts or AI responses.
type Diagnostic struct {
	Path     string   `json:"path,omitempty"`
	Stage    string   `json:"stage,omitempty"`
	Severity Severity `json:"severity"`
	Code     string   `json:"code,omitempty"`
	Message  string   `json:"message,omitempty"`
}

// Stable validation error codes.
const (
	CodePathInvalid          = "CHANGESET_PATH_INVALID"
	CodeUpsertDeleteConflict = "CHANGESET_UPSERT_DELETE_CONFLICT"
	CodeDuplicateUpsert      = "CHANGESET_DUPLICATE_UPSERT"
	CodeSymbolPathMismatch   = "CHANGESET_SYMBOL_PATH_MISMATCH"
	CodeDuplicateSymbolID    = "CHANGESET_DUPLICATE_SYMBOL_ID"
	CodeDuplicateOccurrence  = "CHANGESET_DUPLICATE_OCCURRENCE_ID"
	CodeEmbeddingUnknownSym  = "CHANGESET_EMBEDDING_UNKNOWN_SYMBOL"
	CodeEmbeddingInvalidVec  = "CHANGESET_EMBEDDING_INVALID_VECTOR"
	CodeEmbeddingDimMismatch = "CHANGESET_EMBEDDING_DIMENSION_MISMATCH"
	CodeEmpty                = "CHANGESET_EMPTY"
)

// ValidationError is a typed error with a stable code so callers can branch on
// the cause without parsing strings.
type ValidationError struct {
	Code    string
	Path    string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Path)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func validationError(code, pathValue, message string) *ValidationError {
	return &ValidationError{Code: code, Path: pathValue, Message: message}
}

// ChangeSet is an immutable, validated index update. Its fields are unexported
// and exposed through copying accessors so neither the builder's input nor a
// consumer can mutate it after construction.
type ChangeSet struct {
	id              string
	expectedVersion uint64
	createdAt       time.Time
	upserts         []domain.ParsedFile
	deletedPaths    []string
	embeddings      map[string][]float64
	diagnostics     []Diagnostic
	canonical       []byte
}

func (c *ChangeSet) ID() string              { return c.id }
func (c *ChangeSet) ExpectedVersion() uint64 { return c.expectedVersion }
func (c *ChangeSet) CreatedAt() time.Time    { return c.createdAt }
func (c *ChangeSet) Canonical() []byte       { return append([]byte(nil), c.canonical...) }

// IsNoop reports whether the change carries no upserts, deletions or embeddings.
func (c *ChangeSet) IsNoop() bool {
	return len(c.upserts) == 0 && len(c.deletedPaths) == 0 && len(c.embeddings) == 0
}

// HasErrors reports whether any diagnostic is error-severity. The indexer policy
// must refuse to commit a ChangeSet for which this is true.
func (c *ChangeSet) HasErrors() bool {
	for _, diagnostic := range c.diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (c *ChangeSet) Upserts() []domain.ParsedFile {
	out := make([]domain.ParsedFile, len(c.upserts))
	for i := range c.upserts {
		out[i] = cloneParsedFile(c.upserts[i])
	}
	return out
}

func (c *ChangeSet) DeletedPaths() []string {
	return append([]string(nil), c.deletedPaths...)
}

func (c *ChangeSet) Embeddings() map[string][]float64 {
	return cloneEmbeddings(c.embeddings)
}

func (c *ChangeSet) Diagnostics() []Diagnostic {
	return append([]Diagnostic(nil), c.diagnostics...)
}

// Builder accumulates the parts of a change before validation. It is not safe
// for concurrent use.
type Builder struct {
	expectedVersion uint64
	allowEmpty      bool
	upserts         []domain.ParsedFile
	deletedPaths    []string
	embeddings      map[string][]float64
	diagnostics     []Diagnostic
}

func NewBuilder() *Builder {
	return &Builder{embeddings: make(map[string][]float64)}
}

func (b *Builder) WithExpectedVersion(version uint64) *Builder {
	b.expectedVersion = version
	return b
}

// AllowEmpty permits Build to succeed with no content (an explicit no-op).
func (b *Builder) AllowEmpty() *Builder {
	b.allowEmpty = true
	return b
}

func (b *Builder) Upsert(parsed domain.ParsedFile) *Builder {
	b.upserts = append(b.upserts, parsed)
	return b
}

func (b *Builder) Delete(deletedPath string) *Builder {
	b.deletedPaths = append(b.deletedPaths, deletedPath)
	return b
}

func (b *Builder) Embed(symbolID string, vector []float64) *Builder {
	b.embeddings[symbolID] = vector
	return b
}

func (b *Builder) Diagnose(diagnostic Diagnostic) *Builder {
	b.diagnostics = append(b.diagnostics, diagnostic)
	return b
}

// Build validates and normalizes the accumulated parts, deep-copying every
// collection, and returns an immutable ChangeSet. createdAt is injected so the
// timestamp never depends on a hidden clock; it is excluded from the canonical
// identity. A returned error is always a *ValidationError.
func (b *Builder) Build(createdAt time.Time) (*ChangeSet, error) {
	upserts, err := normalizeUpserts(b.upserts)
	if err != nil {
		return nil, err
	}
	deletedPaths, err := normalizePaths(b.deletedPaths)
	if err != nil {
		return nil, err
	}

	upsertByPath := make(map[string]struct{}, len(upserts))
	symbolIDs := make(map[string]struct{})
	occurrenceIDs := make(map[string]struct{})
	legacySymbolIDs := make(map[string]struct{})
	for _, parsed := range upserts {
		if _, dup := upsertByPath[parsed.File.Path]; dup {
			return nil, validationError(CodeDuplicateUpsert, parsed.File.Path, "path appears in more than one upsert")
		}
		upsertByPath[parsed.File.Path] = struct{}{}
		for _, symbol := range parsed.Symbols {
			if normalizePath(symbol.Path) != parsed.File.Path {
				return nil, validationError(CodeSymbolPathMismatch, symbol.Path, "symbol path differs from its parent file")
			}
			if symbol.OccurrenceID != "" {
				if _, legacy := legacySymbolIDs[symbol.ID]; legacy {
					return nil, validationError(CodeDuplicateSymbolID, parsed.File.Path, "symbol id mixes canonical and legacy handles "+symbol.ID)
				}
				if _, dup := occurrenceIDs[symbol.OccurrenceID]; dup {
					return nil, validationError(CodeDuplicateOccurrence, parsed.File.Path, "duplicate occurrence id "+symbol.OccurrenceID)
				}
				occurrenceIDs[symbol.OccurrenceID] = struct{}{}
			} else if _, dup := symbolIDs[symbol.ID]; dup {
				// Legacy batches have no OccurrenceID, so their parser-local
				// handles must remain unique for unambiguous edge remapping.
				return nil, validationError(CodeDuplicateSymbolID, parsed.File.Path, "duplicate symbol id "+symbol.ID)
			} else {
				legacySymbolIDs[symbol.ID] = struct{}{}
			}
			symbolIDs[symbol.ID] = struct{}{}
		}
	}
	for _, deleted := range deletedPaths {
		if _, conflict := upsertByPath[deleted]; conflict {
			return nil, validationError(CodeUpsertDeleteConflict, deleted, "path is both upserted and deleted")
		}
	}

	embeddings, err := normalizeEmbeddings(b.embeddings, symbolIDs)
	if err != nil {
		return nil, err
	}

	if !b.allowEmpty && len(upserts) == 0 && len(deletedPaths) == 0 && len(embeddings) == 0 {
		return nil, validationError(CodeEmpty, "", "change set has no content")
	}

	canonical := canonicalBytes(b.expectedVersion, upserts, deletedPaths, embeddings)
	digest := sha256.Sum256(canonical)

	return &ChangeSet{
		id:              "sha256:" + hex.EncodeToString(digest[:]),
		expectedVersion: b.expectedVersion,
		createdAt:       createdAt.UTC(),
		upserts:         upserts,
		deletedPaths:    deletedPaths,
		embeddings:      embeddings,
		diagnostics:     append([]Diagnostic(nil), b.diagnostics...),
		canonical:       canonical,
	}, nil
}

func normalizeUpserts(input []domain.ParsedFile) ([]domain.ParsedFile, error) {
	upserts := make([]domain.ParsedFile, 0, len(input))
	for _, parsed := range input {
		normalizedPath, err := validatePath(parsed.File.Path)
		if err != nil {
			return nil, err
		}
		clone := cloneParsedFile(parsed)
		clone.File.Path = normalizedPath
		for i := range clone.Symbols {
			clone.Symbols[i].Path = normalizePath(clone.Symbols[i].Path)
		}
		for i := range clone.Edges {
			clone.Edges[i].Path = normalizePath(clone.Edges[i].Path)
		}
		sort.SliceStable(clone.Symbols, func(i, j int) bool {
			if clone.Symbols[i].ID != clone.Symbols[j].ID {
				return clone.Symbols[i].ID < clone.Symbols[j].ID
			}
			return clone.Symbols[i].OccurrenceID < clone.Symbols[j].OccurrenceID
		})
		sort.SliceStable(clone.Edges, func(i, j int) bool { return edgeKey(clone.Edges[i]) < edgeKey(clone.Edges[j]) })
		upserts = append(upserts, clone)
	}
	sort.SliceStable(upserts, func(i, j int) bool { return upserts[i].File.Path < upserts[j].File.Path })
	return upserts, nil
}

func normalizePaths(input []string) ([]string, error) {
	paths := make([]string, 0, len(input))
	for _, candidate := range input {
		normalized, err := validatePath(candidate)
		if err != nil {
			return nil, err
		}
		paths = append(paths, normalized)
	}
	sort.Strings(paths)
	return paths, nil
}

func normalizeEmbeddings(input map[string][]float64, symbolIDs map[string]struct{}) (map[string][]float64, error) {
	if len(input) == 0 {
		return map[string][]float64{}, nil
	}
	dimension := -1
	embeddings := make(map[string][]float64, len(input))
	for symbolID, vector := range input {
		if _, ok := symbolIDs[symbolID]; !ok {
			return nil, validationError(CodeEmbeddingUnknownSym, "", "embedding for unknown symbol "+symbolID)
		}
		if len(vector) == 0 {
			return nil, validationError(CodeEmbeddingInvalidVec, "", "empty embedding vector for "+symbolID)
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, validationError(CodeEmbeddingInvalidVec, "", "non-finite embedding value for "+symbolID)
			}
		}
		if dimension == -1 {
			dimension = len(vector)
		} else if len(vector) != dimension {
			return nil, validationError(CodeEmbeddingDimMismatch, "", "embedding dimensions differ within the change set")
		}
		embeddings[symbolID] = append([]float64(nil), vector...)
	}
	return embeddings, nil
}

// validatePath rejects empty, absolute, dot, or escaping paths and returns the
// path normalized to forward slashes.
func validatePath(candidate string) (string, error) {
	normalized, err := pathutil.NormalizeWorkspaceRelative(candidate)
	if err != nil {
		return "", validationError(CodePathInvalid, candidate, "path must be workspace-relative")
	}
	if strings.ReplaceAll(candidate, "\\", "/") != normalized {
		return "", validationError(CodePathInvalid, candidate, "path is not in canonical form")
	}
	return normalized, nil
}

func normalizePath(value string) string {
	normalized, err := pathutil.NormalizeWorkspaceRelative(value)
	if err != nil {
		return strings.ReplaceAll(value, "\\", "/")
	}
	return normalized
}

func edgeKey(edge domain.Edge) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d", edge.FromSymbolID, edge.ToSymbolID, edge.ToName, edge.Type, edge.Path, edge.Line)
}

// canonicalBytes produces a stable serialization of the authoritative change,
// excluding volatile fields (id, createdAt, diagnostics).
func canonicalBytes(expectedVersion uint64, upserts []domain.ParsedFile, deletedPaths []string, embeddings map[string][]float64) []byte {
	type canonicalEmbedding struct {
		SymbolID string    `json:"symbolId"`
		Vector   []float64 `json:"vector"`
	}
	embeddingList := make([]canonicalEmbedding, 0, len(embeddings))
	for symbolID, vector := range embeddings {
		embeddingList = append(embeddingList, canonicalEmbedding{SymbolID: symbolID, Vector: vector})
	}
	sort.Slice(embeddingList, func(i, j int) bool { return embeddingList[i].SymbolID < embeddingList[j].SymbolID })

	canonical := struct {
		ExpectedVersion uint64               `json:"expectedVersion"`
		Upserts         []domain.ParsedFile  `json:"upserts"`
		DeletedPaths    []string             `json:"deletedPaths"`
		Embeddings      []canonicalEmbedding `json:"embeddings"`
	}{
		ExpectedVersion: expectedVersion,
		Upserts:         upserts,
		DeletedPaths:    deletedPaths,
		Embeddings:      embeddingList,
	}
	data, _ := json.Marshal(canonical)
	return data
}

func cloneParsedFile(parsed domain.ParsedFile) domain.ParsedFile {
	clone := domain.ParsedFile{File: parsed.File}
	if parsed.Symbols != nil {
		clone.Symbols = append([]domain.Symbol(nil), parsed.Symbols...)
	}
	if parsed.Edges != nil {
		clone.Edges = append([]domain.Edge(nil), parsed.Edges...)
	}
	return clone
}

func cloneEmbeddings(input map[string][]float64) map[string][]float64 {
	out := make(map[string][]float64, len(input))
	for symbolID, vector := range input {
		out[symbolID] = append([]float64(nil), vector...)
	}
	return out
}
