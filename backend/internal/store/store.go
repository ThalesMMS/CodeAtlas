package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/storederive"
	"github.com/ThalesMMS/CodeAtlas/internal/symbols"
)

const snapshotVersion = 1

type Store struct {
	mu        sync.RWMutex
	path      string
	persister snapshotPersister
	current   *state
}

type diskSnapshot struct {
	Version        int                       `json:"version"`
	StoreVersion   uint64                    `json:"storeVersion"`
	NextEdgeID     uint64                    `json:"nextEdgeId,omitempty"`
	Files          []domain.File             `json:"files"`
	Identities     []domain.SymbolIdentity   `json:"identities,omitempty"`
	Occurrences    []domain.SymbolOccurrence `json:"occurrences,omitempty"`
	Symbols        []domain.Symbol           `json:"symbols,omitempty"` // legacy (pre-v1) detection only
	Edges          []domain.Edge             `json:"edges"`
	Wiki           []domain.WikiPage         `json:"wiki"`
	SnapshotID     domain.SnapshotID         `json:"snapshotId,omitempty"`
	SnapshotSchema int                       `json:"snapshotSchema,omitempty"`
	IndexedAt      time.Time                 `json:"indexedAt"`
}

func Open(path string) (*Store, error) {
	return openWith(path, osFileSystem{})
}

// openWith constructs a Store over an injectable fileSystem so the persistence
// protocol can be fault-injected in tests.
func openWith(path string, fs fileSystem) (*Store, error) {
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	s := &Store{
		path:      path,
		persister: snapshotPersister{fs: fs, path: path},
		current:   newState(),
	}
	if err := s.recoverAndLoad(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

// Persist saves the current active state through the atomic write protocol.
func (s *Store) Persist() error {
	s.mu.RLock()
	data, err := encodeSnapshot(s.current)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	tempName, err := s.persister.writeTemp(data)
	if err != nil {
		return err
	}
	return s.persister.replace(tempName)
}

// Version returns the current optimistic-control version of the active state.
func (s *Store) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.version
}

// Revision returns the monotonic commit counter as the typed Revision.
func (s *Store) Revision() domain.Revision {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return domain.Revision(s.current.version)
}

// OccurrencesForSymbol returns every occurrence of a logical symbol in the active
// snapshot (one identity may appear more than once, e.g. duplicate declarations).
func (s *Store) OccurrencesForSymbol(id domain.SymbolID) []domain.SymbolOccurrence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	occIDs := s.current.occBySymbol[id]
	occurrences := make([]domain.SymbolOccurrence, 0, len(occIDs))
	for _, occID := range occIDs {
		if occurrence, ok := s.current.occurrences[occID]; ok {
			occurrences = append(occurrences, occurrence)
		}
	}
	return occurrences
}

// SnapshotID returns the content-addressed id of the current state.
func (s *Store) SnapshotID() domain.SnapshotID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return computeSnapshotID(s.current)
}

// SnapshotMetadata returns the published provenance of the current state. The id
// is recomputed from the authoritative state, so it always matches the content.
func (s *Store) SnapshotMetadata() domain.SnapshotMetadata {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.metadataLocked()
}

func (s *Store) metadataLocked() domain.SnapshotMetadata {
	return domain.SnapshotMetadata{
		ID:                computeSnapshotID(s.current),
		Revision:          domain.Revision(s.current.version),
		CreatedAt:         s.current.indexedAt,
		Schema:            snapshotSchema,
		IdentityAlgorithm: symbols.IdentityAlgorithmVersion,
	}
}

func (s *Store) ReplaceFile(parsed domain.ParsedFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.replaceFile(parsed)
	s.current.indexedAt = time.Now().UTC()
	s.current.version = bumpVersion(s.current.version)
	s.current.finalize()
}

func (s *Store) DeleteFile(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.deleteFile(path)
	s.current.indexedAt = time.Now().UTC()
	s.current.version = bumpVersion(s.current.version)
	s.current.finalize()
}

func (s *Store) FileHash(path string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, ok := s.current.files[path]
	return file.Hash, ok
}

func (s *Store) KnownPaths() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string{}, s.current.knownPaths...)
}

func (s *Store) ListFiles() []domain.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	files := make([]domain.File, 0, len(s.current.files))
	for _, file := range s.current.files {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func (s *Store) FileTree() domain.FileTreeNode {
	return storederive.FileTree(s.ListFiles())
}

// SymbolAt returns the most specific resolved symbol (identity + occurrence) at a
// position in the active snapshot.
func (s *Store) SymbolAt(path string, line, column int) (domain.ResolvedSymbol, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.symbolAt(path, line, column)
}

func contains(r domain.Range, line, column int) bool {
	if line < r.Start.Line || line > r.End.Line {
		return false
	}
	if line == r.Start.Line && column < r.Start.Column {
		return false
	}
	if line == r.End.Line && column > r.End.Column {
		return false
	}
	return true
}

func (s *Store) GetSymbol(id string) (domain.Symbol, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	symbol, ok := s.current.symbols[id]
	return symbol, ok
}

func (s *Store) ResolveSymbol(name, currentPath string) (domain.Symbol, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.resolveSymbol(name, currentPath)
}

func (s *Store) Search(query string, limit int) []domain.SearchHit {
	if limit <= 0 {
		limit = 20
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	hits := s.current.lexical.search(query, limit, s.current.symbols)
	snapshotID := computeSnapshotID(s.current)
	for i := range hits {
		hits[i].SnapshotID = snapshotID
	}
	return hits
}

// RebuildLexical refreshes the complete derived lexical index without changing
// the structural snapshot or its optimistic-control version.
func (s *Store) RebuildLexical() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.rebuildLexical()
}

func (s *Store) Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.graph(seedIDs, depth, maxNodes)
}

func (s *Store) AllSymbols() []domain.Symbol {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Symbol, 0, len(s.current.symbols))
	for _, symbol := range s.current.symbols {
		result = append(result, symbol)
	}
	return result
}

func (s *Store) AllEdges() []domain.Edge {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]domain.Edge(nil), s.current.edges...)
}

func (s *Store) ReplaceWikiPages(pages []domain.WikiPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wiki := make(map[string]domain.WikiPage, len(pages))
	for _, page := range pages {
		wiki[page.Slug] = page
	}
	s.current.wiki = wiki
	s.current.version = bumpVersion(s.current.version)
}

func (s *Store) WikiPages() []domain.WikiPage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pages := make([]domain.WikiPage, 0, len(s.current.wiki))
	for _, page := range s.current.wiki {
		pages = append(pages, page)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Slug < pages[j].Slug })
	return pages
}

func (s *Store) Counts() (files, symbols, edges, wiki int, indexedAt time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.current.files), len(s.current.symbols), len(s.current.edges), len(s.current.wiki), s.current.indexedAt
}
