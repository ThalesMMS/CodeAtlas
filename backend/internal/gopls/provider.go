package gopls

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspadapter"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// ProviderID is the semantic provider id for gopls.
const ProviderID semantic.SemanticProviderID = "gopls"

// maxResults bounds how many locations a single query yields before truncation.
const maxResults = 200

// SourceFunc returns the content of a workspace-relative path for a query: the
// open buffer for the query's own document, the persisted file otherwise.
type SourceFunc = lspadapter.SourceFunc

// Provider implements semantic.SemanticProvider over a gopls Manager. It converts
// LSP positions/URIs/ranges, bounds payloads, and tags facts with provenance and
// the confidence policy.
type Provider struct {
	manager *Manager
	core    lspadapter.ProviderCore
}

// NewProvider builds the gopls semantic provider.
func NewProvider(manager *Manager, workspaceRoot string, source SourceFunc) *Provider {
	return &Provider{
		manager: manager,
		core: lspadapter.ProviderCore{
			WorkspaceRoot: workspaceRoot, Source: source, ProviderID: ProviderID,
			ProvenanceSource: semantic.SourceGopls, ErrorPrefix: "gopls", Version: manager.Version,
			SessionID: manager.SessionID,
		},
	}
}

func (p *Provider) ID() semantic.SemanticProviderID { return ProviderID }

func (p *Provider) ProviderState() semantic.ProviderState {
	decision, reason := p.manager.Decision()
	state := semantic.ProviderStateDisabled
	if decision == DecisionEnabled {
		state = semantic.ProviderStateAvailable
	} else if decision == DecisionUnavailable {
		state = semantic.ProviderStateUnavailable
	}
	return semantic.ProviderState{State: state, Reason: reason, SessionID: p.manager.SessionID()}
}

func (p *Provider) OpenDocument(ctx context.Context, documentID semantic.DocumentID, version semantic.DocumentVersion, path, language string, content []byte) error {
	return p.manager.Open(ctx, Document{DocumentID: string(documentID), Path: path, LanguageID: language, Version: int64(version), Content: content})
}

func (p *Provider) ChangeDocument(ctx context.Context, documentID semantic.DocumentID, version semantic.DocumentVersion, path string, content []byte) error {
	return p.manager.Change(ctx, Document{DocumentID: string(documentID), Path: path, Version: int64(version), Content: content})
}

func (p *Provider) CloseDocument(ctx context.Context, documentID semantic.DocumentID, path string) error {
	return p.manager.Close(ctx, Document{DocumentID: string(documentID), Path: path})
}

func (p *Provider) Capabilities(context.Context) (semantic.SemanticCapabilities, error) {
	decision, reason := p.manager.Decision()
	if decision == DecisionUnavailable {
		return semantic.SemanticCapabilities{}, fmt.Errorf("%w: %s", ErrUnavailable, reason)
	}
	return p.manager.Capabilities(), nil
}

// prepared bundles the per-request state after preconditions pass.
type prepared struct {
	client   LSPClient
	source   []byte
	starts   []int
	encoding string
	lspLine  int
	lspChar  int
	uri      string
}

// prepare runs the common preconditions and converts the query position to LSP.
func (p *Provider) prepare(query semantic.SemanticQuery, has func(semantic.SemanticCapabilities) bool, method string) (prepared, error) {
	client, err := p.manager.operational()
	if err != nil {
		return prepared{}, err
	}
	caps := p.manager.Capabilities()
	if !has(caps) {
		return prepared{}, semantic.CapabilityUnsupported(method)
	}
	// An overlay query must target the version gopls has actually synced.
	if query.UsesOpenDocument() {
		synced, ok := p.manager.SyncedVersion(string(query.DocumentID))
		if !ok || synced != int64(query.DocumentVersion) {
			return prepared{}, ErrStaleVersion
		}
	}
	encoding := p.manager.Encoding()
	position, err := p.core.PreparePosition(query, encoding, p.manager.documentURI(query.Path))
	if err != nil {
		return prepared{}, err
	}
	return prepared{
		client: client, source: position.Source, starts: position.Starts, encoding: position.Encoding,
		lspLine: position.Line, lspChar: position.Character, uri: position.URI,
	}, nil
}

func capHover(c semantic.SemanticCapabilities) bool          { return c.Hover }
func capDefinition(c semantic.SemanticCapabilities) bool     { return c.Definition }
func capReferences(c semantic.SemanticCapabilities) bool     { return c.References }
func capImplementation(c semantic.SemanticCapabilities) bool { return c.Implementation }
func capSemanticTokens(c semantic.SemanticCapabilities) bool { return c.SemanticTokensFull }

// SemanticTokens returns a version- and process-bound token set normalized to
// the CodeAtlas canonical legend.
func (p *Provider) SemanticTokens(ctx context.Context, query semantic.SemanticQuery) (semantic.SemanticTokenSet, error) {
	unlock := func() {}
	if query.UsesOpenDocument() {
		var err error
		unlock, err = p.manager.documents.Lock(ctx, string(query.DocumentID))
		if err != nil {
			return semantic.SemanticTokenSet{}, err
		}
		defer unlock()
	}
	sessionID := p.manager.SessionID()
	prep, err := p.prepare(query, capSemanticTokens, "textDocument/semanticTokens/full")
	if err != nil {
		return semantic.SemanticTokenSet{}, err
	}
	var result *lspfacts.SemanticTokens
	if err := prep.client.Request(ctx, "textDocument/semanticTokens/full", map[string]any{
		"textDocument": map[string]string{"uri": prep.uri},
	}, &result); err != nil {
		return semantic.SemanticTokenSet{}, err
	}
	if sessionID == "" || sessionID != p.manager.SessionID() {
		return semantic.SemanticTokenSet{}, ErrProviderRestarted
	}
	set := semantic.SemanticTokenSet{
		DocumentID: query.DocumentID, DocumentVersion: query.DocumentVersion,
		ContentHash: contenthash.HashContent(prep.source), ProviderSession: sessionID,
		Tokens: []semantic.SemanticToken{},
		Provenance: semantic.Provenance{
			Source: semantic.SourceGopls, ProviderID: ProviderID,
			ToolVersion: p.manager.Version(), Method: "textDocument/semanticTokens/full",
			ObservedAt: time.Now().UTC(),
		},
	}
	if result == nil {
		return set, nil
	}
	set.ResultID = lspfacts.BoundString(result.ResultID)
	set.Tokens, set.Truncated, set.OmittedCount, err = lspadapter.DecodeSemanticTokens(
		prep.source, prep.starts, prep.encoding, result, p.manager.semanticTokenLegend(), nil,
	)
	if err != nil {
		return semantic.SemanticTokenSet{}, err
	}
	return set, nil
}

// Hover returns hover_type facts; a null hover is zero facts, not an error.
func (p *Provider) Hover(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	prep, err := p.prepare(query, capHover, "textDocument/hover")
	if err != nil {
		return nil, err
	}
	var result *lspfacts.HoverResult
	if err := prep.client.Request(ctx, "textDocument/hover", positionParams(prep), &result); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	detail := semantic.SanitizeDetail(lspfacts.BoundHoverText(result.Contents))
	if detail == "" {
		return nil, nil
	}
	location := semantic.SourceLocation{Path: query.Path, Range: queryRange(query), Encoding: semantic.PositionEncoding(prep.encoding)}
	fact := p.fact(query, semantic.KindHoverType, "textDocument/hover", location, detail)
	return []semantic.SemanticFact{fact}, nil
}

// Definitions returns definition facts (location or locationLink array), deduped
// and deterministically ordered.
func (p *Provider) Definitions(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.locationQuery(ctx, query, capDefinition, "textDocument/definition", semantic.KindDefinition, false)
}

// References returns reference facts with includeDeclaration disabled (Definition
// covers the declaration). An empty list is a valid success.
func (p *Provider) References(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.locationQuery(ctx, query, capReferences, "textDocument/references", semantic.KindReference, true)
}

// Implementations returns implementation facts; capability-gated.
func (p *Provider) Implementations(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.locationQuery(ctx, query, capImplementation, "textDocument/implementation", semantic.KindImplementation, false)
}

// locationQuery runs a position→locations method and converts the locations to
// facts of kind. Reference queries include the protocol-defined context object.
func (p *Provider) locationQuery(ctx context.Context, query semantic.SemanticQuery, has func(semantic.SemanticCapabilities) bool, method, kind string, references bool) ([]semantic.SemanticFact, error) {
	prep, err := p.prepare(query, has, method)
	if err != nil {
		return nil, err
	}
	params := positionParams(prep)
	if references {
		params["context"] = map[string]any{"includeDeclaration": false}
	}
	var raw json.RawMessage
	if err := prep.client.Request(ctx, method, params, &raw); err != nil {
		return nil, err
	}
	locations := lspfacts.NormalizeLocations(raw)
	converter := p.locationConverter(query, &prep)
	facts := make([]semantic.SemanticFact, 0, len(locations))
	truncated := false
	for i, loc := range locations {
		if i >= maxResults {
			truncated = true
			break
		}
		location, ok := converter.Convert(loc)
		if !ok {
			continue
		}
		facts = append(facts, p.fact(query, kind, method, location, ""))
	}
	facts = lspadapter.FinishFacts(facts)
	if truncated && len(facts) > 0 {
		facts[len(facts)-1].Detail = semantic.SanitizeDetail("results truncated at " + fmt.Sprint(maxResults))
	}
	return facts, nil
}

func (p *Provider) locationConverter(query semantic.SemanticQuery, prep *prepared) *lspadapter.LocationConverter {
	converter := p.core.NewLocationConverter(query, p.manager.Encoding())
	if prep != nil {
		converter.Seed(query.Path, prep.source, prep.starts)
	}
	return converter
}

// fact builds a provenance-tagged fact. Subject is a partial ref (SymbolID
// resolution against the ReadView is a separate step); object/location carry the
// observed location.
func (p *Provider) fact(query semantic.SemanticQuery, kind, method string, location semantic.SourceLocation, detail string) semantic.SemanticFact {
	return p.core.Fact(query, kind, method, location, detail, nil)
}

func queryRange(query semantic.SemanticQuery) domain.Range {
	return domain.Range{Start: query.Position, End: query.Position}
}

func positionParams(prep prepared) map[string]any {
	return lspadapter.PositionParams(lspadapter.PreparedPosition{
		URI: prep.uri, Line: prep.lspLine, Character: prep.lspChar,
	})
}
