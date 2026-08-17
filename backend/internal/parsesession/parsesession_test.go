package parsesession

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/treesitter"
)

func sexp(node treesitter.Node) string {
	if node.IsNull() {
		return "<null>"
	}
	var b strings.Builder
	b.WriteByte('(')
	b.WriteString(node.Type())
	count := node.NamedChildCount()
	for i := uint32(0); i < count; i++ {
		b.WriteByte(' ')
		b.WriteString(sexp(node.NamedChild(i)))
	}
	b.WriteByte(')')
	return b.String()
}

func treeSexp(t *testing.T, manager *Manager, documentID string) string {
	t.Helper()
	var got string
	if err := manager.WithTree(documentID, nil, func(root treesitter.Node, _ []byte) error {
		got = sexp(root)
		return nil
	}); err != nil {
		t.Fatalf("WithTree: %v", err)
	}
	return got
}

func freshSexp(t *testing.T, language treesitter.Language, content string) string {
	t.Helper()
	tree, err := treesitter.Parse(language, []byte(content))
	if err != nil {
		t.Fatalf("fresh parse: %v", err)
	}
	defer tree.Close()
	return sexp(tree.RootNode())
}

func TestIncrementalUpdateMatchesFreshParse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		language treesitter.Language
		v1, v2   string
	}{
		{"insert-end", treesitter.LanguageGo, "package s\nfunc a() {}\n", "package s\nfunc a() {}\nfunc b() {}\n"},
		{"replace-middle", treesitter.LanguageGo, "package s\nfunc a() { x() }\n", "package s\nfunc a() { y() }\n"},
		{"ts-insert", treesitter.LanguageTypeScript, "const a = 1\n", "const a = 1\nconst b = 2\n"},
		{"unicode-edit", treesitter.LanguageGo, "package s\n// café\nfunc a() {}\n", "package s\n// café\nfunc a() {}\nfunc b() {}\n"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			manager := NewManager(4, contenthash.HashContent, nil)
			defer manager.CloseAll()
			if _, err := manager.Open("doc", 1, tc.language, []byte(tc.v1)); err != nil {
				t.Fatalf("Open: %v", err)
			}
			snap, err := manager.Update("doc", 2, []byte(tc.v2))
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if !snap.Incremental {
				t.Fatal("expected an incremental update, got a full parse")
			}
			// Changed ranges may be empty for a same-structure token edit, but must
			// always stay within the new source bounds.
			for _, r := range snap.ChangedRanges {
				if r.StartByte > r.EndByte || r.EndByte > uint32(len(tc.v2)) {
					t.Fatalf("changed range %+v out of bounds for %d-byte source", r, len(tc.v2))
				}
			}
			if got, want := treeSexp(t, manager, "doc"), freshSexp(t, tc.language, tc.v2); got != want {
				t.Fatalf("incremental tree != fresh:\n got=%s\nwant=%s", got, want)
			}
			metrics := manager.Metrics().Snapshot()
			if metrics.Incremental != 1 || metrics.FullParses != 1 {
				t.Fatalf("unexpected metrics: %+v", metrics)
			}
		})
	}
}

func TestUpdateDiscardsStaleAndHealsForwardGap(t *testing.T) {
	t.Parallel()
	manager := NewManager(2, contenthash.HashContent, nil)
	defer manager.CloseAll()
	manager.Open("doc", 1, treesitter.LanguageGo, []byte("package s\n"))
	manager.Update("doc", 2, []byte("package s\nfunc a(){}\n"))

	// A stale update (<= current) is discarded; the session stays at v2.
	if _, err := manager.Update("doc", 1, []byte("package s\n")); !errors.Is(err, ErrVersionUnavailable) {
		t.Fatalf("stale version error = %v, want ErrVersionUnavailable", err)
	}
	if snap, _ := manager.Get("doc", nil); snap.Version != 2 {
		t.Fatal("session advanced on a stale update")
	}

	// A forward gap self-heals with a full parse to the requested version.
	snap, err := manager.Update("doc", 5, []byte("package s\nfunc a(){}\nfunc b(){}\n"))
	if err != nil {
		t.Fatalf("forward-gap update should self-heal: %v", err)
	}
	if snap.Version != 5 || snap.Incremental {
		t.Fatalf("forward gap should produce a full parse at v5: %+v", snap)
	}
	if manager.Metrics().Snapshot().Fallbacks[FallbackVersionGap] != 1 {
		t.Fatal("version-gap fallback not recorded")
	}
}

func TestGetAndWithTreeRejectStaleVersion(t *testing.T) {
	t.Parallel()
	manager := NewManager(2, contenthash.HashContent, nil)
	defer manager.CloseAll()
	manager.Open("doc", 1, treesitter.LanguageGo, []byte("package s\n"))
	manager.Update("doc", 2, []byte("package s\nfunc a(){}\n"))
	stale := int64(1)
	if _, err := manager.Get("doc", &stale); !errors.Is(err, ErrVersionUnavailable) {
		t.Fatalf("Get(v1) error = %v, want ErrVersionUnavailable", err)
	}
	if err := manager.WithTree("doc", &stale, func(treesitter.Node, []byte) error { return nil }); !errors.Is(err, ErrVersionUnavailable) {
		t.Fatalf("WithTree(v1) error = %v, want ErrVersionUnavailable", err)
	}
}

func TestInvalidSyntaxReportsErrorsWithoutPanicking(t *testing.T) {
	t.Parallel()
	manager := NewManager(2, contenthash.HashContent, nil)
	defer manager.CloseAll()
	manager.Open("doc", 1, treesitter.LanguageGo, []byte("package s\nfunc a() {}\n"))
	snap, err := manager.Update("doc", 2, []byte("package s\nfunc a() { \n")) // unbalanced brace
	if err != nil {
		t.Fatalf("Update with invalid syntax should still parse: %v", err)
	}
	if !snap.HasErrors {
		t.Fatal("expected HasErrors for syntactically invalid content")
	}
}

func TestCloseFreesSession(t *testing.T) {
	t.Parallel()
	manager := NewManager(2, contenthash.HashContent, nil)
	manager.Open("doc", 1, treesitter.LanguageGo, []byte("package s\n"))
	manager.Close("doc")
	if _, err := manager.Get("doc", nil); err != ErrSessionNotFound {
		t.Fatalf("Get after Close = %v, want ErrSessionNotFound", err)
	}
	if _, err := manager.Update("doc", 2, []byte("package s\n")); err != ErrSessionNotFound {
		t.Fatalf("Update after Close = %v, want ErrSessionNotFound", err)
	}
}

func TestConcurrentDocumentsAreRaceFree(t *testing.T) {
	t.Parallel()
	manager := NewManager(4, contenthash.HashContent, nil)
	defer manager.CloseAll()
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func(documentID string) {
			defer wg.Done()
			if _, err := manager.Open(documentID, 1, treesitter.LanguageGo, []byte("package s\n")); err != nil {
				t.Errorf("Open: %v", err)
				return
			}
			version := int64(1)
			for i := 0; i < 15; i++ {
				next := version + 1
				if _, err := manager.Update(documentID, next, []byte("package s\n//e\nfunc a(){}\n")); err != nil {
					// content is identical each iteration after the first; a no-op edit
					// still advances version. Tolerate only real errors.
					t.Errorf("Update: %v", err)
					return
				}
				version = next
				_ = manager.WithTree(documentID, nil, func(root treesitter.Node, _ []byte) error {
					_ = root.HasError()
					return nil
				})
			}
		}(name)
	}
	wg.Wait()
}

func TestComputeEditMinimalAndUTF8Safe(t *testing.T) {
	t.Parallel()
	// Replace "x" with "yy" in the middle; prefix/suffix common.
	edit, ok := computeEdit([]byte("abXcd"), []byte("abYYcd"))
	if !ok {
		t.Fatal("computeEdit failed")
	}
	if edit.StartByte != 2 || edit.OldEndByte != 3 || edit.NewEndByte != 4 {
		t.Fatalf("non-minimal edit: %+v", edit)
	}
	// A multibyte rune changed: the boundary must not split it.
	mbEdit, ok := computeEdit([]byte("a café b"), []byte("a cafe b"))
	if !ok {
		t.Fatal("computeEdit multibyte failed")
	}
	if mbEdit.StartByte > mbEdit.OldEndByte || mbEdit.NewEndByte > uint32(len("a cafe b")) {
		t.Fatalf("multibyte edit out of bounds: %+v", mbEdit)
	}
}
