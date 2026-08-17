package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
)

const original = "package sample\n\nfunc Run() int { return 1 }\n"
const updated = "package sample\n\nfunc Run() int { return 2 }\n"

type failParser struct{}

func (failParser) Parse(string, []byte) ([]domain.Symbol, []domain.Edge, string, error) {
	return nil, nil, "", errors.New("synthetic parse failure")
}

type failEmbedder struct{}

func (failEmbedder) GenerateEmbeddings(context.Context, []domain.Symbol) (map[string][]float64, error) {
	return nil, errors.New("synthetic embedding failure")
}

func newSaveFixture(t *testing.T, parser service.SaveParser, embedder service.SaveEmbedder, maxBytes int64) (*service.SavePreparer, repository.Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if maxBytes == 0 {
		maxBytes = 1_500_000
	}
	preparer := service.NewSavePreparer(service.NewWorkspace(root), repository, parser, embedder, maxBytes)
	return preparer, repository, root
}

func currentHash(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return contenthash.HashContent(data)
}

func assertSaveError(t *testing.T, err error, wantCode string, wantStatus int) {
	t.Helper()
	var saveErr *service.SaveError
	if !errors.As(err, &saveErr) {
		t.Fatalf("error = %v, want *SaveError", err)
	}
	if saveErr.Code != wantCode || saveErr.Status != wantStatus {
		t.Fatalf("save error = %s/%d, want %s/%d", saveErr.Code, saveErr.Status, wantCode, wantStatus)
	}
}

func TestPrepareNoopWhenIdentical(t *testing.T) {
	t.Parallel()
	preparer, repository, root := newSaveFixture(t, codeparser.New(), nil, 0)
	prepared, err := preparer.Prepare(context.Background(), service.SaveRequest{
		Path: "a.go", Content: []byte(original), ExpectedContentHash: currentHash(t, root, "a.go"),
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !prepared.IsNoop() {
		t.Fatal("identical content should be a no-op")
	}
	if repository.Version() != 0 {
		t.Fatal("no-op prepare advanced the store version")
	}
}

func TestPrepareFailureModes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		parser     service.SaveParser
		embedder   service.SaveEmbedder
		maxBytes   int64
		request    func(root string) service.SaveRequest
		wantCode   string
		wantStatus int
	}{
		{
			name: "missing precondition", parser: codeparser.New(),
			request:  func(string) service.SaveRequest { return service.SaveRequest{Path: "a.go", Content: []byte(updated)} },
			wantCode: service.CodePreconditionRequired, wantStatus: 428,
		},
		{
			name: "conflict on changed file", parser: codeparser.New(),
			request: func(string) service.SaveRequest {
				return service.SaveRequest{Path: "a.go", Content: []byte(updated), ExpectedContentHash: "sha256:stale"}
			},
			wantCode: service.CodeFileChangedOnDisk, wantStatus: 409,
		},
		{
			name: "file not found", parser: codeparser.New(),
			request: func(string) service.SaveRequest {
				return service.SaveRequest{Path: "missing.go", Content: []byte(updated), ExpectedContentHash: "sha256:x"}
			},
			wantCode: service.CodeFileNotFound, wantStatus: 404,
		},
		{
			name: "path traversal", parser: codeparser.New(),
			request: func(string) service.SaveRequest {
				return service.SaveRequest{Path: "../secret.go", Content: []byte(updated), ExpectedContentHash: "sha256:x"}
			},
			wantCode: service.CodeSaveInvalidPath, wantStatus: 400,
		},
		{
			name: "file too large", parser: codeparser.New(), maxBytes: 4,
			request: func(root string) service.SaveRequest {
				return service.SaveRequest{Path: "a.go", Content: []byte(updated), ExpectedContentHash: currentHash(t, root, "a.go")}
			},
			wantCode: service.CodeSaveFileTooLarge, wantStatus: 413,
		},
		{
			name: "parse failure", parser: failParser{},
			request: func(root string) service.SaveRequest {
				return service.SaveRequest{Path: "a.go", Content: []byte(updated), ExpectedContentHash: currentHash(t, root, "a.go")}
			},
			wantCode: service.CodeSourceParseFailed, wantStatus: 422,
		},
		{
			name: "embedding failure", parser: codeparser.New(), embedder: failEmbedder{},
			request: func(root string) service.SaveRequest {
				return service.SaveRequest{Path: "a.go", Content: []byte(updated), ExpectedContentHash: currentHash(t, root, "a.go")}
			},
			wantCode: service.CodeEmbeddingUnavailable, wantStatus: 503,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			preparer, repository, root := newSaveFixture(t, tc.parser, tc.embedder, tc.maxBytes)
			before := currentHash(t, root, "a.go")
			_, err := preparer.Prepare(context.Background(), tc.request(root))
			assertSaveError(t, err, tc.wantCode, tc.wantStatus)
			// No side effects: file and store unchanged.
			if after := currentHash(t, root, "a.go"); after != before {
				t.Fatal("a failed prepare modified the source file")
			}
			if repository.Version() != 0 {
				t.Fatal("a failed prepare advanced the store version")
			}
		})
	}
}

func TestPrepareUnsupportedLanguage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, _ := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	preparer := service.NewSavePreparer(service.NewWorkspace(root), repository, codeparser.New(), nil, 1_500_000)
	_, err := preparer.Prepare(context.Background(), service.SaveRequest{
		Path: "notes.txt", Content: []byte("changed"), ExpectedContentHash: contenthash.HashContent([]byte("hello")),
	})
	assertSaveError(t, err, service.CodeSaveUnsupportedLanguage, 422)
}

func TestPrepareCancellation(t *testing.T) {
	t.Parallel()
	preparer, repository, root := newSaveFixture(t, codeparser.New(), nil, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := preparer.Prepare(ctx, service.SaveRequest{Path: "a.go", Content: []byte(updated), ExpectedContentHash: currentHash(t, root, "a.go")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare(cancelled) error = %v, want context.Canceled", err)
	}
	if repository.Version() != 0 {
		t.Fatal("cancelled prepare advanced the store version")
	}
}

func TestPrepareThenCommitHasNoSideEffectsUntilCommit(t *testing.T) {
	t.Parallel()
	preparer, repository, root := newSaveFixture(t, codeparser.New(), nil, 0)
	content := []byte(updated)
	prepared, err := preparer.Prepare(context.Background(), service.SaveRequest{
		Path: "a.go", Content: content, ExpectedContentHash: currentHash(t, root, "a.go"),
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	// Mutate the caller's content slice; the prepared save must keep its own copy.
	content[0] = 'X'

	// Before commit: file and store untouched.
	if got, _ := os.ReadFile(filepath.Join(root, "a.go")); string(got) != original {
		t.Fatal("Prepare modified the source file")
	}
	if repository.Version() != 0 {
		t.Fatal("Prepare advanced the store version")
	}

	if err := preparer.Commit(prepared); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.go"))
	if string(got) != updated {
		t.Fatalf("after commit file = %q, want the updated content (defensive copy failed)", strings.TrimSpace(string(got)))
	}
	if repository.Version() == 0 {
		t.Fatal("commit did not advance the store version")
	}
}
