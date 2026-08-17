package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/edgeresolve"
	"github.com/ThalesMMS/CodeAtlas/internal/lexical"
	"github.com/ThalesMMS/CodeAtlas/internal/storederive"
)

const defaultReadViewWindow = 30 * time.Second

// ReadView is a pinned, snapshot-coherent structural view. It is materialized from
// one read transaction at open and then released, so it never holds a long read
// transaction that would block WAL checkpointing — callers build the ContextPack
// from it and Close it before any remote/LLM call. Structural queries are
// infallible (data is already in memory); Search is explicit about FTS5 landing in
// #54 and never fakes a result.
type ReadView interface {
	Metadata() domain.SnapshotMetadata
	Files() []domain.File
	AllSymbols() []domain.Symbol
	GetSymbol(id string) (domain.Symbol, bool)
	GetSymbolByOccurrence(occurrenceID string) (domain.Symbol, bool)
	SymbolsByName(name string) []domain.Symbol
	SymbolsByPath(path string) []domain.Symbol
	SymbolAt(path string, line, column int) (domain.ResolvedSymbol, bool)
	ResolveSymbol(name, currentPath string) (domain.Symbol, bool)
	OccurrencesForSymbol(id domain.SymbolID) []domain.SymbolOccurrence
	Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge)
	EdgesForSymbol(id string) []domain.Edge
	AllEdges() []domain.Edge
	Embeddings() map[string][]float64
	Search(query string, limit int) ([]domain.SearchHit, error)
	SearchDense(ctx context.Context, queryVector []float64, limit int) ([]DenseHit, error)
	ApplyOverlay(parsed domain.ParsedFile) error
	Close() error
}

type readView struct {
	metadata    domain.SnapshotMetadata
	files       []domain.File
	identities  map[string]domain.SymbolIdentity
	occurrences map[string]domain.SymbolOccurrence
	occBySymbol map[string][]string
	occByFile   map[string][]string
	flat        map[string]domain.Symbol
	flatOrder   []string
	byName      map[string][]string
	boostFields map[string]sqliteLexicalBoostFields
	edges       []domain.Edge
	adjacency   map[string][]int // handle → edge indexes (outgoing + incoming)
	overlayIDs  map[string]struct{}
	openedAt    time.Time
	closed      bool

	// reader/searchCtx let Search run the FTS5 query against the reader pool (the
	// derived FTS index reflects the committed state) without holding the view's
	// materialization transaction open.
	reader    *sql.DB
	searchCtx context.Context
}

type sqliteLexicalBoostFields struct {
	name      string
	qualified string
}

var _ ReadView = (*readView)(nil)

// OpenReadView materializes a coherent structural snapshot under a single read
// transaction bounded by the configured window, then commits it (releasing the
// read lock) so the view holds no transaction afterwards.
func (s *Store) OpenReadView(ctx context.Context) (ReadView, error) {
	window := s.maxReadViewWindow
	if window <= 0 {
		window = defaultReadViewWindow
	}
	viewCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	tx, err := s.db.Reader().BeginTx(viewCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, apperror.DatabaseOpenFailed(err)
	}
	defer tx.Rollback() //nolint:errcheck

	view := &readView{
		identities:  map[string]domain.SymbolIdentity{},
		occurrences: map[string]domain.SymbolOccurrence{},
		occBySymbol: map[string][]string{},
		occByFile:   map[string][]string{},
		flat:        map[string]domain.Symbol{},
		byName:      map[string][]string{},
		boostFields: map[string]sqliteLexicalBoostFields{},
		adjacency:   map[string][]int{},
		openedAt:    time.Now(),
		reader:      s.db.Reader(),
		searchCtx:   ctx,
	}
	if view.metadata, err = readMetadata(viewCtx, tx); err != nil {
		return nil, err
	}
	if err := view.loadFiles(viewCtx, tx); err != nil {
		return nil, err
	}
	if err := view.loadIdentities(viewCtx, tx); err != nil {
		return nil, err
	}
	if err := view.loadOccurrences(viewCtx, tx); err != nil {
		return nil, err
	}
	if err := view.loadEdges(viewCtx, tx); err != nil {
		return nil, err
	}
	// Embeddings are NOT materialized: dense search streams them (bounded memory),
	// and Embeddings() is a lazy legacy accessor. See dense.go.
	view.buildFlat()
	view.resolveEdges()
	view.buildAdjacency()
	// The read transaction has done its job; release it before returning so no long
	// view transaction lingers across the caller's downstream work.
	_ = tx.Rollback()
	return view, nil
}

func (v *readView) loadFiles(ctx context.Context, tx *sql.Tx) error {
	rs, err := tx.QueryContext(ctx, "SELECT path, language, content_hash, size, modified_at, indexed_at, content, content_truncated FROM files ORDER BY path")
	if err != nil {
		return apperror.StoreCorrupted(err)
	}
	defer rs.Close()
	for rs.Next() {
		var file domain.File
		var modified, indexed string
		var contentTruncated int
		if err := rs.Scan(&file.Path, &file.Language, &file.Hash, &file.Size, &modified, &indexed, &file.Content, &contentTruncated); err != nil {
			return apperror.StoreCorrupted(err)
		}
		file.ContentTruncated = contentTruncated != 0
		file.ModifiedAt, _ = time.Parse(time.RFC3339Nano, modified)
		file.IndexedAt, _ = time.Parse(time.RFC3339Nano, indexed)
		v.files = append(v.files, file)
	}
	return rs.Err()
}

func (v *readView) loadIdentities(ctx context.Context, tx *sql.Tx) error {
	rs, err := tx.QueryContext(ctx, "SELECT symbol_id, language, kind, name, qualified_name, COALESCE(parent_symbol_id,''), signature_fingerprint FROM symbol_identities")
	if err != nil {
		return apperror.StoreCorrupted(err)
	}
	defer rs.Close()
	for rs.Next() {
		var id domain.SymbolIdentity
		var parent string
		if err := rs.Scan(&id.ID, &id.Language, &id.Kind, &id.Name, &id.QualifiedName, &parent, &id.SignatureFingerprint); err != nil {
			return apperror.StoreCorrupted(err)
		}
		id.ParentID = domain.SymbolID(parent)
		v.identities[string(id.ID)] = id
	}
	return rs.Err()
}

func (v *readView) loadOccurrences(ctx context.Context, tx *sql.Tx) error {
	rs, err := tx.QueryContext(ctx, `SELECT occurrence_id, symbol_id, file_path, start_line, start_col, end_line, end_col,
		signature, code, doc_comment, summary, body_hash, file_hash FROM symbol_occurrences ORDER BY file_path, start_line, start_col`)
	if err != nil {
		return apperror.StoreCorrupted(err)
	}
	defer rs.Close()
	for rs.Next() {
		var occ domain.SymbolOccurrence
		var symbolID string
		if err := rs.Scan(&occ.ID, &symbolID, &occ.Path,
			&occ.Range.Start.Line, &occ.Range.Start.Column, &occ.Range.End.Line, &occ.Range.End.Column,
			&occ.Signature, &occ.Code, &occ.DocComment, &occ.Summary, &occ.BodyHash, &occ.FileHash); err != nil {
			return apperror.StoreCorrupted(err)
		}
		occ.SymbolID = domain.SymbolID(symbolID)
		idStr := string(occ.ID)
		v.occurrences[idStr] = occ
		v.occByFile[occ.Path] = append(v.occByFile[occ.Path], idStr)
		v.occBySymbol[symbolID] = append(v.occBySymbol[symbolID], idStr)
	}
	return rs.Err()
}

func (v *readView) loadEdges(ctx context.Context, tx *sql.Tx) error {
	rs, err := tx.QueryContext(ctx, `SELECT r.kind, r.subject_symbol_id, COALESCE(r.object_symbol_id,''), COALESCE(r.external_key,''),
		r.ranking_confidence, COALESCE(e.file_path,''), COALESCE(e.start_line,0)
		FROM relations r JOIN relation_evidence e ON e.relation_id = r.relation_id
		ORDER BY r.canonical_key, e.evidence_id`)
	if err != nil {
		return apperror.StoreCorrupted(err)
	}
	defer rs.Close()
	for rs.Next() {
		var edge domain.Edge
		var object, external string
		if err := rs.Scan(&edge.Type, &edge.FromSymbolID, &object, &external, &edge.Confidence, &edge.Path, &edge.Line); err != nil {
			return apperror.StoreCorrupted(err)
		}
		edge.ToSymbolID = object
		if object != "" {
			if identity, ok := v.identities[object]; ok {
				edge.ToName = identity.Name
			}
		} else {
			edge.ToName = external
		}
		v.edges = append(v.edges, edge)
	}
	return rs.Err()
}

// resolveEdges resolves an edge's target name to a symbol handle, preferring the
// same path/package, mirroring the in-memory store so graph traversal reaches the
// same neighbors. It runs after the flat symbols are built.
func (v *readView) resolveEdges() {
	edgeresolve.Resolve(v.flat, v.edges)
}

func (v *readView) buildAdjacency() {
	v.adjacency = make(map[string][]int)
	for i := range v.edges {
		edge := v.edges[i]
		v.adjacency[edge.FromSymbolID] = append(v.adjacency[edge.FromSymbolID], i)
		if edge.ToSymbolID != "" && edge.ToSymbolID != edge.FromSymbolID {
			v.adjacency[edge.ToSymbolID] = append(v.adjacency[edge.ToSymbolID], i)
		}
	}
}

// buildFlat builds one flat domain.Symbol per identity, using the representative
// (first by file/line/column) occurrence.
func (v *readView) buildFlat() {
	for symbolID, occIDs := range v.occBySymbol {
		identity, ok := v.identities[symbolID]
		if !ok || len(occIDs) == 0 {
			continue
		}
		occ := v.occurrences[occIDs[0]] // occBySymbol preserves the file/line ordering
		v.flat[symbolID] = domain.Symbol{
			ID: symbolID, OccurrenceID: string(occ.ID), Path: occ.Path,
			Name: identity.Name, QualifiedName: identity.QualifiedName, Kind: identity.Kind,
			Language: identity.Language, Range: occ.Range,
			Signature: occ.Signature, Code: occ.Code, DocComment: occ.DocComment, Summary: occ.Summary,
		}
		v.flatOrder = append(v.flatOrder, symbolID)
	}
	sort.Strings(v.flatOrder)
	for _, handle := range v.flatOrder {
		symbol := v.flat[handle]
		for _, name := range storederive.SymbolLookupNames(symbol) {
			v.byName[name] = append(v.byName[name], handle)
		}
		v.boostFields[handle] = sqliteLexicalBoostFields{
			name:      strings.ToLower(symbol.Name),
			qualified: strings.ToLower(symbol.QualifiedName),
		}
	}
}

// ApplyOverlay replaces one file inside this already-materialized, exclusive
// ReadView. It mirrors commit derivation without SQL or persistence and rebuilds
// only in-memory projections; the underlying store remains untouched.
func (v *readView) ApplyOverlay(parsed domain.ParsedFile) error {
	if v.closed {
		return errors.New("sqlite store: read view is closed")
	}
	path := parsed.File.Path
	if path == "" {
		return errors.New("sqlite store: overlay path is empty")
	}

	files := v.files[:0]
	for _, file := range v.files {
		if file.Path != path {
			files = append(files, file)
		}
	}
	v.files = append(files, parsed.File)
	sort.Slice(v.files, func(i, j int) bool { return v.files[i].Path < v.files[j].Path })

	for occurrenceID, occurrence := range v.occurrences {
		if occurrence.Path == path {
			delete(v.occurrences, occurrenceID)
		}
	}
	edges := v.edges[:0]
	for _, edge := range v.edges {
		if edge.Path != path {
			edges = append(edges, edge)
		}
	}
	v.edges = edges

	resolved, handleByLegacy := storederive.ResolveParsedFile(parsed)
	v.overlayIDs = make(map[string]struct{}, len(resolved))
	for _, item := range resolved {
		handle := storederive.SymbolHandle(item)
		v.overlayIDs[handle] = struct{}{}
		identity := item.Identity
		identity.ID = domain.SymbolID(handle)
		occurrence := item.Occurrence
		occurrence.SymbolID = domain.SymbolID(handle)
		v.identities[handle] = identity
		v.occurrences[string(occurrence.ID)] = occurrence
	}

	// Identities whose last occurrence belonged to the replaced file disappear.
	usedIdentities := make(map[string]struct{}, len(v.occurrences))
	for _, occurrence := range v.occurrences {
		usedIdentities[string(occurrence.SymbolID)] = struct{}{}
	}
	for symbolID := range v.identities {
		if _, used := usedIdentities[symbolID]; !used {
			delete(v.identities, symbolID)
		}
	}
	for symbolID, identity := range v.identities {
		if identity.ParentID == "" {
			continue
		}
		if _, parentExists := v.identities[string(identity.ParentID)]; !parentExists {
			identity.ParentID = ""
			v.identities[symbolID] = identity
		}
	}
	validEdges := v.edges[:0]
	for _, edge := range v.edges {
		if _, subjectExists := v.identities[edge.FromSymbolID]; !subjectExists {
			continue
		}
		if edge.ToSymbolID != "" {
			if _, objectExists := v.identities[edge.ToSymbolID]; !objectExists {
				continue
			}
		}
		validEdges = append(validEdges, edge)
	}
	v.edges = validEdges

	for _, raw := range parsed.Edges {
		edge := storederive.RemapEdge(raw, handleByLegacy)
		if _, exists := v.identities[edge.FromSymbolID]; !exists || edge.Type == "" {
			continue
		}
		if edge.ToSymbolID != "" {
			if _, exists := v.identities[edge.ToSymbolID]; !exists {
				if edge.ToName == "" {
					continue
				}
				edge.ToSymbolID = ""
			}
		} else if edge.ToName == "" {
			continue
		}
		if edge.Confidence < 0 {
			edge.Confidence = 0
		} else if edge.Confidence > 1 {
			edge.Confidence = 1
		}
		v.edges = append(v.edges, edge)
	}

	v.rebuildProjections()
	return nil
}

func (v *readView) rebuildProjections() {
	v.occBySymbol = map[string][]string{}
	v.occByFile = map[string][]string{}
	occurrenceIDs := make([]string, 0, len(v.occurrences))
	for occurrenceID := range v.occurrences {
		occurrenceIDs = append(occurrenceIDs, occurrenceID)
	}
	sort.Slice(occurrenceIDs, func(i, j int) bool {
		left, right := v.occurrences[occurrenceIDs[i]], v.occurrences[occurrenceIDs[j]]
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Range.Start.Line != right.Range.Start.Line {
			return left.Range.Start.Line < right.Range.Start.Line
		}
		if left.Range.Start.Column != right.Range.Start.Column {
			return left.Range.Start.Column < right.Range.Start.Column
		}
		return occurrenceIDs[i] < occurrenceIDs[j]
	})
	for _, occurrenceID := range occurrenceIDs {
		occurrence := v.occurrences[occurrenceID]
		v.occByFile[occurrence.Path] = append(v.occByFile[occurrence.Path], occurrenceID)
		v.occBySymbol[string(occurrence.SymbolID)] = append(v.occBySymbol[string(occurrence.SymbolID)], occurrenceID)
	}
	v.flat = map[string]domain.Symbol{}
	v.flatOrder = nil
	v.byName = map[string][]string{}
	v.boostFields = map[string]sqliteLexicalBoostFields{}
	v.buildFlat()
	v.resolveEdges()
	v.buildAdjacency()
}

// --- ReadView methods ---

func (v *readView) Metadata() domain.SnapshotMetadata { return v.metadata }

func (v *readView) Files() []domain.File {
	out := make([]domain.File, len(v.files))
	copy(out, v.files)
	return out
}

func (v *readView) AllSymbols() []domain.Symbol {
	if v.closed {
		return nil
	}
	out := make([]domain.Symbol, 0, len(v.flatOrder))
	for _, id := range v.flatOrder {
		out = append(out, v.flat[id])
	}
	return out
}

func (v *readView) GetSymbol(id string) (domain.Symbol, bool) {
	if v.closed {
		return domain.Symbol{}, false
	}
	symbol, ok := v.flat[id]
	return symbol, ok
}

func (v *readView) GetSymbolByOccurrence(occurrenceID string) (domain.Symbol, bool) {
	if v.closed {
		return domain.Symbol{}, false
	}
	occurrence, ok := v.occurrences[occurrenceID]
	if ok {
		handle := symbolHandleForOccurrence(occurrence)
		identity, found := v.identities[handle]
		if found {
			return domain.ResolvedSymbol{Identity: identity, Occurrence: occurrence}.ToSymbol(), true
		}
		if symbol, found := v.flat[handle]; found {
			return symbol, true
		}
		return domain.Symbol{}, false
	}
	// Preserve the previous service behavior where a symbol handle was accepted
	// in the occurrence field as a fallback.
	symbol, ok := v.flat[occurrenceID]
	return symbol, ok
}

func (v *readView) SymbolsByName(name string) []domain.Symbol {
	if v.closed {
		return nil
	}
	handles := v.byName[name]
	symbols := make([]domain.Symbol, 0, len(handles))
	for _, handle := range handles {
		if symbol, ok := v.flat[handle]; ok {
			symbols = append(symbols, symbol)
		}
	}
	return symbols
}

func (v *readView) SymbolsByPath(path string) []domain.Symbol {
	if v.closed {
		return nil
	}
	handles := make(map[string]struct{}, len(v.occByFile[path]))
	symbols := make([]domain.Symbol, 0, len(v.occByFile[path]))
	for _, occurrenceID := range v.occByFile[path] {
		occurrence := v.occurrences[occurrenceID]
		handle := symbolHandleForOccurrence(occurrence)
		if _, duplicate := handles[handle]; duplicate {
			continue
		}
		symbol, ok := v.flat[handle]
		if !ok {
			continue
		}
		handles[handle] = struct{}{}
		symbols = append(symbols, symbol)
	}
	sort.Slice(symbols, func(i, j int) bool { return symbols[i].ID < symbols[j].ID })
	return symbols
}

func symbolHandleForOccurrence(occurrence domain.SymbolOccurrence) string {
	if occurrence.SymbolID != "" {
		return string(occurrence.SymbolID)
	}
	return string(occurrence.ID)
}

func (v *readView) SymbolAt(path string, line, column int) (domain.ResolvedSymbol, bool) {
	if v.closed {
		return domain.ResolvedSymbol{}, false
	}
	best, found := domain.SymbolOccurrence{}, false
	for _, occID := range v.occByFile[path] {
		occ := v.occurrences[occID]
		if !containsPosition(occ.Range, line, column) {
			continue
		}
		if !found || tighter(occ.Range, best.Range) {
			best, found = occ, true
		}
	}
	if !found {
		return domain.ResolvedSymbol{}, false
	}
	identity := v.identities[string(best.SymbolID)]
	return domain.ResolvedSymbol{Identity: identity, Occurrence: best}, true
}

func (v *readView) ResolveSymbol(name, currentPath string) (domain.Symbol, bool) {
	if v.closed {
		return domain.Symbol{}, false
	}
	all := make([]domain.Symbol, 0, len(v.flatOrder))
	for _, id := range v.flatOrder {
		all = append(all, v.flat[id])
	}
	return storederive.ResolveSymbol(all, name, currentPath)
}

func (v *readView) OccurrencesForSymbol(id domain.SymbolID) []domain.SymbolOccurrence {
	if v.closed {
		return nil
	}
	occIDs := v.occBySymbol[string(id)]
	out := make([]domain.SymbolOccurrence, 0, len(occIDs))
	for _, occID := range occIDs {
		out = append(out, v.occurrences[occID])
	}
	return out
}

func (v *readView) AllEdges() []domain.Edge {
	if v.closed {
		return nil
	}
	out := make([]domain.Edge, len(v.edges))
	copy(out, v.edges)
	return out
}

func (v *readView) EdgesForSymbol(id string) []domain.Edge {
	if v.closed {
		return nil
	}
	indexes := v.adjacency[id]
	edges := make([]domain.Edge, 0, len(indexes))
	for _, index := range indexes {
		edges = append(edges, v.edges[index])
	}
	return edges
}

// Graph walks the relation neighborhood from the seeds up to depth/maxNodes,
// returning the reached symbols and the edges among them.
func (v *readView) Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge) {
	if v.closed {
		return nil, nil
	}
	visited, _ := storederive.TraverseGraph(seedIDs, depth, maxNodes, func(id string) bool {
		_, exists := v.flat[id]
		return exists
	}, v.adjacency, v.edges)
	symbolsOut := make([]domain.Symbol, 0, len(visited))
	handles := make([]string, 0, len(visited))
	for handle := range visited {
		handles = append(handles, handle)
	}
	sort.Strings(handles)
	for _, handle := range handles {
		symbolsOut = append(symbolsOut, v.flat[handle])
	}
	return symbolsOut, storederive.GraphEdges(v.edges, v.adjacency, visited)
}

// Search runs the FTS5 lexical query (bm25 column weights) and applies the same
// additive name/qualified boosts as the in-memory ranking, then builds SearchHits
// from the materialized flat symbols. An empty/short query (no usable tokens) is a
// genuine empty result, not a faked one.
func (v *readView) Search(query string, limit int) ([]domain.SearchHit, error) {
	if v.closed {
		return nil, errors.New("sqlite store: read view is closed")
	}
	if limit <= 0 {
		limit = 20
	}
	matchExpr := lexical.MatchExpression(query)
	if matchExpr == "" {
		return nil, nil
	}
	// Over-fetch so the additive boosts can reorder beyond the raw bm25 top-N.
	hits, err := queryFTSRanked(v.searchCtx, v.reader, matchExpr, limit*4)
	if err != nil {
		return nil, apperror.StoreCorrupted(err)
	}
	candidates := make([]ftsSymbolHit, 0, len(hits))
	queryTokens := lexical.BuildQuery(query)
	seen := make(map[string]struct{}, len(hits)+len(v.overlayIDs))
	for _, hit := range hits {
		symbol, ok := v.flat[hit.symbolID]
		if !ok || !symbolMatchesTokens(symbol, queryTokens) {
			continue
		}
		candidates = append(candidates, ftsSymbolHit{symbol: symbol, base: hit.base})
		seen[hit.symbolID] = struct{}{}
	}
	for symbolID := range v.overlayIDs {
		if _, exists := seen[symbolID]; exists {
			continue
		}
		symbol, ok := v.flat[symbolID]
		if !ok || !symbolMatchesTokens(symbol, queryTokens) {
			continue
		}
		candidates = append(candidates, ftsSymbolHit{symbol: symbol, base: 1})
	}
	return rankFTSSymbols(query, limit, v.metadata.ID, candidates), nil
}

func symbolMatchesTokens(symbol domain.Symbol, queryTokens []string) bool {
	document := lexical.BuildDocument(symbol)
	fields := [][]string{document.Name, document.Qualified, document.Kind, document.Path, document.Signature, document.Summary, document.Code}
	for _, queryToken := range queryTokens {
		for _, field := range fields {
			for _, token := range field {
				if token == queryToken {
					return true
				}
			}
		}
	}
	return false
}

// Close releases the materialized snapshot and is idempotent.
func (v *readView) Close() error {
	v.closed = true
	v.flat = nil
	v.byName = nil
	v.boostFields = nil
	v.occurrences = nil
	v.edges = nil
	v.overlayIDs = nil
	return nil
}

func containsPosition(r domain.Range, line, column int) bool {
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

func tighter(candidate, current domain.Range) bool {
	return span(candidate) < span(current)
}

func span(r domain.Range) int {
	return (r.End.Line-r.Start.Line)*100000 + (r.End.Column - r.Start.Column)
}
