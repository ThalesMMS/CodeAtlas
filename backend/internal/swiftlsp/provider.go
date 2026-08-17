package swiftlsp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspadapter"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

const (
	maxResults            = 200
	maxCallHierarchyItems = 8
	maxCalls              = 100
	maxRangesPerRelation  = 16
)

// SourceFunc returns the content of a workspace-relative path for a query.
type SourceFunc = lspadapter.SourceFunc

// Provider implements semantic.SemanticProvider for Swift over the Swift
// language server. It reuses lspconv for positions and lspfacts for protocol
// shapes; only SourceKit-specific behavior lives here.
type Provider struct {
	manager       *Manager
	workspaceRoot string
	source        SourceFunc
	core          lspadapter.ProviderCore
}

func NewProvider(manager *Manager, workspaceRoot string, source SourceFunc) *Provider {
	return &Provider{
		manager: manager, workspaceRoot: workspaceRoot, source: source,
		core: lspadapter.ProviderCore{
			WorkspaceRoot: workspaceRoot, Source: source, ProviderID: ProviderID,
			ProvenanceSource: semantic.SourceSwiftLSP, ErrorPrefix: "sourcekit", Version: manager.Version,
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

func (p *Provider) OpenDocument(ctx context.Context, documentID semantic.DocumentID, version semantic.DocumentVersion, path, _ string, content []byte) error {
	return p.manager.Open(ctx, Document{DocumentID: string(documentID), Path: path, Version: int64(version), Content: content})
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

type prepared struct {
	client   LSPClient
	source   []byte
	starts   []int
	encoding string
	lspLine  int
	lspChar  int
	uri      string
}

func (p *Provider) prepare(query semantic.SemanticQuery, has func(semantic.SemanticCapabilities) bool, method string) (prepared, error) {
	client, err := p.manager.operational()
	if err != nil {
		return prepared{}, err
	}
	if !has(p.manager.Capabilities()) {
		return prepared{}, semantic.CapabilityUnsupported(method)
	}
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
func capCallHierarchy(c semantic.SemanticCapabilities) bool  { return c.CallHierarchy }
func capSemanticTokens(c semantic.SemanticCapabilities) bool { return c.SemanticTokensFull }

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
			Source: semantic.SourceSwiftLSP, ProviderID: ProviderID,
			ToolVersion: p.manager.Version(), Method: "textDocument/semanticTokens/full",
			ObservedAt: time.Now().UTC(),
		},
	}
	if result == nil {
		return set, nil
	}
	set.ResultID = lspfacts.BoundString(result.ResultID)
	set.Tokens, set.Truncated, set.OmittedCount, err = lspadapter.DecodeSemanticTokens(
		prep.source, prep.starts, prep.encoding, result, p.manager.semanticTokenLegend(), map[string]string{"member": "method"},
	)
	if err != nil {
		return semantic.SemanticTokenSet{}, err
	}
	return set, nil
}

func (p *Provider) Hover(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	sessionID, unlock, err := p.lockSemanticQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	defer unlock()
	prep, err := p.prepare(query, capHover, "textDocument/hover")
	if err != nil {
		return nil, err
	}
	var result *lspfacts.HoverResult
	if err := prep.client.Request(ctx, "textDocument/hover", positionParams(prep), &result); err != nil {
		return nil, err
	}
	if err := p.validateSession(sessionID); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	detail := semantic.SanitizeDetail(lspfacts.BoundHoverText(result.Contents))
	if detail == "" {
		return nil, nil
	}
	location := semantic.SourceLocation{Path: query.Path, Range: domain.Range{Start: query.Position, End: query.Position}, Encoding: semantic.PositionEncoding(prep.encoding)}
	return []semantic.SemanticFact{p.fact(query, semantic.KindHoverType, "textDocument/hover", location, detail, nil)}, nil
}

func (p *Provider) Definitions(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.locationQuery(ctx, query, capDefinition, "textDocument/definition", semantic.KindDefinition, false)
}

func (p *Provider) References(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.locationQuery(ctx, query, capReferences, "textDocument/references", semantic.KindReference, true)
}

func (p *Provider) Implementations(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.locationQuery(ctx, query, capImplementation, "textDocument/implementation", semantic.KindImplementation, false)
}

func (p *Provider) locationQuery(ctx context.Context, query semantic.SemanticQuery, has func(semantic.SemanticCapabilities) bool, method, kind string, references bool) ([]semantic.SemanticFact, error) {
	sessionID, unlock, err := p.lockSemanticQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	defer unlock()
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
	if err := p.validateSession(sessionID); err != nil {
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
		if location, ok := converter.Convert(loc); ok {
			facts = append(facts, p.fact(query, kind, method, location, "", nil))
		}
	}
	facts = lspadapter.FinishFacts(facts)
	if truncated && len(facts) > 0 {
		facts[len(facts)-1].Detail = semantic.SanitizeDetail("results truncated at " + fmt.Sprint(maxResults))
	}
	return facts, nil
}

func (p *Provider) IncomingCalls(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.callQuery(ctx, query, "callHierarchy/incomingCalls", semantic.KindCallIncoming)
}

func (p *Provider) OutgoingCalls(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.callQuery(ctx, query, "callHierarchy/outgoingCalls", semantic.KindCallOutgoing)
}

func (p *Provider) callQuery(ctx context.Context, query semantic.SemanticQuery, method, kind string) ([]semantic.SemanticFact, error) {
	sessionID, unlock, err := p.lockSemanticQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	defer unlock()
	prep, err := p.prepare(query, capCallHierarchy, "textDocument/prepareCallHierarchy")
	if err != nil {
		return nil, err
	}
	var items []lspfacts.CallHierarchyItem
	if err := prep.client.Request(ctx, "textDocument/prepareCallHierarchy", positionParams(prep), &items); err != nil {
		return nil, err
	}
	if err := p.validateSession(sessionID); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > maxCallHierarchyItems {
		items = items[:maxCallHierarchyItems]
	}
	if len(items) > 1 && !lspadapter.SameSymbol(items) {
		return nil, apperror.SymbolAmbiguous(items[0].Name, len(items))
	}
	facts := make([]semantic.SemanticFact, 0)
	converter := p.locationConverter(query, &prep)
	truncated := false
	if kind == semantic.KindCallIncoming {
		var calls []lspfacts.IncomingCall
		if err := prep.client.Request(ctx, method, map[string]any{"item": items[0]}, &calls); err != nil {
			return nil, err
		}
		if err := p.validateSession(sessionID); err != nil {
			return nil, err
		}
		for i, call := range calls {
			if i >= maxCalls {
				truncated = true
				break
			}
			facts = append(facts, p.callFact(query, converter, kind, method, call.From, call.From.URI, call.FromRanges))
		}
	} else {
		var calls []lspfacts.OutgoingCall
		if err := prep.client.Request(ctx, method, map[string]any{"item": items[0]}, &calls); err != nil {
			return nil, err
		}
		if err := p.validateSession(sessionID); err != nil {
			return nil, err
		}
		for i, call := range calls {
			if i >= maxCalls {
				truncated = true
				break
			}
			facts = append(facts, p.callFact(query, converter, kind, method, call.To, items[0].URI, call.FromRanges))
		}
	}
	facts = lspadapter.FinishFacts(facts)
	if truncated && len(facts) > 0 {
		facts[len(facts)-1].Detail = semantic.SanitizeDetail(fmt.Sprintf("calls truncated at %d", maxCalls))
	}
	return facts, nil
}

func (p *Provider) Diagnostics(_ context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	if _, err := p.manager.operational(); err != nil {
		return nil, err
	}
	uri := p.manager.documentURI(query.Path)
	set, ok := p.manager.diagnostics.Get(uri)
	if !ok {
		return nil, nil
	}
	if set.VersionKnown && query.UsesOpenDocument() && set.Version != int64(query.DocumentVersion) {
		return nil, nil
	}
	facts := make([]semantic.SemanticFact, 0, len(set.Items))
	converter := p.locationConverter(query, nil)
	for _, diagnostic := range set.Items {
		location, ok := converter.Convert(lspfacts.Location{URI: uri, Range: diagnostic.Range})
		if !ok || location.Range == (domain.Range{}) {
			continue
		}
		fact := p.fact(query, semantic.KindDiagnostic, "textDocument/publishDiagnostics", location, lspadapter.DiagnosticDetail(diagnostic), nil)
		normalized := lspadapter.NormalizeDiagnostic(diagnostic)
		fact.Diagnostic = &normalized
		fact.Confidence = semantic.ConfidenceDiagnostic
		fact.VersionKnown = set.VersionKnown
		if set.VersionKnown {
			fact.DocumentVersion = semantic.DocumentVersion(set.Version)
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// --- helpers ---

func (p *Provider) locationConverter(query semantic.SemanticQuery, prep *prepared) *lspadapter.LocationConverter {
	converter := p.core.NewLocationConverter(query, p.manager.Encoding())
	if prep != nil {
		converter.Seed(query.Path, prep.source, prep.starts)
	}
	return converter
}

func (p *Provider) fact(query semantic.SemanticQuery, kind, method string, location semantic.SourceLocation, detail string, object *semantic.SymbolRef) semantic.SemanticFact {
	return p.core.Fact(query, kind, method, location, detail, object)
}

func (p *Provider) callFact(query semantic.SemanticQuery, converter *lspadapter.LocationConverter, kind, method string, other lspfacts.CallHierarchyItem, rangeURI string, fromRanges []lspfacts.Range) semantic.SemanticFact {
	location, _ := converter.Convert(lspfacts.Location{URI: other.URI, Range: lspfacts.SelectionOf(other)})
	fact := p.fact(query, kind, method, location, "", &semantic.SymbolRef{Name: other.Name})
	for i, r := range fromRanges {
		if i >= maxRangesPerRelation {
			break
		}
		if related, ok := converter.Convert(lspfacts.Location{URI: rangeURI, Range: r}); ok {
			fact.Related = append(fact.Related, related)
		}
	}
	return fact
}

func (p *Provider) lockSemanticQuery(ctx context.Context, query semantic.SemanticQuery) (string, func(), error) {
	unlock := func() {}
	if query.UsesOpenDocument() {
		var err error
		unlock, err = p.manager.documents.Lock(ctx, string(query.DocumentID))
		if err != nil {
			return "", unlock, err
		}
	}
	return p.manager.SessionID(), unlock, nil
}

func (p *Provider) validateSession(sessionID string) error {
	if sessionID == "" || sessionID != p.manager.SessionID() {
		return ErrProviderRestarted
	}
	return nil
}

func positionParams(prep prepared) map[string]any {
	return lspadapter.PositionParams(lspadapter.PreparedPosition{
		URI: prep.uri, Line: prep.lspLine, Character: prep.lspChar,
	})
}
