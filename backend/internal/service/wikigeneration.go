package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

const deepWikiWorkerCount = 3

func requiredWikiPageCount(manifest domain.WikiManifest) int {
	total := 0
	for _, entry := range manifest.Pages {
		if entry.Required && !entry.Omitted {
			total++
		}
	}
	return total
}

func (s *DeepWikiService) generateWikiPages(
	ctx context.Context,
	manifest domain.WikiManifest,
	snapshot domain.SnapshotID,
	symbols []domain.Symbol,
	edges []domain.Edge,
	files []domain.File,
	report WikiProgressReporter,
) ([]domain.WikiPage, error) {
	entries := make([]domain.WikiManifestEntry, 0, len(manifest.Pages))
	for _, entry := range manifest.Pages {
		if entry.Required && !entry.Omitted {
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("deepwiki manifest has no generatable pages")
	}
	symbolsByID := make(map[string]domain.Symbol, len(symbols))
	for _, symbol := range symbols {
		if _, exists := symbolsByID[symbol.ID]; !exists {
			symbolsByID[symbol.ID] = symbol
		}
	}

	workerCount := deepWikiWorkerCount
	if workerCount > len(entries) {
		workerCount = len(entries)
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int, len(entries))
	for index := range entries {
		jobs <- index
	}
	close(jobs)

	pages := make([]domain.WikiPage, len(entries))
	var workers sync.WaitGroup
	var stateMu sync.Mutex
	var firstErr error
	completed := 0
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if workCtx.Err() != nil {
					return
				}
				entry := entries[index]
				page, err := s.generateWikiEntry(workCtx, entry, manifest, snapshot, symbols, symbolsByID, edges, files)
				if err != nil {
					stateMu.Lock()
					if firstErr == nil {
						firstErr = err
						progress := WikiProgress{
							Completed: completed, Total: len(entries), PageSlug: entry.Slug,
							Status: WikiProgressFailed, Err: err,
						}
						s.updateWikiGenerationProgress(entry.Slug, completed, len(entries))
						if report != nil {
							report(progress)
						}
						cancel()
					}
					stateMu.Unlock()
					return
				}
				stateMu.Lock()
				if firstErr != nil {
					stateMu.Unlock()
					return
				}
				pages[index] = page
				completed++
				progress := WikiProgress{Completed: completed, Total: len(entries), PageSlug: entry.Slug, Status: WikiProgressCompleted}
				s.updateWikiGenerationProgress(entry.Slug, completed, len(entries))
				if report != nil {
					report(progress)
				}
				stateMu.Unlock()
			}
		}()
	}
	workers.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return pages, nil
}

func (s *DeepWikiService) updateWikiGenerationProgress(slug string, completed, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation == nil {
		return
	}
	s.generation.CurrentPage = slug
	s.generation.CompletedPages = completed
	s.generation.TotalPages = total
}

func (s *DeepWikiService) generateWikiEntry(
	ctx context.Context,
	entry domain.WikiManifestEntry,
	manifest domain.WikiManifest,
	snapshot domain.SnapshotID,
	symbols []domain.Symbol,
	symbolsByID map[string]domain.Symbol,
	edges []domain.Edge,
	files []domain.File,
) (domain.WikiPage, error) {
	hash := wikiEntrySourceHash(entry, symbols, edges, files)
	if entry.Archetype == "glossary" {
		return deterministicGlossaryPage(entry, manifest, symbolsForPaths(symbols, entry.ScopePaths), filesForModule(files, entry.ScopePaths), hash, s.provider.Name()), nil
	}

	scope := contextpack.ScopeModule
	if entry.Archetype == "overview" || entry.Archetype == "getting-started" || entry.Archetype == "architecture-overview" {
		scope = contextpack.ScopeRepository
	}
	request := contextpack.ContextRequest{
		Feature:    contextpack.FeatureDeepWiki,
		SnapshotID: snapshot,
		Query:      entry.Title + " " + entry.Archetype,
		Scope:      &contextpack.RequestScope{Paths: entry.ScopePaths},
		Options:    contextpack.ContextOptions{Scope: scope},
	}
	pack, err := s.packer.Build(ctx, request)
	if err != nil {
		return domain.WikiPage{}, err
	}
	prelude := ""
	if entry.Archetype == "getting-started" {
		prelude = gettingStartedPrelude(pack)
	}
	return s.pageFromPack(ctx, entry, manifest, pack, symbolsByID, hash, prelude)
}

func wikiEntrySourceHash(entry domain.WikiManifestEntry, symbols []domain.Symbol, edges []domain.Edge, files []domain.File) string {
	pageSymbols := symbolsForPaths(symbols, entry.ScopePaths)
	return sourceHash(pageSymbols, edgesForWikiSymbols(edges, pageSymbols), filesForModule(files, entry.ScopePaths))
}

func edgesForWikiSymbols(edges []domain.Edge, symbols []domain.Symbol) []domain.Edge {
	ids := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		ids[symbol.ID] = struct{}{}
	}
	result := make([]domain.Edge, 0)
	for _, edge := range edges {
		if _, ok := ids[edge.FromSymbolID]; ok {
			result = append(result, edge)
			continue
		}
		if edge.ToSymbolID != "" {
			if _, ok := ids[edge.ToSymbolID]; ok {
				result = append(result, edge)
			}
		}
	}
	return result
}
