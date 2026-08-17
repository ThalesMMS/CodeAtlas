package semantic

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// PathRouter dispatches semantic queries to the provider registered for the
// query's file extension. It lets language-specific providers share the service
// boundary without pretending that one provider understands another language.
type PathRouter struct {
	routes    map[string]SemanticProvider
	providers []SemanticProvider
}

// NewPathRouter builds an immutable extension router. Extensions are normalized
// case-insensitively and may be supplied with or without a leading dot.
func NewPathRouter(routes map[string]SemanticProvider) *PathRouter {
	normalized := make(map[string]SemanticProvider, len(routes))
	byID := make(map[SemanticProviderID]SemanticProvider)
	for extension, provider := range routes {
		if provider == nil {
			continue
		}
		extension = strings.ToLower(strings.TrimSpace(extension))
		if extension == "" {
			continue
		}
		if !strings.HasPrefix(extension, ".") {
			extension = "." + extension
		}
		normalized[extension] = provider
		byID[provider.ID()] = provider
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	providers := make([]SemanticProvider, 0, len(ids))
	for _, id := range ids {
		providers = append(providers, byID[SemanticProviderID(id)])
	}
	return &PathRouter{routes: normalized, providers: providers}
}

func (r *PathRouter) ID() SemanticProviderID { return "language-router" }

// Capabilities returns the union of the routed providers. Query methods still
// check the selected provider, so a capability offered for TypeScript cannot be
// accidentally used for Go (or vice versa).
func (r *PathRouter) Capabilities(ctx context.Context) (SemanticCapabilities, error) {
	var merged SemanticCapabilities
	var firstErr error
	successes := 0
	for _, provider := range r.providers {
		caps, err := provider.Capabilities(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s capabilities: %w", provider.ID(), err)
			}
			continue
		}
		successes++
		mergeCapabilities(&merged, caps)
	}
	if successes == 0 && firstErr != nil {
		return SemanticCapabilities{}, firstErr
	}
	return merged, nil
}

// CapabilitiesForPath reports only the provider selected for path. Callers that
// enforce mandatory semantic coverage must use this method so a healthy provider
// for another language cannot mask the selected provider's failure.
func (r *PathRouter) CapabilitiesForPath(ctx context.Context, path string) (SemanticCapabilities, error) {
	provider := r.providerForPath(path)
	if provider == nil {
		return SemanticCapabilities{}, CapabilityUnsupported("capabilities")
	}
	return provider.Capabilities(ctx)
}

// ProviderIDForPath identifies the concrete provider selected for path.
func (r *PathRouter) ProviderIDForPath(path string) (SemanticProviderID, bool) {
	provider := r.providerForPath(path)
	if provider == nil {
		return "", false
	}
	return provider.ID(), true
}

func (r *PathRouter) ProviderStateForPath(path string) ProviderState {
	provider := r.providerForPath(path)
	if provider == nil {
		return ProviderState{State: ProviderStateDisabled, Reason: "provider_not_configured"}
	}
	if reporter, ok := provider.(ProviderStateReporter); ok {
		state := reporter.ProviderState()
		if state.State != ProviderStateAvailable || state.SessionID != "" {
			return state
		}
		return ProviderState{State: ProviderStateDisabled, Reason: "provider_state_not_reported"}
	}
	// Exact editor facts require a process-session identity. A provider that
	// cannot report one remains usable for navigation, but must not be presented
	// as an available version-bound editor provider.
	return ProviderState{State: ProviderStateDisabled, Reason: "provider_state_not_reported"}
}

func (r *PathRouter) providerForPath(path string) SemanticProvider {
	return r.routes[strings.ToLower(filepath.Ext(path))]
}

func (r *PathRouter) provider(query SemanticQuery, method string) (SemanticProvider, error) {
	provider := r.providerForPath(query.Path)
	if provider == nil {
		return nil, CapabilityUnsupported(method)
	}
	return provider, nil
}

func (r *PathRouter) OpenDocument(ctx context.Context, documentID DocumentID, version DocumentVersion, path, language string, content []byte) error {
	provider := r.providerForPath(path)
	if provider == nil {
		return CapabilityUnsupported("textDocument/didOpen")
	}
	syncer, ok := provider.(DocumentSemanticSync)
	if !ok {
		return CapabilityUnsupported("textDocument/didOpen")
	}
	return syncer.OpenDocument(ctx, documentID, version, path, language, content)
}

func (r *PathRouter) ChangeDocument(ctx context.Context, documentID DocumentID, version DocumentVersion, path string, content []byte) error {
	provider := r.providerForPath(path)
	if provider == nil {
		return CapabilityUnsupported("textDocument/didChange")
	}
	syncer, ok := provider.(DocumentSemanticSync)
	if !ok {
		return CapabilityUnsupported("textDocument/didChange")
	}
	return syncer.ChangeDocument(ctx, documentID, version, path, content)
}

func (r *PathRouter) CloseDocument(ctx context.Context, documentID DocumentID, path string) error {
	provider := r.providerForPath(path)
	if provider == nil {
		return CapabilityUnsupported("textDocument/didClose")
	}
	syncer, ok := provider.(DocumentSemanticSync)
	if !ok {
		return CapabilityUnsupported("textDocument/didClose")
	}
	return syncer.CloseDocument(ctx, documentID, path)
}

func (r *PathRouter) Hover(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	provider, err := r.provider(query, "textDocument/hover")
	if err != nil {
		return nil, err
	}
	return provider.Hover(ctx, query)
}

func (r *PathRouter) Definitions(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	provider, err := r.provider(query, "textDocument/definition")
	if err != nil {
		return nil, err
	}
	return provider.Definitions(ctx, query)
}

func (r *PathRouter) References(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	provider, err := r.provider(query, "textDocument/references")
	if err != nil {
		return nil, err
	}
	return provider.References(ctx, query)
}

func (r *PathRouter) Implementations(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	provider, err := r.provider(query, "textDocument/implementation")
	if err != nil {
		return nil, err
	}
	return provider.Implementations(ctx, query)
}

func (r *PathRouter) IncomingCalls(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	provider, err := r.provider(query, "callHierarchy/incomingCalls")
	if err != nil {
		return nil, err
	}
	return provider.IncomingCalls(ctx, query)
}

func (r *PathRouter) OutgoingCalls(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	provider, err := r.provider(query, "callHierarchy/outgoingCalls")
	if err != nil {
		return nil, err
	}
	return provider.OutgoingCalls(ctx, query)
}

func (r *PathRouter) Diagnostics(ctx context.Context, query SemanticQuery) ([]SemanticFact, error) {
	provider, err := r.provider(query, "textDocument/diagnostic")
	if err != nil {
		return nil, err
	}
	return provider.Diagnostics(ctx, query)
}

func (r *PathRouter) SemanticTokens(ctx context.Context, query SemanticQuery) (SemanticTokenSet, error) {
	provider, err := r.provider(query, "textDocument/semanticTokens/full")
	if err != nil {
		return SemanticTokenSet{}, err
	}
	tokenProvider, ok := provider.(SemanticTokenProvider)
	if !ok {
		return SemanticTokenSet{}, CapabilityUnsupported("textDocument/semanticTokens/full")
	}
	return tokenProvider.SemanticTokens(ctx, query)
}

func mergeCapabilities(target *SemanticCapabilities, source SemanticCapabilities) {
	target.Hover = target.Hover || source.Hover
	target.Definition = target.Definition || source.Definition
	target.References = target.References || source.References
	target.Implementation = target.Implementation || source.Implementation
	target.CallHierarchy = target.CallHierarchy || source.CallHierarchy
	target.Diagnostics = target.Diagnostics || source.Diagnostics
	target.SemanticTokensFull = target.SemanticTokensFull || source.SemanticTokensFull
	target.SemanticTokensDelta = target.SemanticTokensDelta || source.SemanticTokensDelta
	target.SemanticTokenTypes = appendUnique(target.SemanticTokenTypes, source.SemanticTokenTypes...)
	target.SemanticTokenModifiers = appendUnique(target.SemanticTokenModifiers, source.SemanticTokenModifiers...)
	target.DocumentSyncKind = mergedContract(target.DocumentSyncKind, source.DocumentSyncKind)
	target.PositionEncoding = mergedContract(target.PositionEncoding, source.PositionEncoding)
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func mergedContract(current, next string) string {
	if current == "" {
		return next
	}
	if next == "" || current == next {
		return current
	}
	return "routed"
}
