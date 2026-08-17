package semantic

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

type openDocument struct {
	id       DocumentID
	version  DocumentVersion
	path     string
	language string
	content  []byte
}

// Runtime keeps a stable semantic dependency while atomically replacing the
// immutable path router behind it. Document mutations are serialized with
// candidate replay so a newly activated router cannot miss an edit.
type Runtime struct {
	current   atomic.Pointer[PathRouter]
	documents sync.Mutex
	open      map[DocumentID]openDocument
}

func NewRuntime(initial ...*PathRouter) *Runtime {
	runtime := &Runtime{open: make(map[DocumentID]openDocument)}
	router := NewPathRouter(nil)
	if len(initial) > 0 && initial[0] != nil {
		router = initial[0]
	}
	runtime.current.Store(router)
	return runtime
}

func (r *Runtime) router() *PathRouter {
	if router := r.current.Load(); router != nil {
		return router
	}
	return NewPathRouter(nil)
}

// Prepare replays a deterministic, locked snapshot of all open documents into
// candidate. The returned activation and abort closures share a once guard and
// release the document lock; callers must invoke exactly one of them.
func (r *Runtime) Prepare(ctx context.Context, candidate *PathRouter) (activate func(), abort func(context.Context), resultErr error) {
	if candidate == nil {
		candidate = NewPathRouter(nil)
	}
	r.documents.Lock()
	documents := make([]openDocument, 0, len(r.open))
	for _, document := range r.open {
		document.content = append([]byte(nil), document.content...)
		documents = append(documents, document)
	}
	sort.Slice(documents, func(left, right int) bool { return documents[left].id < documents[right].id })
	opened := make([]openDocument, 0, len(documents))
	for _, document := range documents {
		err := candidate.OpenDocument(ctx, document.id, document.version, document.path, document.language, document.content)
		if errors.Is(err, ErrCapabilityUnsupported) {
			continue
		}
		if err != nil {
			for index := len(opened) - 1; index >= 0; index-- {
				_ = candidate.CloseDocument(context.Background(), opened[index].id, opened[index].path)
			}
			r.documents.Unlock()
			return nil, nil, err
		}
		opened = append(opened, document)
	}

	var finish sync.Once
	activate = func() {
		finish.Do(func() {
			r.current.Store(candidate)
			r.documents.Unlock()
		})
	}
	abort = func(abortContext context.Context) {
		finish.Do(func() {
			for index := len(opened) - 1; index >= 0; index-- {
				_ = candidate.CloseDocument(abortContext, opened[index].id, opened[index].path)
			}
			r.documents.Unlock()
		})
	}
	return activate, abort, nil
}

func (r *Runtime) ID() SemanticProviderID {
	router := r.router()
	return router.ID()
}

func (r *Runtime) Capabilities(ctx context.Context) (SemanticCapabilities, error) {
	router := r.router()
	return router.Capabilities(ctx)
}

func (r *Runtime) CapabilitiesForPath(ctx context.Context, path string) (SemanticCapabilities, error) {
	router := r.router()
	return router.CapabilitiesForPath(ctx, path)
}

func (r *Runtime) ProviderIDForPath(path string) (SemanticProviderID, bool) {
	router := r.router()
	return router.ProviderIDForPath(path)
}

func (r *Runtime) ProviderStateForPath(path string) ProviderState {
	router := r.router()
	return router.ProviderStateForPath(path)
}

func (r *Runtime) OpenDocument(ctx context.Context, id DocumentID, version DocumentVersion, path, language string, content []byte) error {
	r.documents.Lock()
	defer r.documents.Unlock()
	router := r.router()
	err := router.OpenDocument(ctx, id, version, path, language, content)
	if err == nil || errors.Is(err, ErrCapabilityUnsupported) {
		r.open[id] = openDocument{id: id, version: version, path: path, language: language, content: append([]byte(nil), content...)}
	}
	return err
}

func (r *Runtime) ChangeDocument(ctx context.Context, id DocumentID, version DocumentVersion, path string, content []byte) error {
	r.documents.Lock()
	defer r.documents.Unlock()
	document := r.open[id]
	router := r.router()
	err := router.ChangeDocument(ctx, id, version, path, content)
	if err == nil || errors.Is(err, ErrCapabilityUnsupported) {
		document.id = id
		document.version = version
		document.path = path
		document.content = append([]byte(nil), content...)
		r.open[id] = document
	}
	return err
}

func (r *Runtime) CloseDocument(ctx context.Context, id DocumentID, path string) error {
	r.documents.Lock()
	defer r.documents.Unlock()
	router := r.router()
	err := router.CloseDocument(ctx, id, path)
	delete(r.open, id)
	return err
}

func (r *Runtime) Hover(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	router := r.router()
	return router.Hover(ctx, query)
}

func (r *Runtime) Definitions(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	router := r.router()
	return router.Definitions(ctx, query)
}

func (r *Runtime) References(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	router := r.router()
	return router.References(ctx, query)
}

func (r *Runtime) Implementations(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	router := r.router()
	return router.Implementations(ctx, query)
}

func (r *Runtime) IncomingCalls(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	router := r.router()
	return router.IncomingCalls(ctx, query)
}

func (r *Runtime) OutgoingCalls(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	router := r.router()
	return router.OutgoingCalls(ctx, query)
}

func (r *Runtime) Diagnostics(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	router := r.router()
	return router.Diagnostics(ctx, query)
}

func (r *Runtime) SemanticTokens(ctx context.Context, query SemanticQuery) (SemanticTokenSet, error) {
	router := r.router()
	return router.SemanticTokens(ctx, query)
}

var (
	_ SemanticProvider      = (*Runtime)(nil)
	_ SemanticTokenProvider = (*Runtime)(nil)
	_ DocumentSemanticSync  = (*Runtime)(nil)
)
