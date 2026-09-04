package store

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/edgeresolve"
	"github.com/ThalesMMS/CodeAtlas/internal/storederive"
)

// edgeIDExhausted is the fatal panic message when the monotonic edge-ID counter
// can no longer allocate a fresh, non-wrapping id.
const edgeIDExhausted = "EDGE_ID_EXHAUSTED"

// symbolCollision is the fatal cause when two distinct identities would occupy
// the same SymbolID (an astronomically unlikely hash collision); it is never
// resolved by overwriting.
const symbolCollision = "SYMBOL_ID_COLLISION"

// state holds the full mutable index state. The authoritative model is the split
// identity/occurrence form (identities, occurrences and their indexes); the flat
// `symbols` map is a derived read-cache (one flat DTO per handle) rebuilt by
// finalize, used by the lexical index and the search/graph read paths, just like
// the lexical index itself is derived. Edges and the lexical index are keyed by
// the symbol handle: the v1 SymbolID for identity-bearing symbols,
// or the OccurrenceID for occurrence-only symbols (imports, anonymous functions).
type state struct {
	version       uint64
	nextEdgeID    uint64
	files         map[string]domain.File
	identities    map[domain.SymbolID]domain.SymbolIdentity
	occurrences   map[domain.OccurrenceID]domain.SymbolOccurrence
	occBySymbol   map[domain.SymbolID][]domain.OccurrenceID
	occByFile     map[string][]domain.OccurrenceID
	occDisplay    map[domain.OccurrenceID]domain.SymbolIdentity // occurrence-only display labels (no logical id)
	symbols       map[string]domain.Symbol                      // derived: handle -> flat DTO (best occurrence)
	symbolsByName map[string][]string                           // derived: lookup name -> flat handles
	knownPaths    []string                                      // derived: sorted file paths
	edges         []domain.Edge
	adjacency     map[string][]int // derived: handle -> edge indexes (incoming + outgoing)
	wiki          map[string]domain.WikiPage
	lexical       *lexicalIndex
	indexedAt     time.Time
}

func newState() *state {
	return &state{
		nextEdgeID:    1,
		files:         make(map[string]domain.File),
		identities:    make(map[domain.SymbolID]domain.SymbolIdentity),
		occurrences:   make(map[domain.OccurrenceID]domain.SymbolOccurrence),
		occBySymbol:   make(map[domain.SymbolID][]domain.OccurrenceID),
		occByFile:     make(map[string][]domain.OccurrenceID),
		occDisplay:    make(map[domain.OccurrenceID]domain.SymbolIdentity),
		symbols:       make(map[string]domain.Symbol),
		symbolsByName: make(map[string][]string),
		adjacency:     make(map[string][]int),
		wiki:          make(map[string]domain.WikiPage),
		lexical:       newLexicalIndex(),
	}
}

// revisionOverflow is the fatal cause when the monotonic Revision counter would
// wrap; there is no wraparound.
const revisionOverflow = "REVISION_OVERFLOW"

// bumpVersion advances the Revision counter, panicking rather than wrapping at
// the uint64 ceiling.
func bumpVersion(current uint64) uint64 {
	if current == math.MaxUint64 {
		panic(revisionOverflow)
	}
	return current + 1
}

// allocEdgeID returns the next monotonic edge id and advances the counter. Ids
// start at 1; removals never free ids. Overflow is a fatal condition.
func (st *state) allocEdgeID() int64 {
	if st.nextEdgeID == 0 {
		st.nextEdgeID = 1
	}
	if st.nextEdgeID >= math.MaxInt64 {
		panic(edgeIDExhausted)
	}
	id := st.nextEdgeID
	st.nextEdgeID++
	return int64(id)
}

// clone deep-copies every authoritative map and slice and rebuilds the derived
// indexes, so the returned state shares no mutable structure with the receiver.
func (st *state) clone() *state {
	next := &state{
		version:     st.version,
		nextEdgeID:  st.nextEdgeID,
		indexedAt:   st.indexedAt,
		files:       make(map[string]domain.File, len(st.files)),
		identities:  make(map[domain.SymbolID]domain.SymbolIdentity, len(st.identities)),
		occurrences: make(map[domain.OccurrenceID]domain.SymbolOccurrence, len(st.occurrences)),
		occBySymbol: make(map[domain.SymbolID][]domain.OccurrenceID, len(st.occBySymbol)),
		occByFile:   make(map[string][]domain.OccurrenceID, len(st.occByFile)),
		occDisplay:  make(map[domain.OccurrenceID]domain.SymbolIdentity, len(st.occDisplay)),
		wiki:        make(map[string]domain.WikiPage, len(st.wiki)),
		edges:       append([]domain.Edge(nil), st.edges...),
	}
	for path, file := range st.files {
		next.files[path] = file
	}
	for id, identity := range st.identities {
		next.identities[id] = identity
	}
	for id, occurrence := range st.occurrences {
		next.occurrences[id] = occurrence
	}
	for id, occs := range st.occBySymbol {
		next.occBySymbol[id] = append([]domain.OccurrenceID(nil), occs...)
	}
	for path, occs := range st.occByFile {
		next.occByFile[path] = append([]domain.OccurrenceID(nil), occs...)
	}
	for id, identity := range st.occDisplay {
		next.occDisplay[id] = identity
	}
	for slug, page := range st.wiki {
		next.wiki[slug] = page
	}
	next.rebuildDerived()
	next.rebuildAdjacency()
	next.rebuildLexical()
	return next
}

// replaceFile applies an upsert: it removes the file's prior occurrences (and any
// now-orphan identities), resolves the new occurrences and remaps the parsed
// edges onto stable handles. The caller batches finalize().
func (st *state) replaceFile(parsed domain.ParsedFile) {
	st.removeOccurrencesForFile(parsed.File.Path)
	st.dropEdgesForFile(parsed.File.Path)
	st.files[parsed.File.Path] = parsed.File

	resolved, handleByLegacy := storederive.ResolveParsedFile(parsed)
	for _, rs := range resolved {
		st.addOccurrence(rs)
	}
	for i := range parsed.Edges {
		edge := storederive.RemapEdge(parsed.Edges[i], handleByLegacy)
		edge.ID = st.allocEdgeID()
		st.edges = append(st.edges, edge)
	}
}

// addOccurrence inserts one resolved symbol into the authoritative indexes,
// failing fatally on a genuine SymbolID collision (same id, different identity).
func (st *state) addOccurrence(resolved domain.ResolvedSymbol) {
	occurrence := resolved.Occurrence
	st.occurrences[occurrence.ID] = occurrence
	st.occByFile[occurrence.Path] = append(st.occByFile[occurrence.Path], occurrence.ID)
	if occurrence.SymbolID == "" {
		st.occDisplay[occurrence.ID] = resolved.Identity // display label for the flat view
		return
	}
	if existing, ok := st.identities[occurrence.SymbolID]; ok {
		if existing.QualifiedName != resolved.Identity.QualifiedName || existing.Kind != resolved.Identity.Kind || existing.Language != resolved.Identity.Language {
			panic(symbolCollision)
		}
	} else {
		st.identities[occurrence.SymbolID] = resolved.Identity
	}
	st.occBySymbol[occurrence.SymbolID] = append(st.occBySymbol[occurrence.SymbolID], occurrence.ID)
}

// removeOccurrencesForFile deletes every occurrence in a file, removing an
// identity only when it has no occurrences left anywhere.
func (st *state) removeOccurrencesForFile(path string) {
	for _, occID := range st.occByFile[path] {
		occurrence, ok := st.occurrences[occID]
		if !ok {
			continue
		}
		delete(st.occurrences, occID)
		if occurrence.SymbolID == "" {
			delete(st.occDisplay, occID)
			continue
		}
		remaining := st.occBySymbol[occurrence.SymbolID][:0]
		for _, candidate := range st.occBySymbol[occurrence.SymbolID] {
			if candidate != occID {
				remaining = append(remaining, candidate)
			}
		}
		if len(remaining) == 0 {
			delete(st.occBySymbol, occurrence.SymbolID)
			delete(st.identities, occurrence.SymbolID)
		} else {
			st.occBySymbol[occurrence.SymbolID] = remaining
		}
	}
	delete(st.occByFile, path)
}

// dropEdgesForFile removes edges declared in a file (kept separate from symbol
// removal so an upsert re-adds the file's edges).
func (st *state) dropEdgesForFile(path string) {
	filtered := st.edges[:0]
	for _, edge := range st.edges {
		if edge.Path == path {
			continue
		}
		filtered = append(filtered, edge)
	}
	st.edges = filtered
}

// migrateEdgeIDs derives nextEdgeID for a snapshot lacking the field. When all
// ids are unique and positive it returns max+1; otherwise it deterministically
// reassigns every id and returns the new count+1.
func migrateEdgeIDs(edges []domain.Edge) (uint64, bool) {
	seen := make(map[int64]struct{}, len(edges))
	var max int64
	repaired := false
	for _, edge := range edges {
		if edge.ID <= 0 {
			repaired = true
		}
		if _, dup := seen[edge.ID]; dup {
			repaired = true
		}
		seen[edge.ID] = struct{}{}
		if edge.ID > max {
			max = edge.ID
		}
	}
	if repaired {
		reassignEdgeIDs(edges)
		return uint64(len(edges)) + 1, true
	}
	return uint64(max) + 1, false
}

// reassignEdgeIDs assigns ids 1..N deterministically by a stable structural key.
func reassignEdgeIDs(edges []domain.Edge) {
	order := make([]int, len(edges))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return edgeOrderKey(edges[order[a]], order[a]) < edgeOrderKey(edges[order[b]], order[b])
	})
	for rank, index := range order {
		edges[index].ID = int64(rank + 1)
	}
}

func edgeOrderKey(edge domain.Edge, origIndex int) string {
	return fmt.Sprintf("%s\x00%012d\x00%s\x00%s\x00%s\x00%s\x00%012d",
		edge.Path, edge.Line, edge.Type, edge.FromSymbolID, edge.ToSymbolID, edge.ToName, origIndex)
}

// validateEdgeIDs rejects a persisted edge set with duplicate, zero/negative ids
// or an inconsistent counter.
func validateEdgeIDs(edges []domain.Edge, nextEdgeID uint64) error {
	seen := make(map[int64]struct{}, len(edges))
	var max int64
	for _, edge := range edges {
		if edge.ID <= 0 {
			return fmt.Errorf("edge with non-positive id %d", edge.ID)
		}
		if _, dup := seen[edge.ID]; dup {
			return fmt.Errorf("duplicate edge id %d", edge.ID)
		}
		seen[edge.ID] = struct{}{}
		if edge.ID > max {
			max = edge.ID
		}
	}
	if nextEdgeID <= uint64(max) {
		return fmt.Errorf("nextEdgeId %d <= max edge id %d", nextEdgeID, max)
	}
	return nil
}

// deleteFile removes a file, its occurrences (and orphan identities) and its edges.
func (st *state) deleteFile(path string) {
	st.removeOccurrencesForFile(path)
	delete(st.files, path)
	st.dropEdgesForFile(path)
}

// finalize rebuilds the derived flat view, resolves edges, then rebuilds the
// adjacency and lexical indexes over that resolved snapshot.
func (st *state) finalize() {
	st.rebuildDerived()
	st.resolveEdges()
	st.rebuildAdjacency()
	st.rebuildLexical()
}

// rebuildDerived rebuilds the flat symbol/name/path read-caches from the
// authoritative files, identities and occurrences.
func (st *state) rebuildDerived() {
	st.symbols = make(map[string]domain.Symbol, len(st.identities))
	st.symbolsByName = make(map[string][]string)
	st.knownPaths = make([]string, 0, len(st.files))
	for path := range st.files {
		st.knownPaths = append(st.knownPaths, path)
	}
	sort.Strings(st.knownPaths)
	// Identity-bearing symbols: pick the best occurrence (smallest range, then
	// lexicographic by path) for a deterministic flat view.
	for symbolID, occIDs := range st.occBySymbol {
		best := st.bestOccurrence(occIDs)
		occurrence, ok := st.occurrences[best]
		if !ok {
			continue
		}
		st.symbols[string(symbolID)] = flattenSymbol(st.identities[symbolID], occurrence)
	}
	// Occurrence-only symbols: one flat entry each, keyed by OccurrenceID, using
	// the display label so the symbol stays nameable.
	for occID, occurrence := range st.occurrences {
		if occurrence.SymbolID != "" {
			continue
		}
		st.symbols[string(occID)] = flattenSymbol(st.occDisplay[occID], occurrence)
	}
	for handle, symbol := range st.symbols {
		for _, name := range storederive.SymbolLookupNames(symbol) {
			st.symbolsByName[name] = append(st.symbolsByName[name], handle)
		}
	}
	for name := range st.symbolsByName {
		sort.Strings(st.symbolsByName[name])
	}
}

// bestOccurrence picks the deterministic primary occurrence for an identity.
func (st *state) bestOccurrence(occIDs []domain.OccurrenceID) domain.OccurrenceID {
	var best domain.OccurrenceID
	bestSpan := math.MaxInt
	for _, occID := range occIDs {
		occurrence, ok := st.occurrences[occID]
		if !ok {
			continue
		}
		span := (occurrence.Range.End.Line-occurrence.Range.Start.Line)*1_000_000 +
			(occurrence.Range.End.Column - occurrence.Range.Start.Column)
		if best == "" || span < bestSpan || (span == bestSpan && occID < best) {
			best = occID
			bestSpan = span
		}
	}
	return best
}

// flattenSymbol builds the flat read DTO from an identity and one occurrence. The
// handle (SymbolID or OccurrenceID) becomes the flat id.
func flattenSymbol(identity domain.SymbolIdentity, occurrence domain.SymbolOccurrence) domain.Symbol {
	return domain.ResolvedSymbol{Identity: identity, Occurrence: occurrence}.ToSymbol()
}

func (st *state) resolveEdges() {
	edgeresolve.Resolve(st.symbols, st.edges)
}

func (st *state) rebuildAdjacency() {
	st.adjacency = make(map[string][]int)
	for index, edge := range st.edges {
		st.adjacency[edge.FromSymbolID] = append(st.adjacency[edge.FromSymbolID], index)
		if edge.ToSymbolID != "" && edge.ToSymbolID != edge.FromSymbolID {
			st.adjacency[edge.ToSymbolID] = append(st.adjacency[edge.ToSymbolID], index)
		}
	}
}

func (st *state) rebuildLexical() {
	index := newLexicalIndex()
	for _, symbol := range st.symbols {
		index.add(symbol)
	}
	index.finalize()
	st.lexical = index
}

// symbolAt returns the most specific occurrence (smallest containing range) at a
// position, with its identity. ok is false when no occurrence contains it.
func (st *state) symbolAt(path string, line, column int) (domain.ResolvedSymbol, bool) {
	var best domain.SymbolOccurrence
	bestSpan := math.MaxInt
	found := false
	for _, occID := range st.occByFile[path] {
		occurrence, ok := st.occurrences[occID]
		if !ok || !contains(occurrence.Range, line, column) {
			continue
		}
		span := (occurrence.Range.End.Line-occurrence.Range.Start.Line)*10_000 +
			(occurrence.Range.End.Column - occurrence.Range.Start.Column)
		if span < bestSpan {
			best = occurrence
			bestSpan = span
			found = true
		}
	}
	if !found {
		return domain.ResolvedSymbol{}, false
	}
	return domain.ResolvedSymbol{Identity: st.identities[best.SymbolID], Occurrence: best}, true
}

// resolveSymbol finds the best symbol handle for an identifier, preferring the
// same file and directory, over the derived flat view.
func (st *state) resolveSymbol(name, currentPath string) (domain.Symbol, bool) {
	candidates := make([]domain.Symbol, 0, len(st.symbols))
	for _, symbol := range st.symbols {
		candidates = append(candidates, symbol)
	}
	return storederive.ResolveSymbol(candidates, name, currentPath)
}

// graph walks the structural neighborhood of the seed handles over the derived
// flat view and the edge set.
func (st *state) graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge) {
	nodes, edges, _ := st.graphWithStats(seedIDs, depth, maxNodes)
	return nodes, edges
}

type graphTraversalStats struct {
	edgeVisits int
}

func (st *state) graphWithStats(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge, graphTraversalStats) {
	visited, edgeVisits := storederive.TraverseGraph(seedIDs, depth, maxNodes, func(id string) bool {
		_, exists := st.symbols[id]
		return exists
	}, st.adjacency, st.edges)
	nodes := make([]domain.Symbol, 0, len(visited))
	for id := range visited {
		nodes = append(nodes, st.symbols[id])
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Path == nodes[j].Path {
			return nodes[i].Range.Start.Line < nodes[j].Range.Start.Line
		}
		return nodes[i].Path < nodes[j].Path
	})
	return nodes, storederive.GraphEdges(st.edges, st.adjacency, visited), graphTraversalStats{edgeVisits: edgeVisits}
}
