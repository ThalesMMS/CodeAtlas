package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

func TestNavigationDefinitionUsesIdentifierAtPositionAndDoesNotCallLLM(t *testing.T) {
	t.Parallel()
	root, repo := navigationFixture(t)
	navigation := NewNavigationService(repo, NewWorkspace(root))

	result, err := navigation.Query(context.Background(), domain.NavigationRequest{
		Kind: domain.NavigationKindDefinition,
		Path: "internal/order/service.go",
		Position: &domain.NavigationPosition{
			Line: 6, Column: 13, Encoding: "utf-16",
		},
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if result.Kind != domain.NavigationKindDefinition || result.SnapshotID == "" || result.ViewHash == "" {
		t.Fatalf("missing navigation metadata: %#v", result)
	}
	if result.Subject.Name != "persist" || result.Subject.SymbolID == "" || result.Subject.OccurrenceID == "" {
		t.Fatalf("subject = %#v, want resolved persist symbol", result.Subject)
	}
	if result.Total != 1 || result.Truncated || len(result.Targets) != 1 {
		t.Fatalf("targets total/truncated = %d/%v len=%d", result.Total, result.Truncated, len(result.Targets))
	}
	target := result.Targets[0]
	if target.Label != "persist" || target.Path != "internal/order/service.go" || target.Range.Start.Line != 9 {
		t.Fatalf("target = %#v, want persist definition on line 9", target)
	}
	if target.TargetID == "" || target.SymbolID == "" || target.OccurrenceID == "" {
		t.Fatalf("target is missing stable ids: %#v", target)
	}
	if target.Relationship != "definition" || target.External {
		t.Fatalf("relationship/external = %q/%v", target.Relationship, target.External)
	}
	if result.SemanticCoverage["providerState"] != "disabled" || result.SemanticCoverage["coverage"] != "ast_only" {
		t.Fatalf("semantic coverage = %#v, want explicit AST-only provider-disabled coverage", result.SemanticCoverage)
	}
}

func TestNavigationMergesSemanticProviderTargetsAndReportsConcreteCoverage(t *testing.T) {
	t.Parallel()
	root, repo := navigationFixture(t)
	navigation := NewNavigationService(repo, NewWorkspace(root))
	request := domain.NavigationRequest{
		Kind: domain.NavigationKindDefinition, Path: "internal/order/service.go",
		Position: &domain.NavigationPosition{Line: 6, Column: 13, Encoding: "utf-16"},
	}
	baseline, err := navigation.Query(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	provider := &navigationSemanticProvider{facts: []semantic.SemanticFact{{
		Kind:       semantic.KindDefinition,
		Location:   semantic.SourceLocation{Path: baseline.Targets[0].Path, Range: baseline.Targets[0].Range, Encoding: semantic.EncodingUTF16},
		Provenance: semantic.Provenance{Source: semantic.SourceGopls, ProviderID: "gopls", Method: "textDocument/definition"},
		Confidence: semantic.ConfidenceLanguageServerResolved,
	}}}
	navigation.SetSemanticProvider(provider)

	result, err := navigation.Query(context.Background(), request)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if result.Total != 1 || len(result.Targets) != 1 {
		t.Fatalf("merged targets total/len = %d/%d, want one location", result.Total, len(result.Targets))
	}
	if result.SemanticCoverage["coverage"] != "ast+lsp" || result.SemanticCoverage["providerState"] != "available" || result.SemanticCoverage["provider"] != "gopls" {
		t.Fatalf("semantic coverage = %#v", result.SemanticCoverage)
	}
	sources := map[string]bool{}
	for _, provenance := range result.Targets[0].Provenance {
		sources[provenance.Source] = true
	}
	if !sources["tree-sitter"] || !sources[semantic.SourceGopls] || result.Targets[0].Confidence != semantic.ConfidenceLanguageServerResolved {
		t.Fatalf("merged target = %#v", result.Targets[0])
	}
	if provider.lastQuery.Path != request.Path || provider.lastQuery.Position.Column != request.Position.Column || provider.lastQuery.DocumentID != "" {
		t.Fatalf("semantic query = %#v", provider.lastQuery)
	}
}

func TestNavigationSemanticDefinitionKeepsUnindexedLocalInsteadOfContainingFunction(t *testing.T) {
	t.Parallel()
	root, repo := navigationFixture(t)
	navigation := NewNavigationService(repo, NewWorkspace(root))
	navigation.SetSemanticProvider(&navigationSemanticProvider{facts: []semantic.SemanticFact{{
		Kind: semantic.KindDefinition,
		Location: semantic.SourceLocation{
			Path: "internal/order/service.go",
			Range: domain.Range{
				Start: domain.Position{Line: 12, Column: 2},
				End:   domain.Position{Line: 12, Column: 9},
			},
			Encoding: semantic.EncodingUTF16,
		},
		Provenance: semantic.Provenance{Source: semantic.SourceGopls, ProviderID: "gopls", Method: "textDocument/definition"},
		Confidence: semantic.ConfidenceLanguageServerResolved,
	}}})

	result, err := navigation.Query(context.Background(), domain.NavigationRequest{
		Kind: domain.NavigationKindDefinition,
		Path: "internal/order/service.go",
		Position: &domain.NavigationPosition{
			Line: 13, Column: 7, Encoding: "utf-16",
		},
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if result.Subject.Name != "service" || result.Total != 1 {
		t.Fatalf("result subject/total = %#v/%d, want local service and one target", result.Subject, result.Total)
	}
	target := result.Targets[0]
	if target.Label != "service" || target.Path != "internal/order/service.go" || target.Range.Start.Line != 12 || target.Range.Start.Column != 2 {
		t.Fatalf("semantic local target = %#v, want service declaration on line 12", target)
	}
	if target.SymbolID != "" || target.OccurrenceID != "" {
		t.Fatalf("semantic local target inherited containing-function ids: %#v", target)
	}
}

func TestNavigationSemanticDefinitionUsesTargetSourceForCrossFileUTF16Range(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "main.go", `package main

func main() { target() }
`)
	writeFile(t, root, "target.go", `package main

func /* é🚀 */ target() {}
`)
	repo := navigationRepository(t, root)
	navigation := NewNavigationService(repo, NewWorkspace(root))
	navigation.SetSemanticProvider(&navigationSemanticProvider{facts: []semantic.SemanticFact{{
		Kind: semantic.KindDefinition,
		Location: semantic.SourceLocation{
			Path: "target.go",
			Range: domain.Range{
				Start: domain.Position{Line: 3, Column: 19},
				End:   domain.Position{Line: 3, Column: 25},
			},
			Encoding: semantic.EncodingUTF8,
		},
		Provenance: semantic.Provenance{Source: semantic.SourceGopls, ProviderID: "gopls", Method: "textDocument/definition"},
		Confidence: semantic.ConfidenceLanguageServerResolved,
	}}})

	result, err := navigation.Query(context.Background(), domain.NavigationRequest{
		Kind: domain.NavigationKindDefinition,
		Path: "main.go",
		Position: &domain.NavigationPosition{
			Line: 3, Column: 15, Encoding: "utf-16",
		},
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if result.Total != 1 || len(result.Targets) != 1 {
		t.Fatalf("cross-file definition total/len = %d/%d, want one merged target: %#v", result.Total, len(result.Targets), result.Targets)
	}
	target := result.Targets[0]
	if target.Label != "target" || target.Path != "target.go" || target.Range.Start.Line != 3 || target.Range.Start.Column != 16 || target.Range.End.Column != 22 {
		t.Fatalf("cross-file UTF-16 target = %#v, want target.go:3:16-22", target)
	}
	sources := map[string]bool{}
	for _, provenance := range target.Provenance {
		sources[provenance.Source] = true
	}
	if !sources["tree-sitter"] || !sources[semantic.SourceGopls] {
		t.Fatalf("cross-file target provenance = %#v, want AST and gopls", target.Provenance)
	}
}

func TestNavigationSemanticDefinitionDoesNotConfuseShadowedLocalsWithIndexedSymbols(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "same-named containing function",
			files: map[string]string{"main.go": `package main

func worker() {
	worker := 1
	_ = worker
}
`},
		},
		{
			name: "unrelated same-named indexed function",
			files: map[string]string{
				"main.go": `package main

func main() {
	worker := 1
	_ = worker
}
`,
				"worker.go": `package main

func worker() {}
`,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for path, content := range test.files {
				writeFile(t, root, path, content)
			}
			repo := navigationRepository(t, root)
			navigation := NewNavigationService(repo, NewWorkspace(root))
			navigation.SetSemanticProvider(&navigationSemanticProvider{facts: []semantic.SemanticFact{{
				Kind: semantic.KindDefinition,
				Location: semantic.SourceLocation{
					Path: "main.go",
					Range: domain.Range{
						Start: domain.Position{Line: 4, Column: 2},
						End:   domain.Position{Line: 4, Column: 8},
					},
					Encoding: semantic.EncodingUTF16,
				},
				Provenance: semantic.Provenance{Source: semantic.SourceGopls, ProviderID: "gopls", Method: "textDocument/definition"},
				Confidence: semantic.ConfidenceLanguageServerResolved,
			}}})

			result, err := navigation.Query(context.Background(), domain.NavigationRequest{
				Kind: domain.NavigationKindDefinition,
				Path: "main.go",
				Position: &domain.NavigationPosition{
					Line: 5, Column: 6, Encoding: "utf-16",
				},
			})
			if err != nil {
				t.Fatalf("Query() error = %v", err)
			}
			if result.Total != 1 || len(result.Targets) != 1 {
				t.Fatalf("shadowed local total/len = %d/%d, want semantic target only: %#v", result.Total, len(result.Targets), result.Targets)
			}
			target := result.Targets[0]
			if target.Label != "worker" || target.Path != "main.go" || target.Range.Start.Line != 4 || target.Range.Start.Column != 2 {
				t.Fatalf("shadowed local target = %#v, want main.go:4:2 worker", target)
			}
			if target.SymbolID != "" || target.OccurrenceID != "" || target.Kind != "" {
				t.Fatalf("shadowed local inherited indexed identity or kind: %#v", target)
			}
			if len(target.Provenance) != 1 || target.Provenance[0].Source != semantic.SourceGopls {
				t.Fatalf("shadowed local provenance = %#v, want only gopls", target.Provenance)
			}
		})
	}
}

func TestNavigationKeepsASTFallbackAndReportsUnsupportedProvider(t *testing.T) {
	t.Parallel()
	root, repo := navigationFixture(t)
	navigation := NewNavigationService(repo, NewWorkspace(root))
	navigation.SetSemanticProvider(&navigationSemanticProvider{err: semantic.CapabilityUnsupported("textDocument/definition")})

	result, err := navigation.Query(context.Background(), domain.NavigationRequest{
		Kind: domain.NavigationKindDefinition, Path: "internal/order/service.go",
		Position: &domain.NavigationPosition{Line: 6, Column: 13, Encoding: "utf-16"},
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if result.Total != 1 || result.SemanticCoverage["coverage"] != "ast_only" || result.SemanticCoverage["providerState"] != "unsupported" {
		t.Fatalf("fallback result = %#v", result)
	}
	if len(result.Omissions) != 1 || result.Omissions[0].Reason != "provider_unsupported" {
		t.Fatalf("omissions = %#v", result.Omissions)
	}
}

func TestByteColumnToUTF16PreservesNavigationRangeAcrossSurrogates(t *testing.T) {
	t.Parallel()
	position := byteColumnToUTF16([]byte("é🚀target\n"), domain.Position{Line: 1, Column: 7})
	if position.Line != 1 || position.Column != 4 || position.Encoding != "utf-16" {
		t.Fatalf("converted position = %#v, want line 1 UTF-16 column 4", position)
	}
}

func TestNavigationSemanticPositionConvertsUTF16InputToByteColumn(t *testing.T) {
	t.Parallel()
	position, ok := navigationSemanticPosition([]byte("é🚀target\n"), &domain.NavigationPosition{Line: 1, Column: 4, Encoding: "utf-16"})
	if !ok || position.Line != 1 || position.Column != 7 || position.Encoding != "utf-8" {
		t.Fatalf("semantic position = %#v, %v, want line 1 UTF-8 byte column 7", position, ok)
	}
}

type navigationSemanticProvider struct {
	facts     []semantic.SemanticFact
	err       error
	lastQuery semantic.SemanticQuery
}

func (p *navigationSemanticProvider) ID() semantic.SemanticProviderID { return "gopls" }
func (p *navigationSemanticProvider) Capabilities(context.Context) (semantic.SemanticCapabilities, error) {
	return semantic.SemanticCapabilities{Definition: true}, nil
}
func (p *navigationSemanticProvider) Definitions(_ context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	p.lastQuery = query
	return p.facts, p.err
}
func (p *navigationSemanticProvider) Hover(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("hover")
}
func (p *navigationSemanticProvider) References(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("references")
}
func (p *navigationSemanticProvider) Implementations(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("implementation")
}
func (p *navigationSemanticProvider) IncomingCalls(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("incomingCalls")
}
func (p *navigationSemanticProvider) OutgoingCalls(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("outgoingCalls")
}
func (p *navigationSemanticProvider) Diagnostics(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("diagnostics")
}

func TestNavigationQueryReturnsZeroResultsForUnresolvedDefinition(t *testing.T) {
	t.Parallel()
	root, repo := navigationFixture(t)
	navigation := NewNavigationService(repo, NewWorkspace(root))

	result, err := navigation.Query(context.Background(), domain.NavigationRequest{
		Kind: domain.NavigationKindDefinition,
		Path: "internal/order/service.go",
		Position: &domain.NavigationPosition{
			Line: 17, Column: 3, Encoding: "utf-16",
		},
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if result.Subject.Name != "missing" {
		t.Fatalf("subject name = %q, want unresolved identifier name", result.Subject.Name)
	}
	if result.Total != 0 || len(result.Targets) != 0 || result.Truncated {
		t.Fatalf("zero-result query returned total=%d len=%d truncated=%v", result.Total, len(result.Targets), result.Truncated)
	}
}

func TestNavigationReferencesDeduplicateSortAndTruncate(t *testing.T) {
	t.Parallel()
	root, repo := navigationFixture(t)
	navigation := NewNavigationService(repo, NewWorkspace(root))
	persistID := symbolIDByName(t, repo, "persist")

	result, err := navigation.Query(context.Background(), domain.NavigationRequest{
		Kind:     domain.NavigationKindReferences,
		SymbolID: persistID,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if result.Subject.Name != "persist" {
		t.Fatalf("subject = %#v, want persist", result.Subject)
	}
	if result.Total != 3 || len(result.Targets) != 2 || !result.Truncated {
		t.Fatalf("references total/len/truncated = %d/%d/%v, want 3/2/true", result.Total, len(result.Targets), result.Truncated)
	}
	if result.Targets[0].Path > result.Targets[1].Path {
		t.Fatalf("targets not sorted by path/range: %#v", result.Targets)
	}
	for _, target := range result.Targets {
		if target.Relationship != "references" || target.Path == "" || target.Range.Start.Line <= 0 {
			t.Fatalf("reference target not mapped to an internal call site: %#v", target)
		}
	}
}

func TestNavigationSymbolIDDoesNotRequireWorkspaceSource(t *testing.T) {
	t.Parallel()
	root, repo := navigationFixture(t)
	navigation := NewNavigationService(repo, NewWorkspace(root))
	persistID := symbolIDByName(t, repo, "persist")

	result, err := navigation.Query(context.Background(), domain.NavigationRequest{
		Kind:     domain.NavigationKindDefinition,
		SymbolID: persistID,
		Path:     "internal/order/missing.go",
	})
	if err != nil {
		t.Fatalf("Query() error = %v, want symbol-id navigation independent of workspace read", err)
	}
	if result.Subject.Name != "persist" || result.Total != 1 || result.Targets[0].Label != "persist" {
		t.Fatalf("result = %#v, want persist definition from index", result)
	}
}

func TestNavigationIncomingAndOutgoingCallsUseDirection(t *testing.T) {
	t.Parallel()
	root, repo := navigationFixture(t)
	navigation := NewNavigationService(repo, NewWorkspace(root))
	submitID := symbolIDByName(t, repo, "Submit")

	incoming, err := navigation.Query(context.Background(), domain.NavigationRequest{
		Kind:     domain.NavigationKindIncomingCalls,
		SymbolID: submitID,
	})
	if err != nil {
		t.Fatalf("incoming Query() error = %v", err)
	}
	if incoming.Total != 1 || incoming.Targets[0].Label != "caller" || incoming.Targets[0].Relationship != "incoming_calls" {
		t.Fatalf("incoming result = %#v, want caller -> Submit", incoming)
	}

	outgoing, err := navigation.Query(context.Background(), domain.NavigationRequest{
		Kind:     domain.NavigationKindOutgoingCalls,
		SymbolID: submitID,
	})
	if err != nil {
		t.Fatalf("outgoing Query() error = %v", err)
	}
	if outgoing.Total != 1 || outgoing.Targets[0].Label != "persist" || outgoing.Targets[0].Relationship != "outgoing_calls" {
		t.Fatalf("outgoing result = %#v, want Submit -> persist", outgoing)
	}
}

func TestNavigationValidationRejectsUnknownKindAndMissingTarget(t *testing.T) {
	t.Parallel()
	root, repo := navigationFixture(t)
	navigation := NewNavigationService(repo, NewWorkspace(root))

	_, err := navigation.Query(context.Background(), domain.NavigationRequest{Kind: "rename"})
	if err == nil {
		t.Fatal("Query() accepted an unknown kind")
	}
	if appErr, ok := apperror.As(err); !ok || appErr.Code != apperror.CodeInvalidArgument {
		t.Fatalf("unknown kind error = %v, want INVALID_ARGUMENT", err)
	}

	_, err = navigation.Query(context.Background(), domain.NavigationRequest{Kind: domain.NavigationKindDefinition})
	if err == nil {
		t.Fatal("Query() accepted a request without symbolId/occurrenceId/path+position")
	}
	if appErr, ok := apperror.As(err); !ok || appErr.Code != apperror.CodeInvalidArgument {
		t.Fatalf("missing target error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestInternalNavigationPathUsesWorkspacePathContract(t *testing.T) {
	t.Parallel()
	if got := internalNavigationPath(`pkg\nested\app.go`); got != "pkg/nested/app.go" {
		t.Fatalf("internalNavigationPath(backslashes) = %q", got)
	}
	for _, unsafe := range []string{"https://example.test/app.go", `..\secret.go`, `C:\Windows\system.ini`, "pkg/\x00app.go"} {
		if got := internalNavigationPath(unsafe); got != "" || isInternalNavigationPath(unsafe) {
			t.Fatalf("unsafe navigation path %q normalized to %q", unsafe, got)
		}
	}
}

func navigationFixture(t *testing.T) (string, repository.Store) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "internal/order/service.go", `package order

type Service struct{}

func (s *Service) Submit() error {
	return persist()
}

func persist() error { return nil }

func caller() {
	service := &Service{}
	_ = service.Submit()
}

func unresolved() {
	missing()
}
`)
	writeFile(t, root, "internal/order/worker.go", `package order

func worker() {
	persist()
}
`)
	writeFile(t, root, "internal/order/report.go", `package order

func report() {
	persist()
}
`)
	repo := navigationRepository(t, root)
	return root, repo
}

func navigationRepository(t *testing.T, root string) repository.Store {
	t.Helper()
	repo, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	backgroundIndexer := indexer.New(root, 1_500_000, codeparser.New(), repo)
	if err := backgroundIndexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	return repo
}

func symbolIDByName(t *testing.T, repo repository.Store, name string) string {
	t.Helper()
	for _, symbol := range repo.AllSymbols() {
		if symbol.Name == name {
			return symbol.ID
		}
	}
	t.Fatalf("symbol %q not found in %#v", name, repo.AllSymbols())
	return ""
}
