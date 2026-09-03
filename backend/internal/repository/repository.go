// Package repository defines the backend-neutral store port used by runtime
// services. JSON and SQLite are adapters behind this seam; production wiring
// chooses SQLite, while JSON remains available for migration/contract tests.
package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	jsonstore "github.com/ThalesMMS/CodeAtlas/internal/store"
	sqlitestore "github.com/ThalesMMS/CodeAtlas/internal/store/sqlite"
	"github.com/ThalesMMS/CodeAtlas/internal/storederive"
)

// ErrDocumentBaseStale means an overlay cannot be safely rebased onto the
// current persisted snapshot because the same file changed since it was opened.
var ErrDocumentBaseStale = errors.New("repository: overlay base is stale")

// ErrVersionConflict reports optimistic concurrency failure across adapters.
var ErrVersionConflict = errors.New("STORE_VERSION_CONFLICT")

// ErrStoreUnavailable reports access to a repository reference before startup
// has installed the concrete backend. Functional HTTP routes are readiness-gated,
// so only diagnostics should observe zero-value reads from this state.
var ErrStoreUnavailable = errors.New("repository: store backend is not available")

// OverlayContext identifies one ephemeral, unsaved overlay being composited over
// a persisted snapshot.
type OverlayContext struct {
	DocumentID      string
	Path            string
	Version         int64
	ContentHash     string
	BaseContentHash string
	BaseSnapshotID  domain.SnapshotID
}

// ReadView is a pinned, snapshot-coherent view over repository facts.
type ReadView interface {
	Metadata() domain.SnapshotMetadata
	Files() []domain.File
	Search(query string, limit int) ([]domain.SearchHit, error)
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
	Close() error
}

// CompositeReadView is an ephemeral read view containing one unsaved overlay.
type CompositeReadView interface {
	ReadView
	ViewHash() string
	Overlay() OverlayContext
	Rebased() bool
}

// PreparedCommit is an opaque prepared repository mutation.
type PreparedCommit interface {
	IsNoop() bool
	ExpectedVersion() uint64
	NextVersion() uint64
}

// CommitResult reports a published repository mutation.
type CommitResult struct {
	Revision   domain.Revision
	SnapshotID domain.SnapshotID
	NoOp       bool
}

// Store is the runtime repository port.
type Store interface {
	SnapshotMetadataContext(ctx context.Context) (domain.SnapshotMetadata, error)
	SnapshotContext(ctx context.Context) (ReadView, error)
	PrepareContext(ctx context.Context, change *changeset.ChangeSet) (PreparedCommit, error)
	PrepareEmbeddingRebuildContext(ctx context.Context, vectors map[string][]float64, metadata domain.EmbeddingIndexMetadata) (PreparedCommit, error)
	CommitPreparedContext(ctx context.Context, prepared PreparedCommit) (CommitResult, error)
	FileHashContext(ctx context.Context, path string) (string, bool, error)
	SearchContext(ctx context.Context, query string, limit int) ([]domain.SearchHit, error)
	EmbeddingMetadataContext(ctx context.Context) (domain.EmbeddingIndexMetadata, error)
	CompositeViewContext(ctx context.Context, ephemeral domain.ParsedFile, overlay OverlayContext) (CompositeReadView, error)
	Version() uint64
	Revision() domain.Revision
	SnapshotID() domain.SnapshotID
	SnapshotMetadata() domain.SnapshotMetadata
	Snapshot() ReadView
	Prepare(change *changeset.ChangeSet) (PreparedCommit, error)
	PrepareEmbeddingRebuild(vectors map[string][]float64, metadata domain.EmbeddingIndexMetadata) (PreparedCommit, error)
	CommitPrepared(prepared PreparedCommit) (CommitResult, error)
	ReplaceFile(parsed domain.ParsedFile) error
	FileHash(path string) (string, bool)
	KnownPaths() []string
	FileTree() domain.FileTreeNode
	Search(query string, limit int) ([]domain.SearchHit, error)
	Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge)
	GetSymbol(id string) (domain.Symbol, bool)
	AllSymbols() []domain.Symbol
	AllEdges() []domain.Edge
	Embeddings() map[string][]float64
	EmbeddingMetadata() domain.EmbeddingIndexMetadata
	EmbeddingCount() int
	WikiPages() []domain.WikiPage
	ReplaceWikiPages(pages []domain.WikiPage) error
	PublishCodemap(codemap domain.Codemap) (domain.ArtifactMetadata, error)
	CodemapByArtifactIDContext(ctx context.Context, id domain.ArtifactID) (domain.Codemap, error)
	ListCodemapsContext(ctx context.Context) ([]domain.CodemapSummary, error)
	Counts() (files, symbols, edges, wiki int, indexedAt time.Time)
	CompositeView(ephemeral domain.ParsedFile, overlay OverlayContext) (CompositeReadView, error)
	Validate(ctx context.Context) error
	Close() error
}

// DenseSearcher is the optional optimized dense-retrieval capability. Runtime
// adapters implement it without materializing the complete embedding index or
// rebuilding a symbol map for every query.
type DenseSearcher interface {
	SearchDense(ctx context.Context, queryVector []float64, limit int) ([]domain.SearchHit, error)
}

// FTSRebuilder is the optional maintenance capability for replacing the entire
// derived lexical index without changing the structural snapshot.
type FTSRebuilder interface {
	RebuildFTSContext(ctx context.Context) error
}

// Ref is a concurrency-safe delegating Store. It lets the HTTP surface and
// services be wired before startup migration opens the concrete SQLite adapter.
type Ref struct {
	mu    sync.RWMutex
	inner Store
}

// NewRef returns an initially-empty delegating store reference.
func NewRef() *Ref { return &Ref{} }

// Set installs the concrete backend. A nil store clears the reference.
func (r *Ref) Set(store Store) {
	r.mu.Lock()
	r.inner = store
	r.mu.Unlock()
}

// Inner returns the currently installed backend, if any.
func (r *Ref) Inner() (Store, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.inner == nil {
		return nil, false
	}
	return r.inner, true
}

func (r *Ref) require() (Store, error) {
	store, ok := r.Inner()
	if !ok {
		return nil, ErrStoreUnavailable
	}
	return store, nil
}

func (r *Ref) snapshotOrEmpty() ReadView {
	store, ok := r.Inner()
	if !ok {
		return emptyReadView{}
	}
	return store.Snapshot()
}

func (r *Ref) SnapshotMetadataContext(ctx context.Context) (domain.SnapshotMetadata, error) {
	store, err := r.require()
	if err != nil {
		return domain.SnapshotMetadata{}, err
	}
	return store.SnapshotMetadataContext(ctx)
}

func (r *Ref) SnapshotContext(ctx context.Context) (ReadView, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	return store.SnapshotContext(ctx)
}

func (r *Ref) PrepareContext(ctx context.Context, change *changeset.ChangeSet) (PreparedCommit, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	return store.PrepareContext(ctx, change)
}

func (r *Ref) PrepareEmbeddingRebuildContext(ctx context.Context, vectors map[string][]float64, metadata domain.EmbeddingIndexMetadata) (PreparedCommit, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	return store.PrepareEmbeddingRebuildContext(ctx, vectors, metadata)
}

func (r *Ref) CommitPreparedContext(ctx context.Context, prepared PreparedCommit) (CommitResult, error) {
	store, err := r.require()
	if err != nil {
		return CommitResult{}, err
	}
	return store.CommitPreparedContext(ctx, prepared)
}

func (r *Ref) FileHashContext(ctx context.Context, path string) (string, bool, error) {
	store, err := r.require()
	if err != nil {
		return "", false, err
	}
	return store.FileHashContext(ctx, path)
}

func (r *Ref) SearchContext(ctx context.Context, query string, limit int) ([]domain.SearchHit, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	return store.SearchContext(ctx, query, limit)
}

func (r *Ref) RebuildFTSContext(ctx context.Context) error {
	store, err := r.require()
	if err != nil {
		return err
	}
	rebuilder, ok := store.(FTSRebuilder)
	if !ok {
		return errors.New("repository: backend does not support FTS rebuild")
	}
	return rebuilder.RebuildFTSContext(ctx)
}

func (r *Ref) EmbeddingMetadataContext(ctx context.Context) (domain.EmbeddingIndexMetadata, error) {
	store, err := r.require()
	if err != nil {
		return domain.EmbeddingIndexMetadata{}, err
	}
	return store.EmbeddingMetadataContext(ctx)
}

func (r *Ref) CompositeViewContext(ctx context.Context, ephemeral domain.ParsedFile, overlay OverlayContext) (CompositeReadView, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	return store.CompositeViewContext(ctx, ephemeral, overlay)
}

func (r *Ref) Version() uint64 {
	if store, ok := r.Inner(); ok {
		return store.Version()
	}
	return 0
}
func (r *Ref) Revision() domain.Revision {
	if store, ok := r.Inner(); ok {
		return store.Revision()
	}
	return 0
}
func (r *Ref) SnapshotID() domain.SnapshotID {
	if store, ok := r.Inner(); ok {
		return store.SnapshotID()
	}
	return ""
}
func (r *Ref) SnapshotMetadata() domain.SnapshotMetadata {
	if store, ok := r.Inner(); ok {
		return store.SnapshotMetadata()
	}
	return domain.SnapshotMetadata{}
}
func (r *Ref) Snapshot() ReadView { return r.snapshotOrEmpty() }
func (r *Ref) Prepare(change *changeset.ChangeSet) (PreparedCommit, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	return store.Prepare(change)
}
func (r *Ref) PrepareEmbeddingRebuild(vectors map[string][]float64, metadata domain.EmbeddingIndexMetadata) (PreparedCommit, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	return store.PrepareEmbeddingRebuild(vectors, metadata)
}
func (r *Ref) CommitPrepared(prepared PreparedCommit) (CommitResult, error) {
	store, err := r.require()
	if err != nil {
		return CommitResult{}, err
	}
	return store.CommitPrepared(prepared)
}
func (r *Ref) ReplaceFile(parsed domain.ParsedFile) error {
	store, err := r.require()
	if err != nil {
		return err
	}
	return store.ReplaceFile(parsed)
}
func (r *Ref) FileHash(path string) (string, bool) {
	if store, ok := r.Inner(); ok {
		return store.FileHash(path)
	}
	return "", false
}
func (r *Ref) KnownPaths() []string {
	if store, ok := r.Inner(); ok {
		return store.KnownPaths()
	}
	return nil
}
func (r *Ref) FileTree() domain.FileTreeNode {
	if store, ok := r.Inner(); ok {
		return store.FileTree()
	}
	return domain.FileTreeNode{Name: "workspace", Type: "directory"}
}
func (r *Ref) Search(query string, limit int) ([]domain.SearchHit, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	return store.Search(query, limit)
}
func (r *Ref) SearchDense(ctx context.Context, queryVector []float64, limit int) ([]domain.SearchHit, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	searcher, ok := store.(DenseSearcher)
	if !ok {
		return nil, errors.New("repository: backend does not support optimized dense search")
	}
	return searcher.SearchDense(ctx, queryVector, limit)
}
func (r *Ref) Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge) {
	if store, ok := r.Inner(); ok {
		return store.Graph(seedIDs, depth, maxNodes)
	}
	return nil, nil
}
func (r *Ref) GetSymbol(id string) (domain.Symbol, bool) {
	if store, ok := r.Inner(); ok {
		return store.GetSymbol(id)
	}
	return domain.Symbol{}, false
}
func (r *Ref) AllSymbols() []domain.Symbol {
	if store, ok := r.Inner(); ok {
		return store.AllSymbols()
	}
	return nil
}
func (r *Ref) AllEdges() []domain.Edge {
	if store, ok := r.Inner(); ok {
		return store.AllEdges()
	}
	return nil
}
func (r *Ref) Embeddings() map[string][]float64 {
	if store, ok := r.Inner(); ok {
		return store.Embeddings()
	}
	return nil
}
func (r *Ref) EmbeddingMetadata() domain.EmbeddingIndexMetadata {
	if store, ok := r.Inner(); ok {
		return store.EmbeddingMetadata()
	}
	return domain.EmbeddingIndexMetadata{}
}
func (r *Ref) EmbeddingCount() int {
	if store, ok := r.Inner(); ok {
		return store.EmbeddingCount()
	}
	return 0
}
func (r *Ref) WikiPages() []domain.WikiPage {
	if store, ok := r.Inner(); ok {
		return store.WikiPages()
	}
	return nil
}
func (r *Ref) ReplaceWikiPages(pages []domain.WikiPage) error {
	store, err := r.require()
	if err != nil {
		return err
	}
	return store.ReplaceWikiPages(pages)
}
func (r *Ref) PublishCodemap(codemap domain.Codemap) (domain.ArtifactMetadata, error) {
	store, err := r.require()
	if err != nil {
		return domain.ArtifactMetadata{}, err
	}
	return store.PublishCodemap(codemap)
}
func (r *Ref) CodemapByArtifactIDContext(ctx context.Context, id domain.ArtifactID) (domain.Codemap, error) {
	store, err := r.require()
	if err != nil {
		return domain.Codemap{}, err
	}
	return store.CodemapByArtifactIDContext(ctx, id)
}
func (r *Ref) ListCodemapsContext(ctx context.Context) ([]domain.CodemapSummary, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	return store.ListCodemapsContext(ctx)
}
func (r *Ref) Counts() (files, symbols, edges, wiki int, indexedAt time.Time) {
	if store, ok := r.Inner(); ok {
		return store.Counts()
	}
	return 0, 0, 0, 0, time.Time{}
}
func (r *Ref) CompositeView(ephemeral domain.ParsedFile, overlay OverlayContext) (CompositeReadView, error) {
	store, err := r.require()
	if err != nil {
		return nil, err
	}
	return store.CompositeView(ephemeral, overlay)
}
func (r *Ref) Validate(ctx context.Context) error {
	store, err := r.require()
	if err != nil {
		return err
	}
	return store.Validate(ctx)
}
func (r *Ref) Close() error {
	store, ok := r.Inner()
	if !ok {
		return nil
	}
	return store.Close()
}

// SQLiteConfig configures the SQLite adapter.
type SQLiteConfig struct {
	WorkspaceRoot string
	Path          string
	BusyTimeout   time.Duration
	ReaderPool    int
}

// OpenJSON opens the legacy JSON-backed adapter.
func OpenJSON(path string) (Store, error) {
	inner, err := jsonstore.Open(path)
	if err != nil {
		return nil, err
	}
	return &jsonAdapter{inner: inner}, nil
}

// OpenSQLite opens the SQLite-backed adapter.
func OpenSQLite(ctx context.Context, config SQLiteConfig) (Store, error) {
	inner, err := sqlitestore.OpenStore(ctx, sqlitestore.Config{
		WorkspaceRoot: config.WorkspaceRoot,
		Path:          config.Path,
		BusyTimeout:   config.BusyTimeout,
		ReaderPool:    config.ReaderPool,
	})
	if err != nil {
		return nil, err
	}
	return &sqliteAdapter{inner: inner}, nil
}

type jsonAdapter struct {
	inner *jsonstore.Store
}

func (a *jsonAdapter) SnapshotMetadataContext(ctx context.Context) (domain.SnapshotMetadata, error) {
	if err := ctx.Err(); err != nil {
		return domain.SnapshotMetadata{}, err
	}
	return a.inner.SnapshotMetadata(), nil
}
func (a *jsonAdapter) SnapshotContext(ctx context.Context) (ReadView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return jsonReadView{inner: a.inner.Snapshot()}, nil
}
func (a *jsonAdapter) PrepareContext(ctx context.Context, change *changeset.ChangeSet) (PreparedCommit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.Prepare(change)
}
func (a *jsonAdapter) PrepareEmbeddingRebuildContext(ctx context.Context, vectors map[string][]float64, metadata domain.EmbeddingIndexMetadata) (PreparedCommit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.PrepareEmbeddingRebuild(vectors, metadata)
}
func (a *jsonAdapter) CommitPreparedContext(ctx context.Context, prepared PreparedCommit) (CommitResult, error) {
	if err := ctx.Err(); err != nil {
		return CommitResult{}, err
	}
	return a.CommitPrepared(prepared)
}
func (a *jsonAdapter) FileHashContext(ctx context.Context, path string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	hash, found := a.inner.FileHash(path)
	return hash, found, nil
}
func (a *jsonAdapter) SearchContext(ctx context.Context, query string, limit int) ([]domain.SearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.Search(query, limit)
}
func (a *jsonAdapter) RebuildFTSContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.inner.RebuildLexical()
	return nil
}
func (a *jsonAdapter) EmbeddingMetadataContext(ctx context.Context) (domain.EmbeddingIndexMetadata, error) {
	if err := ctx.Err(); err != nil {
		return domain.EmbeddingIndexMetadata{}, err
	}
	return a.inner.EmbeddingMetadata(), nil
}
func (a *jsonAdapter) CompositeViewContext(ctx context.Context, ephemeral domain.ParsedFile, overlay OverlayContext) (CompositeReadView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.CompositeView(ephemeral, overlay)
}

func (a *jsonAdapter) Version() uint64                           { return a.inner.Version() }
func (a *jsonAdapter) Revision() domain.Revision                 { return a.inner.Revision() }
func (a *jsonAdapter) SnapshotID() domain.SnapshotID             { return a.inner.SnapshotID() }
func (a *jsonAdapter) SnapshotMetadata() domain.SnapshotMetadata { return a.inner.SnapshotMetadata() }
func (a *jsonAdapter) FileHash(path string) (string, bool)       { return a.inner.FileHash(path) }
func (a *jsonAdapter) KnownPaths() []string                      { return a.inner.KnownPaths() }
func (a *jsonAdapter) FileTree() domain.FileTreeNode             { return a.inner.FileTree() }
func (a *jsonAdapter) Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge) {
	return a.inner.Graph(seedIDs, depth, maxNodes)
}
func (a *jsonAdapter) GetSymbol(id string) (domain.Symbol, bool) { return a.inner.GetSymbol(id) }
func (a *jsonAdapter) AllSymbols() []domain.Symbol               { return a.inner.AllSymbols() }
func (a *jsonAdapter) AllEdges() []domain.Edge                   { return a.inner.AllEdges() }
func (a *jsonAdapter) Embeddings() map[string][]float64          { return a.inner.Embeddings() }
func (a *jsonAdapter) EmbeddingMetadata() domain.EmbeddingIndexMetadata {
	return a.inner.EmbeddingMetadata()
}
func (a *jsonAdapter) EmbeddingCount() int          { return a.inner.EmbeddingCount() }
func (a *jsonAdapter) WikiPages() []domain.WikiPage { return a.inner.WikiPages() }
func (a *jsonAdapter) ReplaceWikiPages(pages []domain.WikiPage) error {
	a.inner.ReplaceWikiPages(pages)
	return a.inner.Persist()
}
func (a *jsonAdapter) PublishCodemap(codemap domain.Codemap) (domain.ArtifactMetadata, error) {
	return domain.ArtifactMetadata{}, errors.New("repository: JSON backend does not support artifact persistence")
}
func (a *jsonAdapter) CodemapByArtifactIDContext(context.Context, domain.ArtifactID) (domain.Codemap, error) {
	return domain.Codemap{}, apperror.ArtifactNotFound()
}
func (a *jsonAdapter) ListCodemapsContext(context.Context) ([]domain.CodemapSummary, error) {
	// The JSON backend has no artifact persistence, so the saved list is empty.
	return []domain.CodemapSummary{}, nil
}
func (a *jsonAdapter) Counts() (int, int, int, int, time.Time) {
	return a.inner.Counts()
}
func (a *jsonAdapter) Snapshot() ReadView { return jsonReadView{inner: a.inner.Snapshot()} }
func (a *jsonAdapter) Search(query string, limit int) ([]domain.SearchHit, error) {
	return a.inner.Search(query, limit), nil
}
func (a *jsonAdapter) SearchDense(ctx context.Context, queryVector []float64, limit int) ([]domain.SearchHit, error) {
	return a.inner.SearchDense(ctx, queryVector, limit)
}
func (a *jsonAdapter) Prepare(change *changeset.ChangeSet) (PreparedCommit, error) {
	prepared, err := a.inner.Prepare(change)
	if err != nil {
		return nil, err
	}
	return &jsonPrepared{inner: prepared}, nil
}
func (a *jsonAdapter) PrepareEmbeddingRebuild(vectors map[string][]float64, metadata domain.EmbeddingIndexMetadata) (PreparedCommit, error) {
	prepared, err := a.inner.PrepareEmbeddingRebuild(vectors, metadata)
	if err != nil {
		return nil, err
	}
	return &jsonPrepared{inner: prepared}, nil
}
func (a *jsonAdapter) ReplaceFile(parsed domain.ParsedFile) error {
	a.inner.ReplaceFile(parsed)
	return a.inner.Persist()
}
func (a *jsonAdapter) CommitPrepared(prepared PreparedCommit) (CommitResult, error) {
	p, ok := prepared.(*jsonPrepared)
	if !ok {
		return CommitResult{}, errors.New("repository: prepared commit belongs to another backend")
	}
	if err := a.inner.CommitPrepared(p.inner); err != nil {
		if errors.Is(err, jsonstore.ErrVersionConflict) {
			return CommitResult{}, ErrVersionConflict
		}
		return CommitResult{}, err
	}
	return CommitResult{Revision: domain.Revision(p.NextVersion()), SnapshotID: a.inner.SnapshotID(), NoOp: p.IsNoop()}, nil
}
func (a *jsonAdapter) CompositeView(ephemeral domain.ParsedFile, overlay OverlayContext) (CompositeReadView, error) {
	view, err := a.inner.CompositeView(ephemeral, jsonstore.OverlayContext{
		DocumentID:      overlay.DocumentID,
		Path:            overlay.Path,
		Version:         overlay.Version,
		ContentHash:     overlay.ContentHash,
		BaseContentHash: overlay.BaseContentHash,
		BaseSnapshotID:  overlay.BaseSnapshotID,
	})
	if errors.Is(err, jsonstore.ErrDocumentBaseStale) {
		return nil, ErrDocumentBaseStale
	}
	if err != nil {
		return nil, err
	}
	return jsonCompositeView{jsonReadView: jsonReadView{inner: view}, inner: view}, nil
}
func (a *jsonAdapter) Validate(context.Context) error { return nil }
func (a *jsonAdapter) Close() error                   { return nil }

type jsonPrepared struct {
	inner *jsonstore.PreparedCommit
}

func (p *jsonPrepared) IsNoop() bool            { return p.inner.IsNoop() }
func (p *jsonPrepared) ExpectedVersion() uint64 { return p.inner.ExpectedVersion }
func (p *jsonPrepared) NextVersion() uint64     { return p.inner.NextVersion }

type jsonReadView struct {
	inner jsonstore.ReadView
}

func (v jsonReadView) Metadata() domain.SnapshotMetadata { return v.inner.Metadata() }
func (v jsonReadView) Files() []domain.File              { return v.inner.Files() }
func (v jsonReadView) Search(query string, limit int) ([]domain.SearchHit, error) {
	return v.inner.Search(query, limit), nil
}
func (v jsonReadView) AllSymbols() []domain.Symbol { return v.inner.AllSymbols() }
func (v jsonReadView) GetSymbol(id string) (domain.Symbol, bool) {
	return v.inner.GetSymbol(id)
}
func (v jsonReadView) GetSymbolByOccurrence(id string) (domain.Symbol, bool) {
	return v.inner.GetSymbolByOccurrence(id)
}
func (v jsonReadView) SymbolsByName(name string) []domain.Symbol {
	return v.inner.SymbolsByName(name)
}
func (v jsonReadView) SymbolsByPath(path string) []domain.Symbol {
	return v.inner.SymbolsByPath(path)
}
func (v jsonReadView) SymbolAt(path string, line, column int) (domain.ResolvedSymbol, bool) {
	return v.inner.SymbolAt(path, line, column)
}
func (v jsonReadView) ResolveSymbol(name, currentPath string) (domain.Symbol, bool) {
	return v.inner.ResolveSymbol(name, currentPath)
}
func (v jsonReadView) OccurrencesForSymbol(id domain.SymbolID) []domain.SymbolOccurrence {
	return v.inner.OccurrencesForSymbol(id)
}
func (v jsonReadView) Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge) {
	return v.inner.Graph(seedIDs, depth, maxNodes)
}
func (v jsonReadView) EdgesForSymbol(id string) []domain.Edge { return v.inner.EdgesForSymbol(id) }
func (v jsonReadView) AllEdges() []domain.Edge                { return v.inner.AllEdges() }
func (v jsonReadView) Embeddings() map[string][]float64       { return v.inner.Embeddings() }
func (v jsonReadView) Close() error                           { return nil }

type jsonCompositeView struct {
	jsonReadView
	inner jsonstore.CompositeReadView
}

func (v jsonCompositeView) ViewHash() string { return v.inner.ViewHash() }
func (v jsonCompositeView) Overlay() OverlayContext {
	overlay := v.inner.Overlay()
	return OverlayContext{
		DocumentID:      overlay.DocumentID,
		Path:            overlay.Path,
		Version:         overlay.Version,
		ContentHash:     overlay.ContentHash,
		BaseContentHash: overlay.BaseContentHash,
		BaseSnapshotID:  overlay.BaseSnapshotID,
	}
}
func (v jsonCompositeView) Rebased() bool { return v.inner.Rebased() }

type sqliteAdapter struct {
	inner *sqlitestore.Store
}

func (a *sqliteAdapter) SnapshotMetadataContext(ctx context.Context) (domain.SnapshotMetadata, error) {
	return a.inner.Metadata(ctx)
}

func (a *sqliteAdapter) SnapshotContext(ctx context.Context) (ReadView, error) {
	view, err := a.inner.OpenReadView(ctx)
	if err != nil {
		return nil, err
	}
	return sqliteReadView{inner: view}, nil
}

func (a *sqliteAdapter) metadata() domain.SnapshotMetadata {
	metadata, err := a.inner.Metadata(context.Background())
	if err != nil {
		return domain.SnapshotMetadata{}
	}
	return metadata
}
func (a *sqliteAdapter) Version() uint64                           { return uint64(a.metadata().Revision) }
func (a *sqliteAdapter) Revision() domain.Revision                 { return a.metadata().Revision }
func (a *sqliteAdapter) SnapshotID() domain.SnapshotID             { return a.metadata().ID }
func (a *sqliteAdapter) SnapshotMetadata() domain.SnapshotMetadata { return a.metadata() }
func (a *sqliteAdapter) Snapshot() ReadView {
	view, err := a.inner.OpenReadView(context.Background())
	if err != nil {
		return emptyReadView{metadata: a.metadata()}
	}
	return sqliteReadView{inner: view}
}
func (a *sqliteAdapter) Prepare(change *changeset.ChangeSet) (PreparedCommit, error) {
	return a.PrepareContext(context.Background(), change)
}
func (a *sqliteAdapter) PrepareContext(ctx context.Context, change *changeset.ChangeSet) (PreparedCommit, error) {
	if change == nil {
		return nil, errors.New("nil change set")
	}
	expected := change.ExpectedVersion()
	metadata, err := a.inner.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	if expected != uint64(metadata.Revision) {
		return nil, ErrVersionConflict
	}
	next := expected
	if !change.IsNoop() {
		next++
	}
	return &sqlitePrepared{change: change, expected: expected, next: next, noop: change.IsNoop()}, nil
}
func (a *sqliteAdapter) PrepareEmbeddingRebuild(vectors map[string][]float64, metadata domain.EmbeddingIndexMetadata) (PreparedCommit, error) {
	return a.PrepareEmbeddingRebuildContext(context.Background(), vectors, metadata)
}
func (a *sqliteAdapter) PrepareEmbeddingRebuildContext(ctx context.Context, vectors map[string][]float64, metadata domain.EmbeddingIndexMetadata) (PreparedCommit, error) {
	current, err := a.inner.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	return &sqliteEmbeddingPrepared{
		expected: uint64(current.Revision),
		next:     uint64(current.Revision) + 1,
		vectors:  cloneVectors(vectors),
		metadata: metadata,
	}, nil
}
func (a *sqliteAdapter) CommitPrepared(prepared PreparedCommit) (CommitResult, error) {
	return a.CommitPreparedContext(context.Background(), prepared)
}
func (a *sqliteAdapter) CommitPreparedContext(ctx context.Context, prepared PreparedCommit) (CommitResult, error) {
	switch p := prepared.(type) {
	case *sqlitePrepared:
		result, err := a.inner.Commit(ctx, p.change)
		if err != nil {
			if errors.Is(err, sqlitestore.ErrVersionConflict) {
				return CommitResult{}, ErrVersionConflict
			}
			return CommitResult{}, err
		}
		return CommitResult{Revision: result.Revision, SnapshotID: result.SnapshotID, NoOp: result.NoOp}, nil
	case *sqliteEmbeddingPrepared:
		snapshot := sqlitestore.EmbeddingSnapshot{
			Provider:        p.metadata.Provider,
			Model:           p.metadata.Model,
			TemplateVersion: p.metadata.TemplateVersion,
			Dimension:       p.metadata.Dimension,
			Vectors:         cloneVectors(p.vectors),
		}
		if err := a.inner.RebuildEmbeddings(ctx, domain.Revision(p.expected), snapshot); err != nil {
			return CommitResult{}, err
		}
		metadata, err := a.inner.Metadata(ctx)
		if err != nil {
			return CommitResult{}, err
		}
		return CommitResult{Revision: metadata.Revision, SnapshotID: metadata.ID}, nil
	default:
		return CommitResult{}, errors.New("repository: prepared commit belongs to another backend")
	}
}
func (a *sqliteAdapter) ReplaceFile(parsed domain.ParsedFile) error {
	change, err := changeset.NewBuilder().WithExpectedVersion(a.Version()).Upsert(parsed).Build(time.Now().UTC())
	if err != nil {
		return err
	}
	prepared, err := a.Prepare(change)
	if err != nil {
		return err
	}
	_, err = a.CommitPrepared(prepared)
	return err
}
func (a *sqliteAdapter) FileHash(path string) (string, bool) {
	hash, found, err := a.FileHashContext(context.Background(), path)
	if err != nil {
		slog.Error("repository sqlite file hash failed", "path", path, "error", err)
		return "", false
	}
	return hash, found
}
func (a *sqliteAdapter) FileHashContext(ctx context.Context, path string) (string, bool, error) {
	return a.inner.FileHash(ctx, path)
}
func (a *sqliteAdapter) KnownPaths() []string {
	paths, err := a.inner.KnownPaths(context.Background())
	if err != nil {
		slog.Error("repository sqlite known paths failed", "error", err)
		return nil
	}
	return paths
}
func (a *sqliteAdapter) FileTree() domain.FileTreeNode {
	view := a.Snapshot()
	defer view.Close()
	return storederive.FileTree(view.Files())
}
func (a *sqliteAdapter) Search(query string, limit int) ([]domain.SearchHit, error) {
	return a.SearchContext(context.Background(), query, limit)
}
func (a *sqliteAdapter) SearchContext(ctx context.Context, query string, limit int) ([]domain.SearchHit, error) {
	return a.inner.Search(ctx, query, limit)
}
func (a *sqliteAdapter) RebuildFTSContext(ctx context.Context) error {
	return a.inner.RebuildFTS(ctx)
}
func (a *sqliteAdapter) SearchDense(ctx context.Context, queryVector []float64, limit int) ([]domain.SearchHit, error) {
	view, err := a.inner.OpenReadView(ctx)
	if err != nil {
		return nil, err
	}
	defer view.Close()
	dense, err := view.SearchDense(ctx, queryVector, limit)
	if err != nil {
		return nil, err
	}
	hits := make([]domain.SearchHit, 0, len(dense))
	for _, hit := range dense {
		symbol, ok := view.GetSymbol(string(hit.SymbolID))
		if !ok {
			continue
		}
		snippet := symbol.DocComment
		if snippet == "" {
			snippet = symbol.Summary
		}
		hits = append(hits, domain.SearchHit{Symbol: symbol, Snippet: snippet, Score: hit.Similarity, Source: "dense"})
	}
	return hits, nil
}
func (a *sqliteAdapter) Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge) {
	view := a.Snapshot()
	defer view.Close()
	return view.Graph(seedIDs, depth, maxNodes)
}
func (a *sqliteAdapter) GetSymbol(id string) (domain.Symbol, bool) {
	view := a.Snapshot()
	defer view.Close()
	return view.GetSymbol(id)
}
func (a *sqliteAdapter) AllSymbols() []domain.Symbol {
	view := a.Snapshot()
	defer view.Close()
	return view.AllSymbols()
}
func (a *sqliteAdapter) AllEdges() []domain.Edge {
	view := a.Snapshot()
	defer view.Close()
	return view.AllEdges()
}
func (a *sqliteAdapter) Embeddings() map[string][]float64 {
	view := a.Snapshot()
	defer view.Close()
	return view.Embeddings()
}
func (a *sqliteAdapter) EmbeddingMetadata() domain.EmbeddingIndexMetadata {
	metadata, err := a.EmbeddingMetadataContext(context.Background())
	if err != nil {
		return domain.EmbeddingIndexMetadata{}
	}
	return metadata
}
func (a *sqliteAdapter) EmbeddingMetadataContext(ctx context.Context) (domain.EmbeddingIndexMetadata, error) {
	return a.inner.EmbeddingMetadata(ctx)
}
func (a *sqliteAdapter) EmbeddingCount() int {
	count, err := a.inner.EmbeddingCount(context.Background())
	if err != nil {
		return 0
	}
	return count
}
func (a *sqliteAdapter) WikiPages() []domain.WikiPage {
	heads, err := a.inner.Artifacts().ListHeads(context.Background(), sqlitestore.ArtifactFilter{Type: "deepwiki"})
	if err != nil {
		return nil
	}
	pages := make([]domain.WikiPage, 0, len(heads))
	for _, head := range heads {
		if head.CurrentID == "" || (head.Status != sqlitestore.StatusCurrent && head.Status != sqlitestore.StatusStale) {
			continue
		}
		artifact, err := a.inner.Artifacts().Get(context.Background(), head.CurrentID)
		if err != nil {
			continue
		}
		var page domain.WikiPage
		if err := json.Unmarshal([]byte(artifact.Payload), &page); err != nil {
			continue
		}
		if page.Markdown == "" {
			page.Markdown = artifact.RenderedMarkdown
		}
		pages = append(pages, page)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Slug < pages[j].Slug })
	return pages
}
func (a *sqliteAdapter) ReplaceWikiPages(pages []domain.WikiPage) error {
	artifacts := a.inner.Artifacts()
	snapshotID := string(a.SnapshotID())
	for _, page := range pages {
		payload, err := json.Marshal(page)
		if err != nil {
			return err
		}
		_, err = artifacts.Publish(context.Background(), sqlitestore.ArtifactCandidate{
			Type:                "deepwiki",
			Key:                 "repo/" + page.Slug,
			InputSnapshotID:     snapshotID,
			ContextPackHash:     page.ContextPackHash,
			PolicyVersion:       page.PolicyVersion,
			PromptVersion:       "deepwiki-v4",
			OutputSchemaVersion: page.OutputSchemaVersion,
			RendererVersion:     "server.v1",
			Provider:            page.Provider,
			Model:               "",
			Title:               page.Title,
			Payload:             string(payload),
			RenderedMarkdown:    page.Markdown,
			Metadata:            "{}",
			Dependencies: []sqlitestore.ArtifactDependency{
				{Kind: "snapshot", ContentHash: snapshotID},
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}
func (a *sqliteAdapter) PublishCodemap(codemap domain.Codemap) (domain.ArtifactMetadata, error) {
	payload, err := json.Marshal(codemap)
	if err != nil {
		return domain.ArtifactMetadata{}, err
	}
	published, err := a.inner.Artifacts().Publish(context.Background(), sqlitestore.ArtifactCandidate{
		Type:                "codemap",
		Key:                 codemap.Artifact.Key,
		InputSnapshotID:     string(codemap.SnapshotID),
		ContextPackHash:     codemap.ContextPackHash,
		PolicyVersion:       codemap.PolicyVersion,
		PromptVersion:       codemap.Artifact.PromptVersion,
		OutputSchemaVersion: codemap.OutputSchemaVersion,
		RendererVersion:     "server.v1",
		Provider:            codemap.Provider,
		Model:               codemap.Artifact.Model,
		Title:               codemap.Title,
		Payload:             string(payload),
		RenderedMarkdown:    codemap.Overview,
		Metadata:            "{}",
		Dependencies:        artifactDependencies(codemap.Artifact.Dependencies),
	})
	if err != nil {
		return domain.ArtifactMetadata{}, err
	}
	metadata := codemap.Artifact
	metadata.ID = domain.ArtifactID(published.ID)
	metadata.Revision = domain.ArtifactRevision(published.Revision)
	metadata.InputSnapshotID = domain.SnapshotID(published.InputSnapshotID)
	metadata.ContextPackHash = published.ContextPackHash
	metadata.Status = domain.ArtifactCurrent
	metadata.CreatedAt = published.CreatedAt
	return metadata, nil
}
func (a *sqliteAdapter) CodemapByArtifactIDContext(ctx context.Context, id domain.ArtifactID) (domain.Codemap, error) {
	published, err := a.inner.Artifacts().Get(ctx, sqlitestore.ArtifactID(id))
	if err != nil {
		return domain.Codemap{}, err
	}
	if published.Type != "codemap" {
		return domain.Codemap{}, apperror.ArtifactNotFound()
	}
	var codemap domain.Codemap
	if err := json.Unmarshal([]byte(published.Payload), &codemap); err != nil {
		return domain.Codemap{}, apperror.StoreCorrupted(err)
	}
	head, err := a.inner.Artifacts().GetHead(ctx, published.Type, published.Key)
	if err != nil {
		return domain.Codemap{}, err
	}
	status := domain.ArtifactCurrent
	var staleReasons []string
	if head.CurrentID != published.ID {
		status = domain.ArtifactStale
		staleReasons = []string{"superseded by a newer artifact"}
	} else if head.Status == sqlitestore.StatusStale {
		status = domain.ArtifactStale
		if head.StaleReason != "" {
			staleReasons = []string{head.StaleReason}
		}
	}
	codemap.SnapshotID = domain.SnapshotID(published.InputSnapshotID)
	codemap.ContextPackHash = published.ContextPackHash
	codemap.PolicyVersion = published.PolicyVersion
	codemap.OutputSchemaVersion = published.OutputSchemaVersion
	codemap.Provider = published.Provider
	codemap.Artifact = domain.ArtifactMetadata{
		ID:              domain.ArtifactID(published.ID),
		Type:            published.Type,
		Key:             published.Key,
		Revision:        domain.ArtifactRevision(published.Revision),
		InputSnapshotID: domain.SnapshotID(published.InputSnapshotID),
		ContextPackHash: published.ContextPackHash,
		PromptVersion:   published.PromptVersion,
		OutputSchema:    published.OutputSchemaVersion,
		Provider:        published.Provider,
		Model:           published.Model,
		Status:          status,
		Dependencies:    domainArtifactDependencies(published.Dependencies),
		StaleReasons:    staleReasons,
		CreatedAt:       published.CreatedAt,
	}
	return codemap, nil
}

// ListCodemapsContext lists the published Codemap heads as lightweight
// summaries, newest first. Query and graph counts come from a partial payload
// decode; a head whose current version row is missing is skipped rather than
// failing the whole listing.
func (a *sqliteAdapter) ListCodemapsContext(ctx context.Context) ([]domain.CodemapSummary, error) {
	heads, err := a.inner.Artifacts().ListHeads(ctx, sqlitestore.ArtifactFilter{Type: "codemap"})
	if err != nil {
		return nil, err
	}
	summaries := make([]domain.CodemapSummary, 0, len(heads))
	for _, head := range heads {
		if head.CurrentID == "" {
			continue
		}
		published, err := a.inner.Artifacts().Get(ctx, head.CurrentID)
		if err != nil {
			slog.Warn("repository codemap head has no readable current version", "artifactId", string(head.CurrentID), "error", err)
			continue
		}
		var payload struct {
			Query string            `json:"query"`
			Nodes []json.RawMessage `json:"nodes"`
			Edges []json.RawMessage `json:"edges"`
		}
		if err := json.Unmarshal([]byte(published.Payload), &payload); err != nil {
			slog.Warn("repository codemap payload is not decodable for listing", "artifactId", string(head.CurrentID), "error", err)
		}
		status := string(domain.ArtifactCurrent)
		staleReason := ""
		if head.Status == sqlitestore.StatusStale {
			status = string(domain.ArtifactStale)
			staleReason = head.StaleReason
		}
		summaries = append(summaries, domain.CodemapSummary{
			ArtifactID:  domain.ArtifactID(published.ID),
			Title:       published.Title,
			Query:       payload.Query,
			Status:      status,
			StaleReason: staleReason,
			Revision:    published.Revision,
			NodeCount:   len(payload.Nodes),
			EdgeCount:   len(payload.Edges),
			Provider:    published.Provider,
			UpdatedAt:   head.UpdatedAt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt) })
	return summaries, nil
}
func (a *sqliteAdapter) Counts() (files, symbols, edges, wiki int, indexedAt time.Time) {
	counts, err := a.inner.Counts(context.Background())
	if err != nil {
		slog.Error("repository sqlite counts failed", "error", err)
		return 0, 0, 0, 0, time.Time{}
	}
	return counts.Files, counts.Symbols, counts.Edges, counts.WikiPages, counts.IndexedAt
}

func artifactDependencies(input []domain.Dependency) []sqlitestore.ArtifactDependency {
	out := make([]sqlitestore.ArtifactDependency, 0, len(input))
	for _, dep := range input {
		out = append(out, sqlitestore.ArtifactDependency{
			Kind:         dep.Kind,
			SymbolID:     string(dep.SymbolID),
			OccurrenceID: string(dep.OccurrenceID),
			Path:         dep.Path,
			ContentHash:  dep.ContentHash,
		})
	}
	return out
}

func domainArtifactDependencies(input []sqlitestore.ArtifactDependency) []domain.Dependency {
	out := make([]domain.Dependency, 0, len(input))
	for _, dep := range input {
		out = append(out, domain.Dependency{
			Kind:         dep.Kind,
			SymbolID:     domain.SymbolID(dep.SymbolID),
			OccurrenceID: domain.OccurrenceID(dep.OccurrenceID),
			Path:         dep.Path,
			ContentHash:  dep.ContentHash,
		})
	}
	return out
}
func (a *sqliteAdapter) CompositeView(ephemeral domain.ParsedFile, overlay OverlayContext) (CompositeReadView, error) {
	return a.CompositeViewContext(context.Background(), ephemeral, overlay)
}
func (a *sqliteAdapter) CompositeViewContext(ctx context.Context, ephemeral domain.ParsedFile, overlay OverlayContext) (CompositeReadView, error) {
	if ephemeral.File.Path == "" || ephemeral.File.Path != overlay.Path {
		return nil, fmt.Errorf("overlay parsed path %q does not match %q", ephemeral.File.Path, overlay.Path)
	}
	persistedHash, exists, err := a.inner.FileHash(ctx, overlay.Path)
	if err != nil {
		return nil, err
	}
	inner, err := a.inner.OpenReadView(ctx)
	if err != nil {
		return nil, err
	}
	metadata := inner.Metadata()
	rebased := false
	if overlay.BaseSnapshotID != "" && overlay.BaseSnapshotID != metadata.ID {
		if exists && persistedHash != "" && overlay.BaseContentHash != "" && persistedHash != overlay.BaseContentHash {
			_ = inner.Close()
			return nil, ErrDocumentBaseStale
		}
		rebased = true
	}
	if err := inner.ApplyOverlay(ephemeral); err != nil {
		_ = inner.Close()
		return nil, err
	}
	return sqliteCompositeView{
		ReadView: sqliteReadView{inner: inner}, overlay: overlay,
		viewHash: compositeViewHash(metadata.ID, overlay), rebased: rebased,
	}, nil
}

func compositeViewHash(snapshotID domain.SnapshotID, overlay OverlayContext) string {
	digest := sha256.New()
	fmt.Fprintf(digest, "%s\x00%s\x00%s\x00%d\x00%s", snapshotID, overlay.DocumentID, overlay.Path, overlay.Version, overlay.ContentHash)
	return "view:" + hex.EncodeToString(digest.Sum(nil))
}
func (a *sqliteAdapter) Validate(ctx context.Context) error { return a.inner.Validate(ctx) }
func (a *sqliteAdapter) Close() error                       { return a.inner.Close() }

type sqlitePrepared struct {
	change         *changeset.ChangeSet
	expected, next uint64
	noop           bool
}

func (p *sqlitePrepared) IsNoop() bool            { return p.noop }
func (p *sqlitePrepared) ExpectedVersion() uint64 { return p.expected }
func (p *sqlitePrepared) NextVersion() uint64     { return p.next }

type sqliteEmbeddingPrepared struct {
	expected uint64
	next     uint64
	vectors  map[string][]float64
	metadata domain.EmbeddingIndexMetadata
}

func (p *sqliteEmbeddingPrepared) IsNoop() bool            { return false }
func (p *sqliteEmbeddingPrepared) ExpectedVersion() uint64 { return p.expected }
func (p *sqliteEmbeddingPrepared) NextVersion() uint64     { return p.next }

type sqliteReadView struct {
	inner sqlitestore.ReadView
}

func (v sqliteReadView) Metadata() domain.SnapshotMetadata { return v.inner.Metadata() }
func (v sqliteReadView) Files() []domain.File              { return v.inner.Files() }
func (v sqliteReadView) Search(query string, limit int) ([]domain.SearchHit, error) {
	return v.inner.Search(query, limit)
}
func (v sqliteReadView) AllSymbols() []domain.Symbol { return v.inner.AllSymbols() }
func (v sqliteReadView) GetSymbol(id string) (domain.Symbol, bool) {
	return v.inner.GetSymbol(id)
}
func (v sqliteReadView) GetSymbolByOccurrence(id string) (domain.Symbol, bool) {
	return v.inner.GetSymbolByOccurrence(id)
}
func (v sqliteReadView) SymbolsByName(name string) []domain.Symbol {
	return v.inner.SymbolsByName(name)
}
func (v sqliteReadView) SymbolsByPath(path string) []domain.Symbol {
	return v.inner.SymbolsByPath(path)
}
func (v sqliteReadView) SymbolAt(path string, line, column int) (domain.ResolvedSymbol, bool) {
	return v.inner.SymbolAt(path, line, column)
}
func (v sqliteReadView) ResolveSymbol(name, currentPath string) (domain.Symbol, bool) {
	return v.inner.ResolveSymbol(name, currentPath)
}
func (v sqliteReadView) OccurrencesForSymbol(id domain.SymbolID) []domain.SymbolOccurrence {
	return v.inner.OccurrencesForSymbol(id)
}
func (v sqliteReadView) Graph(seedIDs []string, depth, maxNodes int) ([]domain.Symbol, []domain.Edge) {
	return v.inner.Graph(seedIDs, depth, maxNodes)
}
func (v sqliteReadView) EdgesForSymbol(id string) []domain.Edge { return v.inner.EdgesForSymbol(id) }
func (v sqliteReadView) AllEdges() []domain.Edge                { return v.inner.AllEdges() }
func (v sqliteReadView) Embeddings() map[string][]float64       { return v.inner.Embeddings() }
func (v sqliteReadView) Close() error                           { return v.inner.Close() }

type sqliteCompositeView struct {
	ReadView
	overlay  OverlayContext
	viewHash string
	rebased  bool
}

func (v sqliteCompositeView) ViewHash() string        { return v.viewHash }
func (v sqliteCompositeView) Overlay() OverlayContext { return v.overlay }
func (v sqliteCompositeView) Rebased() bool           { return v.rebased }

type emptyReadView struct {
	metadata domain.SnapshotMetadata
}

func (v emptyReadView) Metadata() domain.SnapshotMetadata              { return v.metadata }
func (v emptyReadView) Files() []domain.File                           { return nil }
func (v emptyReadView) Search(string, int) ([]domain.SearchHit, error) { return nil, nil }
func (v emptyReadView) AllSymbols() []domain.Symbol                    { return nil }
func (v emptyReadView) GetSymbol(string) (domain.Symbol, bool)         { return domain.Symbol{}, false }
func (v emptyReadView) GetSymbolByOccurrence(string) (domain.Symbol, bool) {
	return domain.Symbol{}, false
}
func (v emptyReadView) SymbolsByName(string) []domain.Symbol { return nil }
func (v emptyReadView) SymbolsByPath(string) []domain.Symbol { return nil }
func (v emptyReadView) SymbolAt(string, int, int) (domain.ResolvedSymbol, bool) {
	return domain.ResolvedSymbol{}, false
}
func (v emptyReadView) ResolveSymbol(string, string) (domain.Symbol, bool) {
	return domain.Symbol{}, false
}
func (v emptyReadView) OccurrencesForSymbol(domain.SymbolID) []domain.SymbolOccurrence {
	return nil
}
func (v emptyReadView) Graph([]string, int, int) ([]domain.Symbol, []domain.Edge) { return nil, nil }
func (v emptyReadView) EdgesForSymbol(string) []domain.Edge                       { return nil }
func (v emptyReadView) AllEdges() []domain.Edge                                   { return nil }
func (v emptyReadView) Embeddings() map[string][]float64                          { return nil }
func (v emptyReadView) Close() error                                              { return nil }

func cloneVectors(input map[string][]float64) map[string][]float64 {
	out := make(map[string][]float64, len(input))
	for id, vector := range input {
		out[id] = append([]float64(nil), vector...)
	}
	return out
}
