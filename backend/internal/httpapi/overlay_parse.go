package httpapi

import (
	"fmt"
	"sync"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/overlay"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/treesitter"
)

type overlayParseEntry struct {
	mu          sync.Mutex
	version     int64
	contentHash string
	parsed      domain.ParsedFile
	valid       bool
}

// overlayParseCache retains only the latest extracted ParsedFile per open
// document. Per-document locks coalesce concurrent Hover/navigation requests
// without serializing extraction across unrelated documents.
type overlayParseCache struct {
	mu      sync.Mutex
	entries map[string]*overlayParseEntry
}

func newOverlayParseCache() *overlayParseCache {
	return &overlayParseCache{entries: make(map[string]*overlayParseEntry)}
}

func (c *overlayParseCache) entry(documentID string) *overlayParseEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[documentID]
	if entry == nil {
		entry = &overlayParseEntry{}
		c.entries[documentID] = entry
	}
	return entry
}

func (c *overlayParseCache) getOrBuild(documentID string, version int64, contentHash string, build func() (domain.ParsedFile, error)) (domain.ParsedFile, error) {
	entry := c.entry(documentID)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.valid && entry.version == version && entry.contentHash == contentHash {
		return entry.parsed, nil
	}
	parsed, err := build()
	if err != nil {
		return domain.ParsedFile{}, err
	}
	entry.version = version
	entry.contentHash = contentHash
	entry.parsed = parsed
	entry.valid = true
	return parsed, nil
}

func (c *overlayParseCache) invalidate(documentID string) {
	c.mu.Lock()
	delete(c.entries, documentID)
	c.mu.Unlock()
}

// parsedOverlayFile extracts symbols from the exact incremental tree published
// for the overlay version, then caches that normalization for every request at
// the same version. It never creates a second Tree-sitter parser or tree.
func (s *Server) parsedOverlayFile(snapshot overlay.OverlaySnapshot) (domain.ParsedFile, error) {
	documentID := string(snapshot.DocumentID)
	version := int64(snapshot.Version)
	return s.overlayParses.getOrBuild(documentID, version, snapshot.ContentHash, func() (domain.ParsedFile, error) {
		var parsed domain.ParsedFile
		err := s.parseSessions.WithTree(documentID, &version, func(root treesitter.Node, source []byte) error {
			if actual := contenthash.HashContent(source); actual != snapshot.ContentHash {
				return fmt.Errorf("overlay parse content hash mismatch: got %s, want %s", actual, snapshot.ContentHash)
			}
			symbols, edges, language, err := codeparser.New().ExtractTree(snapshot.Path, source, root)
			if err != nil {
				return err
			}
			parsed = domain.ParsedFile{
				File:    domain.File{Path: snapshot.Path, Language: language, Hash: snapshot.ContentHash},
				Symbols: symbols,
				Edges:   edges,
			}
			return nil
		})
		return parsed, err
	})
}
