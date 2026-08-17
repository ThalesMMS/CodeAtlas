package semantic

import "context"

// SemanticProvider answers semantic queries with provenance-tagged facts. A query
// the provider does not support returns ErrCapabilityUnsupported — never an empty
// slice indistinguishable from "no facts". Query is read-only; document
// lifecycle/sync lives in DocumentSemanticSync so lookup and mutation never mix.
type SemanticProvider interface {
	ID() SemanticProviderID
	Capabilities(ctx context.Context) (SemanticCapabilities, error)
	Hover(ctx context.Context, query SemanticQuery) ([]SemanticFact, error)
	Definitions(ctx context.Context, query SemanticQuery) ([]SemanticFact, error)
	References(ctx context.Context, query SemanticQuery) ([]SemanticFact, error)
	Implementations(ctx context.Context, query SemanticQuery) ([]SemanticFact, error)
	IncomingCalls(ctx context.Context, query SemanticQuery) ([]SemanticFact, error)
	OutgoingCalls(ctx context.Context, query SemanticQuery) ([]SemanticFact, error)
	Diagnostics(ctx context.Context, query SemanticQuery) ([]SemanticFact, error)
}

// DocumentSemanticSync is the optional, separate lifecycle/sync surface a provider
// may implement (e.g. an LSP client tracking open documents). It is kept apart
// from SemanticProvider so querying never implies mutation.
type DocumentSemanticSync interface {
	OpenDocument(ctx context.Context, documentID DocumentID, version DocumentVersion, path, language string, content []byte) error
	ChangeDocument(ctx context.Context, documentID DocumentID, version DocumentVersion, path string, content []byte) error
	CloseDocument(ctx context.Context, documentID DocumentID, path string) error
}

// SemanticTokenProvider is optional because a language server may support
// navigation without supporting semantic tokens. Unsupported is a typed error,
// distinct from an available provider returning a legitimate empty token set.
type SemanticTokenProvider interface {
	SemanticTokens(ctx context.Context, query SemanticQuery) (SemanticTokenSet, error)
}

type ProviderStateReporter interface {
	ProviderState() ProviderState
}
