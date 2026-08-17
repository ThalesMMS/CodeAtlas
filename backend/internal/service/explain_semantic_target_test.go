package service

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
	"github.com/ThalesMMS/CodeAtlas/internal/semevidence"
)

const semanticTargetFixture = `package main

import "net/http"

func main() {
	repository := new(int)
	_ = repository
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
}
`

func TestHoverResolvesUnindexedLocalAndExternalTargetsWithoutFallingBackToMain(t *testing.T) {
	root, repo := semanticTargetRepository(t)
	semanticProvider := &semanticTargetProvider{}
	llm := &capturingProvider{response: "explanation"}
	explainer := NewExplainer(repo, NewWorkspace(root), llm)
	explainer.SetSemanticSources(semanticProvider, semevidence.NewCollector(semanticProvider, time.Second, 16))

	cases := []struct {
		name          string
		line          int
		column        int
		wantName      string
		wantKind      string
		wantSignature string
	}{
		{name: "local variable", line: 6, column: 5, wantName: "repository", wantKind: domain.KindVariable, wantSignature: "var repository *int"},
		{name: "external method", line: 9, column: 10, wantName: "HandleFunc", wantKind: domain.KindMethod, wantSignature: "func (*http.ServeMux).HandleFunc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			explanation, err := explainer.Explain(context.Background(), domain.ExplainRequest{
				Feature: domain.ExplainFeatureHover,
				Path:    "main.go",
				Position: &domain.Position{
					Line: tc.line, Column: tc.column, Encoding: "utf-16",
				},
			})
			if err != nil {
				t.Fatalf("Explain() error = %v", err)
			}
			if explanation.Symbol.Name != tc.wantName || explanation.Symbol.Kind != tc.wantKind {
				t.Fatalf("resolved symbol = %+v, want %s %s", explanation.Symbol, tc.wantKind, tc.wantName)
			}
			if explanation.Symbol.ID != "" || explanation.Symbol.OccurrenceID != "" {
				t.Fatalf("semantic-only target minted persisted ids: %+v", explanation.Symbol)
			}
			if explanation.Symbol.Path != "main.go" || explanation.Symbol.Signature == "" || len(explanation.Symbol.Signature) < len(tc.wantSignature) || explanation.Symbol.Signature[:len(tc.wantSignature)] != tc.wantSignature {
				t.Fatalf("semantic target location/signature = %+v, want path main.go and prefix %q", explanation.Symbol, tc.wantSignature)
			}
			if explanation.Symbol.Name == "main" {
				t.Fatal("hover silently fell back to the containing main function")
			}
			assertSemanticTargetEvidence(t, explanation, tc.wantName)
		})
	}

	if got := semanticProvider.count("hover", 6); got != 1 {
		t.Fatalf("repository hover calls = %d, want exactly one", got)
	}
	if got := semanticProvider.count("definition", 6); got != 1 {
		t.Fatalf("repository definition calls = %d, want exactly one", got)
	}
	if got := semanticProvider.count("hover", 9); got != 1 {
		t.Fatalf("HandleFunc hover calls = %d, want exactly one", got)
	}
	if got := semanticProvider.count("definition", 9); got != 1 {
		t.Fatalf("HandleFunc definition calls = %d, want exactly one", got)
	}
}

func TestHoverUnindexedIdentifierWithoutSemanticProviderReturnsExplicitError(t *testing.T) {
	root, repo := semanticTargetRepository(t)
	llm := &capturingProvider{response: "must not be called"}
	explainer := NewExplainer(repo, NewWorkspace(root), llm)

	_, err := explainer.Explain(context.Background(), domain.ExplainRequest{
		Feature: domain.ExplainFeatureHover,
		Path:    "main.go",
		Position: &domain.Position{
			Line: 6, Column: 5, Encoding: "utf-16",
		},
	})
	appErr, ok := apperror.As(err)
	if !ok || appErr.Code != apperror.CodeSymbolNotFound {
		t.Fatalf("Explain() error = %v, want SYMBOL_NOT_FOUND", err)
	}
	if len(llm.userPrompts) != 0 {
		t.Fatalf("LLM was called after unsafe target resolution: %d request(s)", len(llm.userPrompts))
	}
}

func TestHoverOverlayCarriesExactDocumentVersionAndContentToSemanticProvider(t *testing.T) {
	root, repo := semanticTargetRepository(t)
	source := []byte(semanticTargetFixture)
	symbols, edges, language, err := codeparser.New().Parse("main.go", source)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	view, err := repo.SnapshotContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	metadata := view.Metadata()
	baseHash := ""
	for _, file := range view.Files() {
		if file.Path == "main.go" {
			baseHash = file.Hash
		}
	}
	_ = view.Close()
	composite, err := repo.CompositeViewContext(context.Background(), domain.ParsedFile{
		File:    domain.File{Path: "main.go", Language: language, Hash: "overlay-content", Content: string(source)},
		Symbols: symbols, Edges: edges,
	}, repository.OverlayContext{
		DocumentID: "doc:main", Path: "main.go", Version: 7, ContentHash: "overlay-content",
		BaseContentHash: baseHash, BaseSnapshotID: metadata.ID,
	})
	if err != nil {
		t.Fatalf("CompositeViewContext() error = %v", err)
	}
	semanticProvider := &semanticTargetProvider{}
	explainer := NewExplainer(repo, NewWorkspace(root), &capturingProvider{response: "overlay"})
	explainer.SetSemanticSources(semanticProvider, semevidence.NewCollector(semanticProvider, time.Second, 16))

	explanation, err := explainer.ExplainOverlay(context.Background(), domain.ExplainRequest{
		Feature: domain.ExplainFeatureHover, DocumentID: "doc:main", DocumentVersion: 7, Path: "main.go",
		Position: &domain.Position{Line: 6, Column: 5, Encoding: "utf-16"},
	}, composite, source, nil)
	if err != nil {
		t.Fatalf("ExplainOverlay() error = %v", err)
	}
	if explanation.Symbol.Name != "repository" || explanation.Symbol.Kind != domain.KindVariable {
		t.Fatalf("overlay target = %+v, want local variable repository", explanation.Symbol)
	}
	if explanation.DocumentID != "doc:main" || explanation.DocumentVersion != 7 || !explanation.Ephemeral {
		t.Fatalf("overlay response identity = document %q version %d ephemeral=%v", explanation.DocumentID, explanation.DocumentVersion, explanation.Ephemeral)
	}
	query := semanticProvider.query("hover", 6)
	if query.DocumentID != "doc:main" || query.DocumentVersion != 7 || string(query.Content) != semanticTargetFixture {
		t.Fatalf("overlay semantic query = %+v, want exact document/version/content", query)
	}
	if got := semanticProvider.count("hover", 6); got != 1 {
		t.Fatalf("overlay hover calls = %d, want exactly one", got)
	}
}

func TestHoverAmbiguousIndexedFallbackReturnsExplicitError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n\nfunc main() { Duplicate() }\n")
	writeFile(t, root, "pkg/a/a.go", "package a\n\nfunc Duplicate() {}\n")
	writeFile(t, root, "pkg/b/b.go", "package b\n\nfunc Duplicate() {}\n")
	repo, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := indexer.New(root, 1_500_000, codeparser.New(), repo, nil).Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	llm := &capturingProvider{response: "must not be called"}
	explainer := NewExplainer(repo, NewWorkspace(root), llm)
	_, err = explainer.Explain(context.Background(), domain.ExplainRequest{
		Feature: domain.ExplainFeatureHover, Path: "main.go",
		Position: &domain.Position{Line: 3, Column: 16, Encoding: "utf-16"},
	})
	appErr, ok := apperror.As(err)
	if !ok || appErr.Code != apperror.CodeSymbolAmbiguous {
		t.Fatalf("Explain() error = %v, want SYMBOL_AMBIGUOUS", err)
	}
	if len(llm.userPrompts) != 0 {
		t.Fatalf("LLM was called for ambiguous target: %d request(s)", len(llm.userPrompts))
	}
}

func TestSeeMoreReusesSemanticTargetAndPreloadedDefinition(t *testing.T) {
	root, repo := semanticTargetRepository(t)
	semanticProvider := &semanticTargetProvider{}
	explainer := NewExplainer(repo, NewWorkspace(root), &groundedSeeMoreProvider{})
	explainer.SetSemanticSources(semanticProvider, semevidence.NewCollector(semanticProvider, time.Second, 16))

	explanation, err := explainer.Explain(context.Background(), domain.ExplainRequest{
		Feature: domain.ExplainFeatureSeeMore, Path: "main.go",
		Position: &domain.Position{Line: 9, Column: 10, Encoding: "utf-16"},
	})
	if err != nil {
		t.Fatalf("Explain(see_more) error = %v", err)
	}
	if explanation.Symbol.Name != "HandleFunc" || explanation.Symbol.Kind != domain.KindMethod {
		t.Fatalf("See More target = %+v, want method HandleFunc", explanation.Symbol)
	}
	if got := semanticProvider.count("hover", 9); got != 1 {
		t.Fatalf("See More hover calls = %d, want exactly one", got)
	}
	if got := semanticProvider.count("definition", 9); got != 1 {
		t.Fatalf("See More definition calls = %d, want exactly one", got)
	}
	foundExternalBoundary := false
	for _, evidence := range explanation.Evidence {
		if evidence.Relation == "definition" && evidence.Path == "" {
			foundExternalBoundary = true
		}
	}
	if !foundExternalBoundary {
		t.Fatalf("See More evidence lacks non-navigable external definition: %+v", explanation.Evidence)
	}
}

// groundedSeeMoreProvider returns the smallest valid See More response while
// grounding its required changeImpact claim in evidence from the actual pack.
type groundedSeeMoreProvider struct {
	capturingProvider
}

func (p *groundedSeeMoreProvider) Complete(_ context.Context, _, userPrompt string, _ int) (string, error) {
	start := strings.Index(userPrompt, "ev:")
	if start < 0 {
		return "", semantic.CapabilityUnsupported("test evidence")
	}
	end := strings.IndexByte(userPrompt[start:], '"')
	if end < 0 {
		return "", semantic.CapabilityUnsupported("test evidence")
	}
	evidenceID := userPrompt[start : start+end]
	return `{"schemaVersion":"explanation/v2","summary":"details","observations":[],"inferences":[],"uncertainties":[],"changeImpact":[{"text":"impact","evidenceIds":[` + strconv.Quote(evidenceID) + `]}]}`, nil
}

func semanticTargetRepository(t *testing.T) (string, repository.Store) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "main.go", semanticTargetFixture)
	writeFile(t, root, "go.mod", "module example.com/semantic-target\n\ngo 1.22\n")
	repo, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	backgroundIndexer := indexer.New(root, 1_500_000, codeparser.New(), repo, nil)
	if err := backgroundIndexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	return root, repo
}

func assertSemanticTargetEvidence(t *testing.T, explanation domain.Explanation, name string) {
	t.Helper()
	found := false
	for _, evidence := range explanation.Evidence {
		if evidence.Relation != "type" || evidence.Path != "main.go" {
			continue
		}
		for _, provenance := range evidence.Provenance {
			if provenance.Source == semantic.SourceGopls {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("semantic target %q lacks local gopls type evidence: %+v", name, explanation.Evidence)
	}
	for _, evidence := range explanation.Evidence {
		if evidence.Path == "file:///opt/homebrew/Cellar/go/1.26.4/libexec/src/net/http/server.go" {
			t.Fatalf("external URI escaped as navigable evidence path: %+v", evidence)
		}
	}
}

type semanticTargetProvider struct {
	mu      sync.Mutex
	counts  map[string]int
	queries map[string]semantic.SemanticQuery
}

func (p *semanticTargetProvider) ID() semantic.SemanticProviderID { return "gopls" }

func (p *semanticTargetProvider) Capabilities(context.Context) (semantic.SemanticCapabilities, error) {
	return semantic.SemanticCapabilities{Hover: true, Definition: true, Diagnostics: true}, nil
}

func (p *semanticTargetProvider) Hover(_ context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	p.record("hover", query)
	detail := ""
	switch query.Position.Line {
	case 6:
		detail = "```go\nvar repository *int\n```"
	case 9:
		detail = "```go\nfunc (*http.ServeMux).HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))\n```\n\nHandleFunc registers the handler function for the given pattern."
	default:
		return nil, nil
	}
	return []semantic.SemanticFact{{
		Kind: semantic.KindHoverType,
		Location: semantic.SourceLocation{
			Path: "main.go", Range: domain.Range{Start: query.Position, End: query.Position}, Encoding: semantic.EncodingUTF8,
		},
		Detail: detail, Confidence: semantic.ConfidenceLanguageServerResolved,
		Provenance: semantic.Provenance{Source: semantic.SourceGopls, ProviderID: "gopls", Method: "textDocument/hover", ObservedAt: time.Now().UTC()},
	}}, nil
}

func (p *semanticTargetProvider) Definitions(_ context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	p.record("definition", query)
	location := semantic.SourceLocation{Path: "main.go", Encoding: semantic.EncodingUTF8}
	switch query.Position.Line {
	case 6:
		location.Range = domain.Range{Start: domain.Position{Line: 6, Column: 2}, End: domain.Position{Line: 6, Column: 12}}
	case 9:
		location.Path = "file:///opt/homebrew/Cellar/go/1.26.4/libexec/src/net/http/server.go"
	default:
		return nil, nil
	}
	return []semantic.SemanticFact{{
		Kind: semantic.KindDefinition, Location: location,
		Confidence: semantic.ConfidenceLanguageServerResolved,
		Provenance: semantic.Provenance{Source: semantic.SourceGopls, ProviderID: "gopls", Method: "textDocument/definition", ObservedAt: time.Now().UTC()},
	}}, nil
}

func (p *semanticTargetProvider) References(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("references")
}

func (p *semanticTargetProvider) Implementations(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("implementation")
}

func (p *semanticTargetProvider) IncomingCalls(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("incomingCalls")
}

func (p *semanticTargetProvider) OutgoingCalls(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("outgoingCalls")
}

func (p *semanticTargetProvider) Diagnostics(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, nil
}

func (p *semanticTargetProvider) record(method string, query semantic.SemanticQuery) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.counts == nil {
		p.counts = make(map[string]int)
	}
	if p.queries == nil {
		p.queries = make(map[string]semantic.SemanticQuery)
	}
	key := method + ":" + string(rune(query.Position.Line))
	p.counts[key]++
	p.queries[key] = query
}

func (p *semanticTargetProvider) count(method string, line int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counts[method+":"+string(rune(line))]
}

func (p *semanticTargetProvider) query(method string, line int) semantic.SemanticQuery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.queries[method+":"+string(rune(line))]
}

var _ semantic.SemanticProvider = (*semanticTargetProvider)(nil)
var _ contextpack.SemanticSource = (*semevidence.Collector)(nil)
