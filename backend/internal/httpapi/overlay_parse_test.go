package httpapi

import (
	"errors"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/overlay"
	"github.com/ThalesMMS/CodeAtlas/internal/parsesession"
	"github.com/ThalesMMS/CodeAtlas/internal/treesitter"
)

func TestParsedOverlayFileUsesIncrementalSessionAndCachesVersion(t *testing.T) {
	t.Parallel()
	content := []byte("export function submit() { return 1 }\n")
	documentID := "doc:overlay"
	manager := parsesession.NewManager(1, contenthash.HashContent, nil)
	if _, err := manager.Open(documentID, 1, treesitter.LanguageTypeScript, content); err != nil {
		t.Fatal(err)
	}
	server := &Server{parseSessions: manager, overlayParses: newOverlayParseCache()}
	snapshot := overlay.OverlaySnapshot{
		DocumentID:  overlay.DocumentID(documentID),
		Path:        "web/checkout.ts",
		Version:     1,
		Content:     content,
		ContentHash: contenthash.HashContent(content),
	}

	first, err := server.parsedOverlayFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Symbols) < 2 || first.File.Hash != snapshot.ContentHash {
		t.Fatalf("parsed overlay = %+v", first)
	}

	// A repeated request for the same version is served from the normalized
	// cache. Closing the underlying tree here makes an accidental second
	// WithTree/extraction observable.
	manager.Close(documentID)
	second, err := server.parsedOverlayFile(snapshot)
	if err != nil {
		t.Fatalf("cached same-version parse: %v", err)
	}
	if len(second.Symbols) != len(first.Symbols) {
		t.Fatalf("cached symbol count = %d, want %d", len(second.Symbols), len(first.Symbols))
	}

	// The cache key includes the version/hash; a new version cannot reuse the
	// prior ParsedFile and therefore requires a live exact-version session.
	snapshot.Version = 2
	snapshot.ContentHash = contenthash.HashContent([]byte("export function submit() { return 2 }\n"))
	if _, err := server.parsedOverlayFile(snapshot); !errors.Is(err, parsesession.ErrSessionNotFound) {
		t.Fatalf("new-version parse error = %v, want session-not-found", err)
	}
}
