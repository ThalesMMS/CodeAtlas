package retrieval

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

// reconcileProvider is a configurable embedding provider: it returns one vector
// of the declared dimension per input text, so the dimension probe and the
// rebuild both succeed unless failAfter is set.
type reconcileProvider struct {
	mu         sync.Mutex
	available  bool
	dimension  int
	failAfter  int // when >0, Embed errors after this many successful calls
	embedCalls int
}

func (p *reconcileProvider) Name() string    { return "openai-compatible" }
func (p *reconcileProvider) Available() bool { return p.available }
func (p *reconcileProvider) Complete(context.Context, string, string, int) (string, error) {
	return "", errors.New("completion endpoint should not be called")
}

func (p *reconcileProvider) Embed(_ context.Context, texts []string) ([][]float64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.embedCalls++
	if p.failAfter > 0 && p.embedCalls > p.failAfter {
		return nil, errors.New("embedding endpoint unavailable")
	}
	out := make([][]float64, len(texts))
	for i := range texts {
		vec := make([]float64, p.dimension)
		for j := range vec {
			vec[j] = float64((i+j)%7) * 0.1
		}
		out[i] = vec
	}
	return out, nil
}

func (p *reconcileProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.embedCalls
}

func (p *reconcileProvider) resetCalls() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.embedCalls = 0
}

func lineRange(start, end int) domain.Range {
	return domain.Range{Start: domain.Position{Line: start, Column: 1}, End: domain.Position{Line: end, Column: 1}}
}

func TestSymbolEmbeddingTextV2IncludesDocCommentBeforeSummary(t *testing.T) {
	t.Parallel()
	text := SymbolEmbeddingTextV2(domain.Symbol{DocComment: "declared intent", Summary: "synthetic calls"})
	doc := strings.Index(text, "declared intent")
	summary := strings.Index(text, "synthetic calls")
	if EmbeddingTemplateVersion != "v2" || doc < 0 || summary < 0 || doc >= summary {
		t.Fatalf("embedding template/version does not prioritize docs: version=%q text=%q", EmbeddingTemplateVersion, text)
	}
}

// reconcileStore indexes a file with three non-file symbols so a rebuild
// produces several vectors.
func reconcileStore(t *testing.T) repository.Store {
	t.Helper()
	repository, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	repository.ReplaceFile(domain.ParsedFile{
		File: domain.File{Path: "service.go", Language: "go", Hash: "service", IndexedAt: time.Now()},
		Symbols: []domain.Symbol{
			{ID: "service.go:file", Path: "service.go", Name: "service.go", Kind: "file", Range: lineRange(1, 30)},
			{ID: "submit", Path: "service.go", Name: "SubmitOrder", QualifiedName: "service.go::SubmitOrder", Kind: "function", Range: lineRange(1, 5), Signature: "func SubmitOrder()", Summary: "Submits an order.", Code: "func SubmitOrder() {}"},
			{ID: "cancel", Path: "service.go", Name: "CancelOrder", QualifiedName: "service.go::CancelOrder", Kind: "function", Range: lineRange(6, 10), Signature: "func CancelOrder()", Summary: "Cancels an order.", Code: "func CancelOrder() {}"},
			{ID: "lookup", Path: "service.go", Name: "LookupOrder", QualifiedName: "service.go::LookupOrder", Kind: "function", Range: lineRange(11, 15), Signature: "func LookupOrder()", Summary: "Looks up an order.", Code: "func LookupOrder() {}"},
		},
	})
	return repository
}

func nonFileSymbolCount(repository repository.Store) int {
	n := 0
	for _, symbol := range repository.AllSymbols() {
		if symbol.Kind != "file" {
			n++
		}
	}
	return n
}

func TestReconcileDisabledIsNoOp(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	provider := &reconcileProvider{available: true, dimension: 4}
	retriever := NewHybrid(repository, provider, false)

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if provider.calls() != 0 {
		t.Fatalf("Embed calls = %d, want 0 when disabled", provider.calls())
	}
	if repository.EmbeddingCount() != 0 {
		t.Fatalf("EmbeddingCount = %d, want 0 when disabled", repository.EmbeddingCount())
	}
}

func TestReconcileFailsWhenProviderUnavailable(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	provider := &reconcileProvider{available: false, dimension: 4}
	retriever := NewHybrid(repository, provider, true)

	err := retriever.Reconcile(context.Background(), "openai-compatible", "default")
	if !errors.Is(err, ai.ErrUnavailable) {
		t.Fatalf("Reconcile() error = %v, want ai.ErrUnavailable", err)
	}
}

func TestReconcileBuildsFreshIndex(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	provider := &reconcileProvider{available: true, dimension: 4}
	retriever := NewHybrid(repository, provider, true)

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got, want := repository.EmbeddingCount(), nonFileSymbolCount(repository); got != want {
		t.Fatalf("EmbeddingCount = %d, want %d (one per non-file symbol)", got, want)
	}
	md := repository.EmbeddingMetadata()
	if !md.Enabled || md.Provider != "openai-compatible" || md.Model != "default" {
		t.Fatalf("metadata = %+v, want enabled openai-compatible/default", md)
	}
	if md.Dimension != 4 || md.TemplateVersion != EmbeddingTemplateVersion || md.Distance != EmbeddingDistance {
		t.Fatalf("metadata = %+v, want dim 4 / template %s / distance %s", md, EmbeddingTemplateVersion, EmbeddingDistance)
	}
	if md.BuiltAt.IsZero() {
		t.Fatal("metadata BuiltAt is zero; want a build timestamp")
	}
	for id, vec := range repository.Embeddings() {
		if len(vec) != 4 {
			t.Fatalf("vector for %s has dimension %d, want 4", id, len(vec))
		}
	}
}

func TestReconcileSkipsRebuildWhenCompatible(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	provider := &reconcileProvider{available: true, dimension: 4}
	retriever := NewHybrid(repository, provider, true)

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err != nil {
		t.Fatalf("Reconcile(first) error = %v", err)
	}
	first := repository.EmbeddingMetadata()
	provider.resetCalls()

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err != nil {
		t.Fatalf("Reconcile(second) error = %v", err)
	}
	if provider.calls() != 1 {
		t.Fatalf("Embed calls = %d on compatible reconcile, want 1 (probe only, no rebuild)", provider.calls())
	}
	if !repository.EmbeddingMetadata().BuiltAt.Equal(first.BuiltAt) {
		t.Fatal("BuiltAt changed on a compatible reconcile; index was rebuilt unnecessarily")
	}
}

func TestRebuildEmbeddingsRegeneratesCompatibleIndex(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	provider := &reconcileProvider{available: true, dimension: 4}
	retriever := NewHybrid(repository, provider, true)

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	provider.resetCalls()
	if err := retriever.RebuildEmbeddings(context.Background()); err != nil {
		t.Fatalf("RebuildEmbeddings() error = %v", err)
	}
	if provider.calls() <= 1 {
		t.Fatalf("Embed calls = %d, want probe plus full regeneration", provider.calls())
	}
	if got, want := repository.EmbeddingCount(), nonFileSymbolCount(repository); got != want {
		t.Fatalf("EmbeddingCount = %d, want %d after rebuild", got, want)
	}
}

func TestReconcileRebuildsOnDimensionChange(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	provider := &reconcileProvider{available: true, dimension: 4}
	retriever := NewHybrid(repository, provider, true)

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err != nil {
		t.Fatalf("Reconcile(first) error = %v", err)
	}
	provider.dimension = 8
	provider.resetCalls()

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err != nil {
		t.Fatalf("Reconcile(after dimension change) error = %v", err)
	}
	if provider.calls() <= 1 {
		t.Fatalf("Embed calls = %d, want >1 (probe + rebuild) after a dimension change", provider.calls())
	}
	if md := repository.EmbeddingMetadata(); md.Dimension != 8 {
		t.Fatalf("metadata dimension = %d, want 8 after rebuild", md.Dimension)
	}
	for id, vec := range repository.Embeddings() {
		if len(vec) != 8 {
			t.Fatalf("vector for %s has dimension %d, want 8 after rebuild", id, len(vec))
		}
	}
}

func TestReconcileRebuildsOnModelChange(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	provider := &reconcileProvider{available: true, dimension: 4}
	retriever := NewHybrid(repository, provider, true)

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "model-a"); err != nil {
		t.Fatalf("Reconcile(model-a) error = %v", err)
	}
	provider.resetCalls()

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "model-b"); err != nil {
		t.Fatalf("Reconcile(model-b) error = %v", err)
	}
	if provider.calls() <= 1 {
		t.Fatalf("Embed calls = %d, want >1 (probe + rebuild) after a model change", provider.calls())
	}
	if md := repository.EmbeddingMetadata(); md.Model != "model-b" {
		t.Fatalf("metadata model = %q, want model-b after rebuild", md.Model)
	}
}

func TestReconcileRebuildsLegacyTemplate(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	// Seed a legacy index whose only incompatibility is an older template version.
	legacyVectors := make(map[string][]float64)
	for _, symbol := range repository.AllSymbols() {
		if symbol.Kind == "file" {
			continue
		}
		legacyVectors[symbol.ID] = []float64{0.1, 0.2, 0.3, 0.4}
	}
	legacy := domain.EmbeddingIndexMetadata{
		Enabled: true, Provider: "openai-compatible", Model: "default",
		Dimension: 4, TemplateVersion: "v0", Distance: EmbeddingDistance, BuiltAt: time.Now().UTC(),
	}
	prepared, err := repository.PrepareEmbeddingRebuild(legacyVectors, legacy)
	if err != nil {
		t.Fatalf("seed PrepareEmbeddingRebuild() error = %v", err)
	}
	if _, err := repository.CommitPrepared(prepared); err != nil {
		t.Fatalf("seed CommitPrepared() error = %v", err)
	}

	provider := &reconcileProvider{available: true, dimension: 4}
	retriever := NewHybrid(repository, provider, true)
	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if md := repository.EmbeddingMetadata(); md.TemplateVersion != EmbeddingTemplateVersion {
		t.Fatalf("metadata template = %q, want %q after rebuild", md.TemplateVersion, EmbeddingTemplateVersion)
	}
	if provider.calls() <= 1 {
		t.Fatalf("Embed calls = %d, want >1 (probe + rebuild) for a legacy template", provider.calls())
	}
}

// An incomplete legacy snapshot — vectors present, compatibility metadata
// absent — is always rebuilt rather than trusted by inferring config.
func TestReconcileRebuildsLegacyIndexWithIncompleteMetadata(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	legacy := make(map[string][]float64)
	for _, symbol := range repository.AllSymbols() {
		if symbol.Kind == "file" {
			continue
		}
		legacy[symbol.ID] = []float64{0.1, 0.2, 0.3, 0.4}
	}
	prepared, err := repository.PrepareEmbeddingRebuild(legacy, domain.EmbeddingIndexMetadata{Dimension: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CommitPrepared(prepared); err != nil {
		t.Fatal(err)
	}
	if !repository.EmbeddingMetadata().BuiltAt.IsZero() {
		t.Fatal("seeded a legacy index but metadata is present")
	}
	if repository.EmbeddingCount() == 0 {
		t.Fatal("legacy index seed produced no vectors")
	}

	provider := &reconcileProvider{available: true, dimension: 4}
	retriever := NewHybrid(repository, provider, true)
	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	md := repository.EmbeddingMetadata()
	if !md.Enabled || md.Dimension != 4 || md.TemplateVersion != EmbeddingTemplateVersion {
		t.Fatalf("legacy index not rebuilt with metadata: %+v", md)
	}
	if md.BuiltAt.IsZero() {
		t.Fatal("rebuild did not stamp BuiltAt on the legacy index")
	}
	if provider.calls() <= 1 {
		t.Fatalf("Embed calls = %d, want >1 (probe + rebuild) for a legacy index", provider.calls())
	}
}

// Incremental refresh must reject a batch whose dimension differs from the
// established index instead of silently mixing it in.
func TestRefreshEmbeddingsRejectsDimensionMismatch(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	provider := &reconcileProvider{available: true, dimension: 4}
	retriever := NewHybrid(repository, provider, true)
	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	repository.ReplaceFile(domain.ParsedFile{
		File: domain.File{Path: "extra.go", Language: "go", Hash: "extra"},
		Symbols: []domain.Symbol{
			{ID: "extra.go:file", Path: "extra.go", Name: "extra.go", Kind: "file", Range: lineRange(1, 5)},
			{ID: "newfn", Path: "extra.go", Name: "NewFn", QualifiedName: "extra.go::NewFn", Kind: "function", Range: lineRange(1, 3), Signature: "func NewFn()", Code: "func NewFn() {}"},
		},
	})
	provider.dimension = 8 // a provider now returning a different dimension

	err := retriever.RefreshEmbeddings(context.Background(), []domain.Symbol{
		{ID: "newfn", Path: "extra.go", Name: "NewFn", Kind: "function"},
	})
	if err == nil {
		t.Fatal("RefreshEmbeddings accepted a dimension-mismatched vector; want rejection")
	}
	if _, ok := repository.Embeddings()["newfn"]; ok {
		t.Fatal("mismatched vector was published into the index")
	}
}

func TestReconcileRebuildFailureLeavesStoreUntouched(t *testing.T) {
	t.Parallel()
	repository := reconcileStore(t)
	// failAfter:1 lets the dimension probe succeed but fails the rebuild embeds.
	provider := &reconcileProvider{available: true, dimension: 4, failAfter: 1}
	retriever := NewHybrid(repository, provider, true)

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "default"); err == nil {
		t.Fatal("Reconcile() succeeded despite a failing rebuild; want error")
	}
	if repository.EmbeddingCount() != 0 {
		t.Fatalf("EmbeddingCount = %d after failed rebuild, want 0 (no partial index)", repository.EmbeddingCount())
	}
	if repository.EmbeddingMetadata().Enabled {
		t.Fatal("metadata was published despite a failed rebuild")
	}
}
