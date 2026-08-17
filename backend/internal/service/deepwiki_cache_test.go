package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

func TestDeepWikiCollectionCachesSnapshotByStoreVersion(t *testing.T) {
	t.Parallel()
	base := seededWikiStore(t)
	counting := &countingDeepWikiStore{Store: base}
	service := NewDeepWikiService(counting, &capturingProvider{response: "# page"})

	for range 20 {
		_ = service.Collection()
	}
	if snapshots, ids := counting.readCounts(); snapshots != 0 || ids != 1 {
		t.Fatalf("stable-version reads = snapshots %d ids %d, want 0/1", snapshots, ids)
	}

	addFile(base, "cache-invalidation.go")
	_ = service.Collection()
	if snapshots, ids := counting.readCounts(); snapshots != 0 || ids != 2 {
		t.Fatalf("post-mutation reads = snapshots %d ids %d, want 0/2", snapshots, ids)
	}
}

func TestDeepWikiCollectionCachesPagesByStoreVersion(t *testing.T) {
	t.Parallel()
	base := seededWikiStore(t)
	if _, err := NewDeepWikiService(base, &capturingProvider{response: "page"}).Generate(context.Background()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	counting := &countingDeepWikiStore{Store: base}
	service := NewDeepWikiService(counting, &capturingProvider{response: "page"})

	for range 20 {
		if len(service.Collection().Pages) == 0 {
			t.Fatal("Collection() lost published pages")
		}
	}
	if reads := counting.wikiPageReadCount(); reads != 1 {
		t.Fatalf("stable-version WikiPages reads = %d, want 1", reads)
	}

	addFile(base, "pages-cache-invalidation.go")
	_ = service.Collection()
	if reads := counting.wikiPageReadCount(); reads != 2 {
		t.Fatalf("post-mutation WikiPages reads = %d, want 2", reads)
	}
}

func TestDeepWikiPublishRefreshesPagesCacheWhenStructuralVersionIsStable(t *testing.T) {
	t.Parallel()
	store := &fixedVersionWikiStore{Store: seededWikiStore(t), version: 77}
	service := NewDeepWikiService(store, &capturingProvider{response: "page"})
	if pages := service.Pages(); len(pages) != 0 {
		t.Fatalf("initial pages = %d, want 0", len(pages))
	}
	if _, err := service.Generate(context.Background()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	pages := service.Pages()
	if len(pages) == 0 {
		t.Fatal("published pages were hidden by the stable-version cache")
	}
	if store.wikiPageReads != 1 {
		t.Fatalf("WikiPages reads = %d, want the initial read only", store.wikiPageReads)
	}
}

func TestDeepWikiCollectionCachesExpectedHashesAfterRestart(t *testing.T) {
	t.Parallel()
	base := seededWikiStore(t)
	if _, err := NewDeepWikiService(base, &capturingProvider{response: "page"}).Generate(context.Background()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	counting := &countingDeepWikiStore{Store: base}
	restarted := NewDeepWikiService(counting, &capturingProvider{response: "page"})
	for range 20 {
		if status := restarted.Collection().Status; status != domain.DeepWikiReady {
			t.Fatalf("restart status = %q, want ready", status)
		}
	}
	if snapshots, ids := counting.readCounts(); snapshots != 1 || ids != 1 {
		t.Fatalf("restart stable-version reads = snapshots %d ids %d, want 1/1", snapshots, ids)
	}

	addFile(base, "restart-cache-invalidation.go")
	if status := restarted.Collection().Status; status != domain.DeepWikiStale {
		t.Fatalf("post-mutation status = %q, want stale", status)
	}
	if snapshots, ids := counting.readCounts(); snapshots != 2 || ids != 2 {
		t.Fatalf("restart post-mutation reads = snapshots %d ids %d, want 2/2", snapshots, ids)
	}
}

func TestDeepWikiRestartRecomputesGettingStartedDerivedScope(t *testing.T) {
	t.Parallel()
	store := seededWikiStore(t)
	service := NewDeepWikiService(store, &capturingProvider{response: "page"})
	service.SetPlannerEnabled(false)
	pages, err := service.Generate(context.Background())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	getting := pageWithArchetype(t, pages, "getting-started")

	restarted := NewDeepWikiService(store, &capturingProvider{response: "page"})
	if got := restarted.expectedHashes([]domain.WikiPage{getting})[getting.Slug]; got != getting.SourceHash {
		t.Fatalf("restart hash = %q, want persisted %q before mutation", got, getting.SourceHash)
	}
	store.ReplaceFile(domain.ParsedFile{File: domain.File{
		Path: "README.md", Language: "markdown", Hash: "readme-v1", Content: "# Project\n",
		Size: 10, IndexedAt: time.Now(),
	}})

	if got := restarted.expectedHashes([]domain.WikiPage{getting})[getting.Slug]; got == getting.SourceHash {
		t.Fatal("getting-started hash ignored a new root project file after restart")
	}
	if got := restarted.Collection().Status; got != domain.DeepWikiStale {
		t.Fatalf("status after root project file = %q, want stale", got)
	}
}

func TestDeepWikiCacheReadsStopWhenStoreVersionKeepsChanging(t *testing.T) {
	base := seededWikiStore(t)
	changing := &changingVersionStore{Store: base}
	service := NewDeepWikiService(changing, &capturingProvider{response: "page"})
	done := make(chan struct{})
	go func() {
		_ = service.currentSnapshot()
		_ = service.expectedHashes([]domain.WikiPage{{Slug: "overview", Archetype: "overview"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cache reads spun indefinitely while the store version changed")
	}
}

func TestSourceHashInvalidatesPagesForDiagramRendererVersion(t *testing.T) {
	t.Parallel()
	symbols := []domain.Symbol{
		{ID: "b", Signature: "func B(ç string)"},
		{ID: "a", Signature: "func A()"},
	}
	edges := []domain.Edge{
		{FromSymbolID: "b", Type: "calls", ToName: "A"},
		{FromSymbolID: "a", Type: "references", ToName: "B"},
	}
	legacy := legacySourceHash(symbols, edges)
	files := []domain.File{{Path: "main.go", Hash: "sha256:main"}}
	got := sourceHash(symbols, edges, files)
	if got == legacy {
		t.Fatalf("sourceHash = %q, want renderer-version invalidation from legacy %q", got, legacy)
	}
	if repeat := sourceHash(slices.Clone(symbols), slices.Clone(edges), slices.Clone(files)); repeat != got {
		t.Fatalf("sourceHash is not deterministic: first=%q repeat=%q", got, repeat)
	}
	changedFiles := slices.Clone(files)
	changedFiles[0].Hash = "sha256:changed"
	if changed := sourceHash(symbols, edges, changedFiles); changed == got {
		t.Fatal("sourceHash ignored indexed file content hash")
	}
}

type countingDeepWikiStore struct {
	repository.Store
	mu            sync.Mutex
	snapshotReads int
	snapshotIDs   int
	wikiPageReads int
}

func (s *countingDeepWikiStore) WikiPages() []domain.WikiPage {
	s.mu.Lock()
	s.wikiPageReads++
	s.mu.Unlock()
	return s.Store.WikiPages()
}

func (s *countingDeepWikiStore) wikiPageReadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wikiPageReads
}

type changingVersionStore struct {
	repository.Store
	mu      sync.Mutex
	version uint64
}

type fixedVersionWikiStore struct {
	repository.Store
	version       uint64
	wikiPageReads int
}

func (s *fixedVersionWikiStore) Version() uint64 { return s.version }
func (s *fixedVersionWikiStore) WikiPages() []domain.WikiPage {
	s.wikiPageReads++
	return s.Store.WikiPages()
}

func (s *changingVersionStore) Version() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	return s.version
}

func (s *countingDeepWikiStore) Snapshot() repository.ReadView {
	s.mu.Lock()
	s.snapshotReads++
	s.mu.Unlock()
	return s.Store.Snapshot()
}

func (s *countingDeepWikiStore) SnapshotID() domain.SnapshotID {
	s.mu.Lock()
	s.snapshotIDs++
	s.mu.Unlock()
	return s.Store.SnapshotID()
}

func (s *countingDeepWikiStore) readCounts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotReads, s.snapshotIDs
}

func legacySourceHash(symbols []domain.Symbol, edges []domain.Edge) string {
	parts := make([]string, 0, len(symbols)+len(edges))
	for _, symbol := range symbols {
		parts = append(parts, symbol.ID+symbol.Signature)
	}
	for _, edge := range edges {
		parts = append(parts, fmt.Sprintf("%s:%s:%s", edge.FromSymbolID, edge.Type, edge.ToName))
	}
	sort.Strings(parts)
	digest := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(digest[:12])
}
