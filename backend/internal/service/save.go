package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

// Stable save error codes.
const (
	CodeSaveInvalidPath         = "SAVE_INVALID_PATH"
	CodeFileNotFound            = "FILE_NOT_FOUND"
	CodePreconditionRequired    = "SAVE_PRECONDITION_REQUIRED"
	CodeFileChangedOnDisk       = "FILE_CHANGED_ON_DISK"
	CodeSaveFileTooLarge        = "SAVE_FILE_TOO_LARGE"
	CodeSaveUnsupportedLanguage = "SAVE_UNSUPPORTED_LANGUAGE"
	CodeSourceParseFailed       = "SOURCE_PARSE_FAILED"
	CodeEmbeddingUnavailable    = "EMBEDDING_UNAVAILABLE"
	CodeSaveStoreConflict       = "STORE_VERSION_CONFLICT"
	CodeSaveStoreUnavailable    = "STORE_UNAVAILABLE"
)

// SaveError is a typed error carrying a stable code and an HTTP status so the
// handler can respond without parsing strings. Messages are sanitized and never
// include divergent file content.
type SaveError struct {
	Code    string
	Status  int
	Message string
}

func (e *SaveError) Error() string { return e.Code + ": " + e.Message }

func saveError(code string, status int, message string) *SaveError {
	return &SaveError{Code: code, Status: status, Message: message}
}

// SaveParser parses received bytes into symbols and edges.
type SaveParser interface {
	Parse(path string, source []byte) ([]domain.Symbol, []domain.Edge, string, error)
}

// SaveEmbedder generates embeddings without mutating the store.
type SaveEmbedder interface {
	GenerateEmbeddings(ctx context.Context, symbols []domain.Symbol) (map[string][]float64, error)
}

// SaveRequest is an optimistic single-file save.
type SaveRequest struct {
	Path                string
	Content             []byte
	ExpectedContentHash string
}

// PreparedSave is the opaque, side-effect-free result of preparing a save. Its
// content and prepared index snapshot are private so callers cannot mutate them.
type PreparedSave struct {
	id            string
	path          string
	expectedHash  string
	newHash       string
	content       []byte
	preparedIndex repository.PreparedCommit
	noop          bool
}

func (p *PreparedSave) ID() string             { return p.id }
func (p *PreparedSave) Path() string           { return p.path }
func (p *PreparedSave) NewContentHash() string { return p.newHash }
func (p *PreparedSave) IsNoop() bool           { return p.noop }

// SavePreparer turns a save request into a PreparedSave without touching the
// source file, store, snapshot or events.
type SavePreparer struct {
	workspace    *Workspace
	store        repository.Store
	parser       SaveParser
	embedder     SaveEmbedder
	maxFileBytes int64
}

func NewSavePreparer(workspace *Workspace, st repository.Store, parser SaveParser, embedder SaveEmbedder, maxFileBytes int64) *SavePreparer {
	return &SavePreparer{workspace: workspace, store: st, parser: parser, embedder: embedder, maxFileBytes: maxFileBytes}
}

// Prepare validates the request, enforces optimistic concurrency, parses the
// received bytes, generates embeddings and prepares a candidate index snapshot.
// It performs no writes. A returned error is a *SaveError (or a context error).
func (s *SavePreparer) Prepare(ctx context.Context, request SaveRequest) (*PreparedSave, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := s.workspace.Resolve(request.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, saveError(CodeFileNotFound, 404, "file not found")
		}
		return nil, saveError(CodeSaveInvalidPath, 400, "invalid path or path outside the workspace")
	}

	current, err := s.workspace.Read(request.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, saveError(CodeFileNotFound, 404, "file not found")
		}
		return nil, saveError(CodeSaveInvalidPath, 400, "the file could not be read")
	}
	currentHash := contenthash.HashContent(current)

	if strings.TrimSpace(request.ExpectedContentHash) == "" {
		return nil, saveError(CodePreconditionRequired, 428, "expectedContentHash is required")
	}
	if currentHash != request.ExpectedContentHash {
		return nil, saveError(CodeFileChangedOnDisk, 409, "file changed on disk; current hash "+currentHash)
	}

	if int64(len(request.Content)) > s.maxFileBytes {
		return nil, saveError(CodeSaveFileTooLarge, 413, "content exceeds the maximum size")
	}
	if _, _, supported := codeparser.DetectLanguage(request.Path); !supported {
		return nil, saveError(CodeSaveUnsupportedLanguage, 422, "unsupported language")
	}

	newHash := contenthash.HashContent(request.Content)
	if newHash == currentHash {
		return &PreparedSave{id: newHash, path: request.Path, expectedHash: request.ExpectedContentHash, newHash: newHash, noop: true}, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	symbols, edges, language, err := s.parser.Parse(request.Path, request.Content)
	if err != nil {
		return nil, saveError(CodeSourceParseFailed, 422, textutil.CompactMessage(request.Path+": "+err.Error(), 300))
	}

	now := time.Now().UTC()
	parsed := domain.ParsedFile{
		File:    domain.File{Path: request.Path, Language: language, Hash: newHash, Size: int64(len(request.Content)), ModifiedAt: now, IndexedAt: now},
		Symbols: symbols,
		Edges:   edges,
	}
	if len(symbols) > 0 {
		parsed.File.Summary = symbols[0].Summary
	}

	var embeddings map[string][]float64
	if s.embedder != nil {
		embeddings, err = s.embedder.GenerateEmbeddings(ctx, symbols)
		if err != nil {
			return nil, saveError(CodeEmbeddingUnavailable, 503, "embeddings unavailable")
		}
	}

	metadata, err := s.store.SnapshotMetadataContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, saveError(CodeSaveStoreUnavailable, 503, "the index state could not be read")
	}
	builder := changeset.NewBuilder().WithExpectedVersion(uint64(metadata.Revision)).Upsert(parsed)
	for id, vector := range embeddings {
		builder.Embed(id, vector)
	}
	change, err := builder.Build(now)
	if err != nil {
		return nil, saveError(CodeSourceParseFailed, 422, textutil.CompactMessage(err.Error(), 300))
	}
	prepared, err := s.store.PrepareContext(ctx, change)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, repository.ErrVersionConflict) {
			return nil, saveError(CodeSaveStoreConflict, 409, "the index changed during preparation")
		}
		return nil, saveError(CodeSaveStoreUnavailable, 503, "the index could not be prepared")
	}

	return &PreparedSave{
		id:            newHash,
		path:          request.Path,
		expectedHash:  request.ExpectedContentHash,
		newHash:       newHash,
		content:       append([]byte(nil), request.Content...),
		preparedIndex: prepared,
	}, nil
}

// Commit writes the source file and publishes the prepared index snapshot. A
// no-op commit does nothing. (The fully coordinated, journaled commit is P2.6.)
func (s *SavePreparer) Commit(prepared *PreparedSave) error {
	return s.CommitContext(context.Background(), prepared)
}

func (s *SavePreparer) CommitContext(ctx context.Context, prepared *PreparedSave) error {
	if prepared == nil || prepared.noop {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.workspace.Write(prepared.path, prepared.content); err != nil {
		return err
	}
	if _, err := s.store.CommitPreparedContext(ctx, prepared.preparedIndex); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, repository.ErrVersionConflict) {
			return saveError(CodeSaveStoreConflict, 409, "the index changed; save again")
		}
		return saveError(CodeSaveStoreUnavailable, 503, "the index could not be published")
	}
	return nil
}
