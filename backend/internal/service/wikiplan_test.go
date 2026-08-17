package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

func indexedTinycommerceStore(t *testing.T) repository.Store {
	t.Helper()
	store, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "..", "examples", "tinycommerce")
	engine := codeparser.New()
	err = filepath.WalkDir(root, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), ".ts")) {
			return nil
		}
		source, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(root, fullPath)
		if relErr != nil {
			return relErr
		}
		indexSource(t, store, engine, filepath.ToSlash(relative), string(source))
		return nil
	})
	if err != nil {
		t.Fatalf("index tinycommerce: %v", err)
	}
	moduleBytes, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	store.ReplaceFile(domain.ParsedFile{File: domain.File{
		Path: "go.mod", Language: "gomod", Hash: "go.mod", Content: string(moduleBytes),
		Size: int64(len(moduleBytes)), IndexedAt: time.Now(),
	}})
	return store
}

func TestDeepWikiTinycommerceDeterministicPlanMeetsQualityContract(t *testing.T) {
	store := indexedTinycommerceStore(t)
	service := NewDeepWikiService(store, &capturingProvider{response: "Grounded page"})
	service.SetPlannerEnabled(false)

	pages, err := service.Generate(context.Background())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(pages) < 6 {
		t.Fatalf("generated %d pages, want at least 6", len(pages))
	}
	bySlug := make(map[string]domain.WikiPage, len(pages))
	archetypes := make(map[string]int)
	for _, page := range pages {
		bySlug[page.Slug] = page
		archetypes[page.Archetype]++
		if page.SourceHash == "" {
			t.Errorf("page %q has an empty source hash", page.Slug)
		}
		if !strings.Contains(page.Markdown, "<summary>Relevant source files</summary>") {
			t.Errorf("page %q lacks Relevant source files", page.Slug)
		}
		if !strings.Contains(page.Markdown, "**Sources:**") {
			t.Errorf("page %q lacks section sources", page.Slug)
		}
		if page.Archetype == "architecture-overview" || page.Archetype == "module" || page.Archetype == "layer" {
			if page.Diagram == nil || page.Diagram.Source == "" {
				t.Errorf("page %q (%s) lacks a grounded diagram", page.Slug, page.Archetype)
			}
		}
	}
	for _, required := range []string{"overview", "getting-started", "architecture-overview", "frontend", "testing", "glossary"} {
		if archetypes[required] == 0 {
			t.Errorf("missing required %q archetype: %#v", required, archetypes)
		}
	}
	if archetypes["module"]+archetypes["layer"] == 0 {
		t.Errorf("missing backend module/layer pages: %#v", archetypes)
	}
	for _, page := range pages {
		for _, link := range page.RelatedPages {
			target, exists := bySlug[link.Slug]
			if !exists {
				t.Errorf("page %q links to missing page %q", page.Slug, link.Slug)
				continue
			}
			if page.Archetype != "glossary" {
				rendered := "[" + link.Title + " (" + link.Relation + ")](wiki:" + link.Slug + ")"
				if !strings.Contains(page.Markdown, rendered) {
					t.Errorf("page %q Markdown lacks related link %q", page.Slug, rendered)
				}
			}
			switch link.Relation {
			case "parent":
				if page.ParentSlug != target.Slug || !hasWikiLink(target, page.Slug, "child") {
					t.Errorf("page %q and parent %q are not bidirectionally linked", page.Slug, target.Slug)
				}
			case "child":
				if target.ParentSlug != page.Slug || !hasWikiLink(target, page.Slug, "parent") {
					t.Errorf("page %q and child %q are not bidirectionally linked", page.Slug, target.Slug)
				}
			}
		}
	}
	getting := pageWithArchetype(t, pages, "getting-started")
	for _, command := range []string{"go test ./...", "go run ./cmd/api"} {
		if !strings.Contains(getting.Markdown, command) {
			t.Errorf("getting-started lacks %q: %s", command, getting.Markdown)
		}
	}
	glossary := pageWithArchetype(t, pages, "glossary")
	if !strings.Contains(glossary.Markdown, "| Term | Kind | Definition |") {
		t.Errorf("glossary lacks exported-symbol table: %s", glossary.Markdown)
	}
}

func TestDeepWikiInvalidPlannerFallsBackToDeterministicManifest(t *testing.T) {
	store := indexedTinycommerceStore(t)
	service := NewDeepWikiService(store, &capturingProvider{response: "not planner JSON"})
	pages, err := service.Generate(context.Background())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(pages) < 6 {
		t.Fatalf("fallback generated %d pages, want at least 6", len(pages))
	}
	collection := service.Collection()
	if collection.Manifest == nil || !strings.Contains(strings.Join(collection.Manifest.Omissions, " "), "planner output failed validation") {
		t.Fatalf("manifest omissions = %#v, want planner validation fallback", collection.Manifest)
	}
}

func TestDeepWikiPlannerRetryUsesSafeSemanticCategory(t *testing.T) {
	store := indexedTinycommerceStore(t)
	provider := &plannerSequenceProvider{plan: `{
		"schemaVersion":"wiki-plan/v1",
		"pages":[
			{"slug":"overview","title":"Overview","parentSlug":"","scopePaths":["invented.go"],"archetype":"overview"},
			{"slug":"getting","title":"Getting","parentSlug":"overview","scopePaths":["cmd/api/main.go"],"archetype":"getting-started"},
			{"slug":"architecture","title":"Architecture","parentSlug":"overview","scopePaths":["cmd/api/main.go"],"archetype":"architecture-overview"},
			{"slug":"module","title":"Module","parentSlug":"overview","scopePaths":["internal/order/service.go"],"archetype":"module"},
			{"slug":"testing","title":"Testing","parentSlug":"overview","scopePaths":["internal/order/service_test.go"],"archetype":"testing"},
			{"slug":"frontend","title":"Frontend","parentSlug":"overview","scopePaths":["web/src/app.ts"],"archetype":"frontend"},
			{"slug":"glossary","title":"Glossary","parentSlug":"overview","scopePaths":["go.mod"],"archetype":"glossary"}
		]
	}`}
	service := NewDeepWikiService(store, provider)
	if _, err := service.Generate(context.Background()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	requests := provider.Requests()
	var plannerRequests []ai.GenerationRequest
	for _, request := range requests {
		if request.Operation == "deepwiki-plan" {
			plannerRequests = append(plannerRequests, request)
		}
	}
	if len(plannerRequests) != 2 {
		t.Fatalf("planner request count = %d, want 2", len(plannerRequests))
	}
	repairPrompt := plannerRequests[1].UserPrompt
	if !strings.Contains(repairPrompt, "unknown scope path") {
		t.Fatalf("repair prompt lacks safe semantic category: %q", repairPrompt)
	}
	if strings.Contains(repairPrompt, "invented.go") {
		t.Fatalf("repair diagnostic leaked rejected path: %q", repairPrompt)
	}
	omissions := strings.Join(service.Collection().Manifest.Omissions, " ")
	if !strings.Contains(omissions, "unknown scope path") || strings.Contains(omissions, "invented.go") {
		t.Fatalf("manifest omissions = %q, want bounded semantic category", omissions)
	}
}

func TestDeepWikiPlannerRequestUsesKnownPathSchema(t *testing.T) {
	store := indexedTinycommerceStore(t)
	provider := &schemaCapturingWikiProvider{}
	service := NewDeepWikiService(store, provider)
	if _, err := service.Generate(context.Background()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var planner *ai.GenerationRequest
	for _, request := range provider.Requests() {
		if request.Operation == "deepwiki-plan" {
			requestCopy := request
			planner = &requestCopy
			break
		}
	}
	if planner == nil {
		t.Fatal("provider captured no planner request")
	}
	var schema map[string]any
	if err := json.Unmarshal(planner.OutputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	pages := properties["pages"].(map[string]any)
	page := pages["items"].(map[string]any)
	pageProperties := page["properties"].(map[string]any)
	scope := pageProperties["scopePaths"].(map[string]any)
	values, ok := scope["items"].(map[string]any)["enum"].([]any)
	if !ok || len(values) != 10 {
		t.Fatalf("planner path enum = %#v, want 10 tinycommerce paths", scope["items"])
	}
	for _, want := range []string{"cmd/api/main.go", "go.mod", "internal/order/service.go", "web/src/app.ts"} {
		found := false
		for _, value := range values {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("planner path enum omits %q: %#v", want, values)
		}
	}
}

func TestValidateWikiPlanRejectsUnsafeHierarchyAndScope(t *testing.T) {
	base := aiout.WikiPlan{SchemaVersion: aiout.WikiPlanSchemaVersion, Pages: []aiout.WikiPagePlan{
		{Slug: "overview", Title: "Overview", ScopePaths: []string{"main.go"}, Archetype: "overview"},
		{Slug: "getting", Title: "Getting", ParentSlug: "overview", ScopePaths: []string{"main.go"}, Archetype: "getting-started"},
		{Slug: "architecture", Title: "Architecture", ParentSlug: "overview", ScopePaths: []string{"main.go"}, Archetype: "architecture-overview"},
		{Slug: "module", Title: "Module", ParentSlug: "overview", ScopePaths: []string{"main.go"}, Archetype: "module"},
		{Slug: "glossary", Title: "Glossary", ParentSlug: "overview", ScopePaths: []string{"main.go"}, Archetype: "glossary"},
	}}
	if err := validateWikiPlan(base, []string{"main.go"}, nil); err != nil {
		t.Fatalf("valid base plan rejected: %v", err)
	}
	t.Run("unknown scope", func(t *testing.T) {
		plan := cloneWikiPlan(base)
		plan.Pages[3].ScopePaths = []string{"invented.go"}
		if err := validateWikiPlan(plan, []string{"main.go"}, nil); err == nil {
			t.Fatal("unknown scope path accepted")
		}
	})
	t.Run("duplicate scope", func(t *testing.T) {
		plan := cloneWikiPlan(base)
		plan.Pages[3].ScopePaths = []string{"main.go", "main.go"}
		if err := validateWikiPlan(plan, []string{"main.go"}, nil); err == nil {
			t.Fatal("duplicate scope path accepted")
		}
	})
	t.Run("duplicate slug", func(t *testing.T) {
		plan := cloneWikiPlan(base)
		plan.Pages[4].Slug = "module"
		if err := validateWikiPlan(plan, []string{"main.go"}, nil); err == nil {
			t.Fatal("duplicate slug accepted")
		}
	})
	t.Run("depth greater than two", func(t *testing.T) {
		plan := cloneWikiPlan(base)
		plan.Pages = append(plan.Pages,
			aiout.WikiPagePlan{Slug: "child", Title: "Child", ParentSlug: "module", ScopePaths: []string{"main.go"}, Archetype: "layer"},
			aiout.WikiPagePlan{Slug: "grandchild", Title: "Grandchild", ParentSlug: "child", ScopePaths: []string{"main.go"}, Archetype: "layer"},
		)
		if err := validateWikiPlan(plan, []string{"main.go"}, nil); err == nil {
			t.Fatal("hierarchy deeper than two accepted")
		}
	})
	t.Run("disconnected page", func(t *testing.T) {
		plan := cloneWikiPlan(base)
		plan.Pages[3].ParentSlug = ""
		if err := validateWikiPlan(plan, []string{"main.go"}, nil); err == nil {
			t.Fatal("page disconnected from overview accepted")
		}
	})
}

func TestDeepWikiGeneratesPagesConcurrentlyAndReportsProgress(t *testing.T) {
	store := indexedTinycommerceStore(t)
	provider := &delayedWikiProvider{}
	service := NewDeepWikiService(store, provider)
	service.SetPlannerEnabled(false)
	var mu sync.Mutex
	var progress []WikiProgress
	pages, err := service.GenerateWithProgress(context.Background(), func(update WikiProgress) {
		mu.Lock()
		progress = append(progress, update)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("GenerateWithProgress() error = %v", err)
	}
	if provider.maxActive.Load() < 2 {
		t.Fatalf("maximum concurrent page calls = %d, want at least 2", provider.maxActive.Load())
	}
	if len(progress) != len(pages) {
		t.Fatalf("progress updates = %d, pages = %d", len(progress), len(pages))
	}
	last := progress[len(progress)-1]
	if last.Completed != len(pages) || last.Total != len(pages) {
		t.Fatalf("last progress = %#v, want completed total %d", last, len(pages))
	}
}

func TestDeepWikiReportsThePageThatActuallyFailed(t *testing.T) {
	store := indexedTinycommerceStore(t)
	service := NewDeepWikiService(store, failingTestingPageProvider{})
	service.SetPlannerEnabled(false)
	var progress []WikiProgress
	_, err := service.GenerateWithProgress(context.Background(), func(update WikiProgress) {
		progress = append(progress, update)
	})
	if err == nil {
		t.Fatal("GenerateWithProgress() succeeded, want invalid testing page failure")
	}
	var failed *WikiProgress
	for index := range progress {
		if progress[index].Status == WikiProgressFailed {
			failed = &progress[index]
			break
		}
	}
	if failed == nil {
		t.Fatalf("progress = %#v, want a failed-page update", progress)
	}
	if failed.PageSlug != "6-testing" {
		t.Fatalf("failed page = %q, want 6-testing", failed.PageSlug)
	}
	if failed.Err == nil {
		t.Fatal("failed-page update omitted its internal error")
	}
}

func TestDeepWikiConstrainsPageReferencesToRequestAllowlistsInOutputSchema(t *testing.T) {
	store := indexedTinycommerceStore(t)
	provider := &schemaCapturingWikiProvider{}
	service := NewDeepWikiService(store, provider)
	service.SetPlannerEnabled(false)

	if _, err := service.Generate(context.Background()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	requests := provider.Requests()
	if len(requests) == 0 {
		t.Fatal("structured provider captured no DeepWiki page requests")
	}
	for _, request := range requests {
		allowedRelated := allowedRelatedPagesFromPrompt(t, request.UserPrompt)
		gotRelated := relatedPageEnumFromSchema(t, request.OutputSchema)
		if strings.Join(gotRelated, "\n") != strings.Join(allowedRelated, "\n") {
			t.Fatalf("relatedPages enum = %#v, want allowedRelatedPages %#v", gotRelated, allowedRelated)
		}
		allowedEvidence := evidenceIDsFromPrompt(t, request.UserPrompt)
		for field, gotEvidence := range evidenceEnumsFromSchema(t, request.OutputSchema) {
			if strings.Join(gotEvidence, "\n") != strings.Join(allowedEvidence, "\n") {
				t.Fatalf("%s enum = %#v, want ContextPack EvidenceIDs %#v", field, gotEvidence, allowedEvidence)
			}
		}
	}
}

func TestDeepWikiRequestsExpandedOutputTokenBudget(t *testing.T) {
	store := indexedTinycommerceStore(t)
	provider := &schemaCapturingWikiProvider{}
	service := NewDeepWikiService(store, provider)
	service.SetPlannerEnabled(false)

	if _, err := service.Generate(context.Background()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	requests := provider.Requests()
	if len(requests) == 0 {
		t.Fatal("structured provider captured no DeepWiki page requests")
	}
	for _, request := range requests {
		if request.MaxOutputTokens != 16_384 {
			t.Fatalf("DeepWiki output token budget = %d, want 16384", request.MaxOutputTokens)
		}
	}
}

func TestDeepWikiSourceHashesRemainPageScoped(t *testing.T) {
	store := indexedTinycommerceStore(t)
	service := NewDeepWikiService(store, &capturingProvider{response: "Grounded page"})
	service.SetPlannerEnabled(false)
	before, err := service.Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeFrontend := pageWithArchetype(t, before, "frontend")
	beforeBackend := pageWithArchetype(t, before, "module")
	engine := codeparser.New()
	indexSource(t, store, engine, "web/src/api.ts", `export async function submitOrder() { return fetch("/orders") }
export function addedForHashTest() { return true }
`)
	after, err := service.Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	afterFrontend := pageBySlug(t, after, beforeFrontend.Slug)
	afterBackend := pageBySlug(t, after, beforeBackend.Slug)
	if beforeFrontend.SourceHash == afterFrontend.SourceHash {
		t.Fatal("frontend page source hash did not change after a scoped frontend mutation")
	}
	if beforeBackend.SourceHash != afterBackend.SourceHash {
		t.Fatal("backend module source hash changed after an unrelated frontend mutation")
	}
}

func TestResolveWikiEntryScopeExpandsLayerFromCurrentModule(t *testing.T) {
	entry := domain.WikiManifestEntry{
		Slug: "3.2-service-layer", Title: "Service layer", Kind: "layer", Archetype: "layer",
		ModuleSlug: "module-internal-orders", ScopePaths: []string{"internal/orders/order_service.go"},
	}
	modules := []domain.WikiModule{{
		Slug: "module-internal-orders",
		Paths: []string{
			"internal/orders/order.go",
			"internal/orders/order_service.go",
			"internal/orders/payment_service.go",
		},
	}}

	got := resolveWikiEntryScope(entry, nil, modules, nil)
	want := []string{"internal/orders/order_service.go", "internal/orders/payment_service.go"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("service layer scope = %#v, want %#v", got, want)
	}
}

func TestWikiLayerPathsExcludeOnlyRecognizedTestFiles(t *testing.T) {
	t.Parallel()
	spec := wikiLayerSpec{keys: []string{"service"}}
	got := wikiLayerPaths([]string{
		"internal/latest_service.go",
		"internal/contest_service.go",
		"internal/attestation_service.go",
		"internal/service_test.go",
		"web/service.test.ts",
		"web/service.spec.ts",
		"internal/tests/service.go",
	}, spec)
	want := []string{
		"internal/attestation_service.go",
		"internal/contest_service.go",
		"internal/latest_service.go",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("wiki layer paths = %#v, want %#v", got, want)
	}
}

func TestDeepWikiRestartRecomputesLayerDerivedScope(t *testing.T) {
	store := indexedTinycommerceStore(t)
	service := NewDeepWikiService(store, &capturingProvider{response: "Grounded page"})
	service.SetPlannerEnabled(false)
	pages, err := service.Generate(context.Background())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var layer domain.WikiPage
	for _, page := range pages {
		if page.Archetype == "layer" && strings.Contains(page.Slug, "service-layer") {
			layer = page
			break
		}
	}
	if layer.Slug == "" {
		t.Fatal("deterministic manifest has no service-layer page")
	}

	restarted := NewDeepWikiService(store, &capturingProvider{response: "Grounded page"})
	if got := restarted.expectedHashes([]domain.WikiPage{layer})[layer.Slug]; got != layer.SourceHash {
		t.Fatalf("restart hash = %q, want persisted %q before mutation", got, layer.SourceHash)
	}
	engine := codeparser.New()
	indexSource(t, store, engine, "internal/order/payment_service.go", `package order

func ProcessPayment() error { return nil }
`)
	if got := restarted.expectedHashes([]domain.WikiPage{layer})[layer.Slug]; got == layer.SourceHash {
		t.Fatal("service-layer hash ignored a new matching module file after restart")
	}
}

type delayedWikiProvider struct {
	active    atomic.Int32
	maxActive atomic.Int32
}

type failingTestingPageProvider struct{}

type schemaCapturingWikiProvider struct {
	mu       sync.Mutex
	requests []ai.GenerationRequest
}

type plannerSequenceProvider struct {
	mu       sync.Mutex
	plan     string
	requests []ai.GenerationRequest
}

func (p *plannerSequenceProvider) Name() string    { return "planner-sequence" }
func (p *plannerSequenceProvider) Available() bool { return true }
func (p *plannerSequenceProvider) Complete(_ context.Context, systemPrompt, _ string, _ int) (string, error) {
	return structuredStub(systemPrompt, "Grounded page"), nil
}
func (p *plannerSequenceProvider) CompleteStructured(_ context.Context, request ai.GenerationRequest) (ai.GenerationResult, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	if request.Operation == "deepwiki-plan" {
		return ai.GenerationResult{RawJSON: []byte(p.plan), Provider: p.Name()}, nil
	}
	return ai.GenerationResult{RawJSON: []byte(structuredStub(request.SystemPrompt, "Grounded page")), Provider: p.Name()}, nil
}
func (p *plannerSequenceProvider) Embed(context.Context, []string) ([][]float64, error) {
	return nil, ai.ErrUnavailable
}
func (p *plannerSequenceProvider) Requests() []ai.GenerationRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ai.GenerationRequest(nil), p.requests...)
}

func (p *schemaCapturingWikiProvider) Name() string    { return "schema-capturing-wiki" }
func (p *schemaCapturingWikiProvider) Available() bool { return true }
func (p *schemaCapturingWikiProvider) Complete(_ context.Context, systemPrompt, _ string, _ int) (string, error) {
	return structuredStub(systemPrompt, "Grounded page"), nil
}
func (p *schemaCapturingWikiProvider) CompleteStructured(_ context.Context, request ai.GenerationRequest) (ai.GenerationResult, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	return ai.GenerationResult{RawJSON: []byte(structuredStub(request.SystemPrompt, "Grounded page")), Provider: p.Name()}, nil
}
func (p *schemaCapturingWikiProvider) Embed(context.Context, []string) ([][]float64, error) {
	return nil, ai.ErrUnavailable
}
func (p *schemaCapturingWikiProvider) Requests() []ai.GenerationRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ai.GenerationRequest(nil), p.requests...)
}

func (failingTestingPageProvider) Name() string    { return "failing-testing-page" }
func (failingTestingPageProvider) Available() bool { return true }
func (failingTestingPageProvider) Complete(_ context.Context, systemPrompt, userPrompt string, _ int) (string, error) {
	if strings.Contains(systemPrompt, "wiki-page/v4") && strings.Contains(userPrompt, `"slug":"6-testing"`) {
		return `{"schemaVersion":"wiki-page/v4","title":"Testing","sections":[],"relatedPages":[],"inferences":[],"limitations":[{"text":"Runtime behavior is unknown"}]}`, nil
	}
	return structuredStub(systemPrompt, "Grounded page"), nil
}
func (failingTestingPageProvider) Embed(context.Context, []string) ([][]float64, error) {
	return nil, ai.ErrUnavailable
}

func (p *delayedWikiProvider) Name() string    { return "delayed-wiki" }
func (p *delayedWikiProvider) Available() bool { return true }
func (p *delayedWikiProvider) Complete(_ context.Context, systemPrompt, _ string, _ int) (string, error) {
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		maximum := p.maxActive.Load()
		if active <= maximum || p.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	time.Sleep(15 * time.Millisecond)
	return structuredStub(systemPrompt, "Grounded page"), nil
}
func (p *delayedWikiProvider) Embed(context.Context, []string) ([][]float64, error) {
	return nil, ai.ErrUnavailable
}

func cloneWikiPlan(plan aiout.WikiPlan) aiout.WikiPlan {
	cloned := aiout.WikiPlan{SchemaVersion: plan.SchemaVersion, Pages: make([]aiout.WikiPagePlan, len(plan.Pages))}
	copy(cloned.Pages, plan.Pages)
	for index := range cloned.Pages {
		cloned.Pages[index].ScopePaths = append([]string(nil), plan.Pages[index].ScopePaths...)
	}
	return cloned
}

func pageWithArchetype(t *testing.T, pages []domain.WikiPage, archetype string) domain.WikiPage {
	t.Helper()
	for _, page := range pages {
		if page.Archetype == archetype {
			return page
		}
	}
	t.Fatalf("no page with archetype %q in %#v", archetype, pages)
	return domain.WikiPage{}
}

func pageBySlug(t *testing.T, pages []domain.WikiPage, slug string) domain.WikiPage {
	t.Helper()
	for _, page := range pages {
		if page.Slug == slug {
			return page
		}
	}
	t.Fatalf("no page with slug %q", slug)
	return domain.WikiPage{}
}

func hasWikiLink(page domain.WikiPage, slug, relation string) bool {
	for _, link := range page.RelatedPages {
		if link.Slug == slug && link.Relation == relation {
			return true
		}
	}
	return false
}

func allowedRelatedPagesFromPrompt(t *testing.T, prompt string) []string {
	t.Helper()
	const opening = "<CODEATLAS_WIKI_PAGE_PLAN>\n"
	const closing = "\n</CODEATLAS_WIKI_PAGE_PLAN>"
	start := strings.Index(prompt, opening)
	if start < 0 {
		t.Fatalf("wiki page prompt lacks %s", opening)
	}
	start += len(opening)
	end := strings.Index(prompt[start:], closing)
	if end < 0 {
		t.Fatalf("wiki page prompt lacks %s", closing)
	}
	var plan struct {
		AllowedRelatedPages []string `json:"allowedRelatedPages"`
	}
	if err := json.Unmarshal([]byte(prompt[start:start+end]), &plan); err != nil {
		t.Fatalf("decode wiki page plan: %v", err)
	}
	return plan.AllowedRelatedPages
}

func relatedPageEnumFromSchema(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var schema struct {
		Properties struct {
			RelatedPages struct {
				Items struct {
					Enum []string `json:"enum"`
				} `json:"items"`
			} `json:"relatedPages"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode output schema: %v", err)
	}
	if schema.Properties.RelatedPages.Items.Enum == nil {
		t.Fatal("relatedPages output schema has no manifest enum")
	}
	return schema.Properties.RelatedPages.Items.Enum
}

func evidenceIDsFromPrompt(t *testing.T, prompt string) []string {
	t.Helper()
	const opening = "<CODEATLAS_CONTEXT_PACK>\n"
	const closing = "\n</CODEATLAS_CONTEXT_PACK>"
	start := strings.Index(prompt, opening)
	if start < 0 {
		t.Fatalf("wiki page prompt lacks %s", opening)
	}
	start += len(opening)
	end := strings.Index(prompt[start:], closing)
	if end < 0 {
		t.Fatalf("wiki page prompt lacks %s", closing)
	}
	var pack struct {
		Evidence []struct {
			ID string `json:"id"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(prompt[start:start+end]), &pack); err != nil {
		t.Fatalf("decode ContextPack: %v", err)
	}
	ids := make([]string, 0, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		ids = append(ids, evidence.ID)
	}
	return ids
}

func evidenceEnumsFromSchema(t *testing.T, raw json.RawMessage) map[string][]string {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode output schema: %v", err)
	}
	paths := map[string][]string{
		"claim.evidenceIds":         {"$defs", "claim", "properties", "evidenceIds", "items"},
		"inference.evidenceIds":     {"$defs", "inference", "properties", "evidenceIds", "items"},
		"uncertainty.evidenceIds":   {"$defs", "uncertainty", "properties", "evidenceIds", "items"},
		"limitation.evidenceIds":    {"$defs", "wikiLimitation", "properties", "evidenceIds", "items"},
		"section.codeEvidenceIds":   {"properties", "sections", "items", "properties", "codeEvidenceIds", "items"},
		"section.table.evidenceIds": {"properties", "sections", "items", "properties", "tables", "items", "properties", "evidenceIds", "items"},
	}
	result := make(map[string][]string, len(paths))
	for name, path := range paths {
		current := schema
		for _, key := range path {
			value, exists := current[key]
			if !exists {
				t.Fatalf("schema path %s lacks %q", name, key)
			}
			current, _ = value.(map[string]any)
			if current == nil {
				t.Fatalf("schema path %s key %q is not an object", name, key)
			}
		}
		values, ok := current["enum"].([]any)
		if !ok {
			t.Fatalf("schema path %s has no enum", name)
		}
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				t.Fatalf("schema path %s enum contains %#v", name, value)
			}
			result[name] = append(result[name], text)
		}
	}
	return result
}
