// Package lspadapter owns the language-neutral mechanics shared by semantic
// language-server adapters. Language packages retain only process policy,
// initialization options, language IDs and provider provenance.
package lspadapter

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// SourceFunc returns the content used to convert an LSP location. It selects an
// open buffer for the query document and persisted content for other files.
type SourceFunc func(query semantic.SemanticQuery, relativePath string) ([]byte, error)

// ProviderCore centralizes position conversion, provenance, ordering and
// deduplication for every language-server adapter.
type ProviderCore struct {
	WorkspaceRoot    string
	Source           SourceFunc
	ProviderID       semantic.SemanticProviderID
	ProvenanceSource string
	ErrorPrefix      string
	Version          func() string
	SessionID        func() string
}

// PreparedPosition is the language-neutral state needed by an LSP request.
type PreparedPosition struct {
	Source    []byte
	Starts    []int
	Encoding  string
	Line      int
	Character int
	URI       string
}

type indexedSource struct {
	source []byte
	starts []int
	err    error
}

// LocationConverter converts all locations in one semantic query. Its cache is
// deliberately request-scoped so source and line indexes never cross document
// versions or snapshots.
type LocationConverter struct {
	core     ProviderCore
	query    semantic.SemanticQuery
	encoding string
	byPath   map[string]indexedSource
}

func (c ProviderCore) NewLocationConverter(query semantic.SemanticQuery, encoding string) *LocationConverter {
	return &LocationConverter{core: c, query: query, encoding: encoding, byPath: make(map[string]indexedSource)}
}

// Seed reuses source and line starts already loaded by PreparePosition.
func (c *LocationConverter) Seed(relativePath string, source []byte, starts []int) {
	c.byPath[relativePath] = indexedSource{source: source, starts: starts}
}

// Convert maps one LSP location, reading and indexing each workspace path no
// more than once for this query.
func (c *LocationConverter) Convert(location lspfacts.Location) (semantic.SourceLocation, bool) {
	relative, ok := lspconv.WorkspaceRelative(c.core.WorkspaceRoot, location.URI)
	if !ok {
		return semantic.SourceLocation{Path: SanitizeURI(location.URI), Encoding: semantic.PositionEncoding(c.encoding)}, true
	}
	indexed, exists := c.byPath[relative]
	if !exists {
		indexed.source, indexed.err = c.core.Source(c.query, relative)
		if indexed.err == nil {
			indexed.starts = lspconv.LineStarts(indexed.source)
		}
		c.byPath[relative] = indexed
	}
	if indexed.err != nil {
		return semantic.SourceLocation{Path: relative, Encoding: semantic.PositionEncoding(c.encoding)}, true
	}
	rng, ok := ConvertRange(indexed.source, indexed.starts, location.Range, c.encoding)
	if !ok {
		return semantic.SourceLocation{Path: relative, Encoding: semantic.PositionEncoding(c.encoding)}, true
	}
	return semantic.SourceLocation{Path: relative, Range: rng, Encoding: semantic.EncodingUTF8}, true
}

// PreparePosition converts the internal query position once for a request.
func (c ProviderCore) PreparePosition(query semantic.SemanticQuery, encoding, uri string) (PreparedPosition, error) {
	source, err := c.Source(query, query.Path)
	if err != nil {
		return PreparedPosition{}, err
	}
	starts := lspconv.LineStarts(source)
	line, character, err := lspconv.InternalToLSP(source, starts, query.Position.Line, query.Position.Column, encoding)
	if err != nil {
		return PreparedPosition{}, fmt.Errorf("%s: %w", c.ErrorPrefix, err)
	}
	return PreparedPosition{Source: source, Starts: starts, Encoding: encoding, Line: line, Character: character, URI: uri}, nil
}

// PositionParams builds the common textDocument+position request payload.
func PositionParams(position PreparedPosition) map[string]any {
	return map[string]any{
		"textDocument": map[string]string{"uri": position.URI},
		"position":     map[string]int{"line": position.Line, "character": position.Character},
	}
}

// ConvertRange converts one LSP range using a caller-owned line index.
func ConvertRange(source []byte, starts []int, r lspfacts.Range, encoding string) (domain.Range, bool) {
	startLine, startColumn, err := lspconv.LSPToInternal(source, starts, r.Start.Line, r.Start.Character, encoding)
	if err != nil {
		return domain.Range{}, false
	}
	endLine, endColumn, err := lspconv.LSPToInternal(source, starts, r.End.Line, r.End.Character, encoding)
	if err != nil {
		return domain.Range{}, false
	}
	return domain.Range{
		Start: domain.Position{Line: startLine, Column: startColumn},
		End:   domain.Position{Line: endLine, Column: endColumn},
	}, true
}

// Fact builds one provenance-tagged language-server fact.
func (c ProviderCore) Fact(query semantic.SemanticQuery, kind, method string, location semantic.SourceLocation, detail string, object *semantic.SymbolRef) semantic.SemanticFact {
	version := ""
	if c.Version != nil {
		version = c.Version()
	}
	sessionID := ""
	if c.SessionID != nil {
		sessionID = c.SessionID()
	}
	contentHash := ""
	if query.Content != nil {
		contentHash = contenthash.HashContent(query.Content)
	}
	return semantic.SemanticFact{
		Kind: kind, Subject: semantic.SymbolRef{SymbolID: query.SymbolID, Resolved: query.SymbolID != ""},
		Object: object, Location: location, Detail: detail,
		Provenance: semantic.Provenance{
			Source: c.ProvenanceSource, ProviderID: c.ProviderID,
			ToolVersion: version, Method: method, ObservedAt: time.Now().UTC(),
		},
		Confidence: semantic.ConfidenceFor(c.ProvenanceSource, kind, true),
		SnapshotID: query.SnapshotID, DocumentID: query.DocumentID, DocumentVersion: query.DocumentVersion,
		ContentHash: contentHash, ProviderSession: sessionID,
	}
}

// FinishFacts orders facts deterministically and deduplicates only identical
// full ranges. Distinct ranges sharing a start position remain distinct.
func FinishFacts(facts []semantic.SemanticFact) []semantic.SemanticFact {
	sort.SliceStable(facts, func(i, j int) bool {
		a, b := facts[i].Location, facts[j].Location
		ai, bi := IsExternalPath(a.Path), IsExternalPath(b.Path)
		if ai != bi {
			return !ai
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Range.Start.Line != b.Range.Start.Line {
			return a.Range.Start.Line < b.Range.Start.Line
		}
		if a.Range.Start.Column != b.Range.Start.Column {
			return a.Range.Start.Column < b.Range.Start.Column
		}
		if a.Range.End.Line != b.Range.End.Line {
			return a.Range.End.Line < b.Range.End.Line
		}
		return a.Range.End.Column < b.Range.End.Column
	})
	seen := make(map[string]struct{}, len(facts))
	out := facts[:0]
	for _, fact := range facts {
		key := fmt.Sprintf("%s|%d:%d-%d:%d", fact.Location.Path,
			fact.Location.Range.Start.Line, fact.Location.Range.Start.Column,
			fact.Location.Range.End.Line, fact.Location.Range.End.Column)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, fact)
	}
	return out
}

func IsExternalPath(path string) bool { return strings.Contains(path, "://") }

func SanitizeURI(uri string) string {
	if index := strings.IndexByte(uri, '?'); index >= 0 {
		return uri[:index]
	}
	return uri
}

// SameSymbol reports whether prepared call-hierarchy items identify the same
// name in the same document.
func SameSymbol(items []lspfacts.CallHierarchyItem) bool {
	for index := 1; index < len(items); index++ {
		if items[index].Name != items[0].Name || items[index].URI != items[0].URI {
			return false
		}
	}
	return true
}

// ContextMutex is a mutex whose acquisition respects cancellation.
type ContextMutex struct{ token chan struct{} }

func NewContextMutex() *ContextMutex {
	mutex := &ContextMutex{token: make(chan struct{}, 1)}
	mutex.token <- struct{}{}
	return mutex
}

func (m *ContextMutex) Lock(ctx context.Context) error {
	select {
	case <-m.token:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *ContextMutex) Unlock() { m.token <- struct{}{} }

// DocumentState owns per-document serialization and synced versions for an
// adapter. Both language adapters use this implementation.
type DocumentState struct {
	mu     sync.Mutex
	synced map[string]int64
	locks  map[string]*ContextMutex
}

func NewDocumentState() *DocumentState {
	return &DocumentState{synced: make(map[string]int64), locks: make(map[string]*ContextMutex)}
}

func (s *DocumentState) Lock(ctx context.Context, documentID string) (func(), error) {
	s.mu.Lock()
	lock := s.locks[documentID]
	if lock == nil {
		lock = NewContextMutex()
		s.locks[documentID] = lock
	}
	s.mu.Unlock()
	if err := lock.Lock(ctx); err != nil {
		return nil, err
	}
	return lock.Unlock, nil
}

func (s *DocumentState) Set(documentID string, version int64) {
	s.mu.Lock()
	s.synced[documentID] = version
	s.mu.Unlock()
}

func (s *DocumentState) Version(documentID string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	version, ok := s.synced[documentID]
	return version, ok
}

func (s *DocumentState) Clear(documentID string) {
	s.mu.Lock()
	delete(s.synced, documentID)
	s.mu.Unlock()
}

func (s *DocumentState) Reset() {
	s.mu.Lock()
	s.synced = make(map[string]int64)
	s.locks = make(map[string]*ContextMutex)
	s.mu.Unlock()
}
