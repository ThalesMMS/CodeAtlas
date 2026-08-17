package store

import (
	"sort"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// ReadView is an immutable, pinned snapshot of the store. Every read in a single
// operation must go through one ReadView so a symbol lookup and a graph walk can
// never observe two different revisions. Its Metadata identifies exactly which
// snapshot produced the answer.
type ReadView interface {
	Metadata() domain.SnapshotMetadata
	Files() []domain.File
	Search(query string, limit int) []domain.SearchHit
	AllSymbols() []domain.Symbol
	SymbolAt(path string, line, column int) (domain.ResolvedSymbol, bool)
	ResolveSymbol(name, currentPath string) (domain.Symbol, bool)
	GetSymbol(id string) (domain.Symbol, bool)
	GetSymbolByOccurrence(occurrenceID string) (domain.Symbol, bool)
	SymbolsByName(name string) []domain.Symbol
	SymbolsByPath(path string) []domain.Symbol
	OccurrencesForSymbol(id domain.SymbolID) []domain.SymbolOccurrence
	Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge)
	EdgesForSymbol(id string) []domain.Edge
	AllEdges() []domain.Edge
	Embeddings() map[string][]float64
}

// readView pins a deep copy of the state so reads are isolated from concurrent
// writers without holding a lock for the operation's lifetime.
type readView struct {
	st   *state
	meta domain.SnapshotMetadata
}

// Snapshot returns an isolated ReadView of the current state, copied under a
// short read lock.
func (s *Store) Snapshot() ReadView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &readView{st: s.current.clone(), meta: s.metadataLocked()}
}

func (v *readView) Metadata() domain.SnapshotMetadata { return v.meta }

func (v *readView) Files() []domain.File {
	files := make([]domain.File, 0, len(v.st.files))
	for _, file := range v.st.files {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func (v *readView) Search(query string, limit int) []domain.SearchHit {
	if limit <= 0 {
		limit = 20
	}
	return v.st.lexical.search(query, limit, v.st.symbols)
}

func (v *readView) AllSymbols() []domain.Symbol {
	symbols := make([]domain.Symbol, 0, len(v.st.symbols))
	for _, symbol := range v.st.symbols {
		symbols = append(symbols, symbol)
	}
	sort.Slice(symbols, func(i, j int) bool { return symbols[i].ID < symbols[j].ID })
	return symbols
}

func (v *readView) SymbolAt(path string, line, column int) (domain.ResolvedSymbol, bool) {
	return v.st.symbolAt(path, line, column)
}

func (v *readView) ResolveSymbol(name, currentPath string) (domain.Symbol, bool) {
	return v.st.resolveSymbol(name, currentPath)
}

func (v *readView) GetSymbol(id string) (domain.Symbol, bool) {
	symbol, ok := v.st.symbols[id]
	return symbol, ok
}

func (v *readView) GetSymbolByOccurrence(occurrenceID string) (domain.Symbol, bool) {
	occurrence, ok := v.st.occurrences[domain.OccurrenceID(occurrenceID)]
	if ok {
		if occurrence.SymbolID != "" {
			identity, found := v.st.identities[occurrence.SymbolID]
			if !found {
				return domain.Symbol{}, false
			}
			return flattenSymbol(identity, occurrence), true
		}
		identity, found := v.st.occDisplay[occurrence.ID]
		if !found {
			return domain.Symbol{}, false
		}
		return flattenSymbol(identity, occurrence), true
	}
	// Preserve the previous service behavior where a symbol handle was accepted
	// in the occurrence field as a fallback.
	symbol, ok := v.st.symbols[occurrenceID]
	return symbol, ok
}

func (v *readView) SymbolsByName(name string) []domain.Symbol {
	handles := v.st.symbolsByName[name]
	symbols := make([]domain.Symbol, 0, len(handles))
	for _, handle := range handles {
		if symbol, ok := v.st.symbols[handle]; ok {
			symbols = append(symbols, symbol)
		}
	}
	return symbols
}

func (v *readView) SymbolsByPath(path string) []domain.Symbol {
	handles := make(map[string]struct{}, len(v.st.occByFile[path]))
	symbols := make([]domain.Symbol, 0, len(v.st.occByFile[path]))
	for _, occurrenceID := range v.st.occByFile[path] {
		occurrence := v.st.occurrences[occurrenceID]
		handle := string(occurrence.SymbolID)
		if handle == "" {
			handle = string(occurrence.ID)
		}
		if _, duplicate := handles[handle]; duplicate {
			continue
		}
		symbol, ok := v.st.symbols[handle]
		if !ok {
			continue
		}
		handles[handle] = struct{}{}
		symbols = append(symbols, symbol)
	}
	sort.Slice(symbols, func(i, j int) bool { return symbols[i].ID < symbols[j].ID })
	return symbols
}

func (v *readView) OccurrencesForSymbol(id domain.SymbolID) []domain.SymbolOccurrence {
	occIDs := v.st.occBySymbol[id]
	occurrences := make([]domain.SymbolOccurrence, 0, len(occIDs))
	for _, occID := range occIDs {
		if occurrence, ok := v.st.occurrences[occID]; ok {
			occurrences = append(occurrences, occurrence)
		}
	}
	return occurrences
}

func (v *readView) Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge) {
	return v.st.graph(seedIDs, depth, maxNodes)
}

func (v *readView) EdgesForSymbol(id string) []domain.Edge {
	indexes := v.st.adjacency[id]
	edges := make([]domain.Edge, 0, len(indexes))
	for _, index := range indexes {
		edges = append(edges, v.st.edges[index])
	}
	return edges
}

func (v *readView) AllEdges() []domain.Edge {
	return append([]domain.Edge(nil), v.st.edges...)
}

func (v *readView) Embeddings() map[string][]float64 {
	out := make(map[string][]float64, len(v.st.embeddings))
	for id, vector := range v.st.embeddings {
		out[id] = append([]float64(nil), vector...)
	}
	return out
}
