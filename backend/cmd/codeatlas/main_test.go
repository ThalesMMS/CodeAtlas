package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/config"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/gopls"
	"github.com/ThalesMMS/CodeAtlas/internal/pythonlsp"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
	"github.com/ThalesMMS/CodeAtlas/internal/rustlsp"
	"github.com/ThalesMMS/CodeAtlas/internal/settings"
	"github.com/ThalesMMS/CodeAtlas/internal/swiftlsp"
	"github.com/ThalesMMS/CodeAtlas/internal/typescriptlsp"
)

func TestNewSettingsTokenUsesThirtyTwoRandomBytes(t *testing.T) {
	first, err := newSettingsToken()
	if err != nil {
		t.Fatalf("newSettingsToken() error = %v", err)
	}
	second, err := newSettingsToken()
	if err != nil {
		t.Fatalf("newSettingsToken() second error = %v", err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("decoded token length = %d, want 32", len(decoded))
	}
	if first == second {
		t.Fatal("newSettingsToken() returned the same token twice")
	}
}

type startupDocumentStore struct{ document settings.Document }

func (s startupDocumentStore) Load(context.Context) (settings.Document, error) {
	return s.document, nil
}
func (s startupDocumentStore) Save(context.Context, settings.Document) error { return nil }

type startupCredentialStore struct{ values map[string]string }

func (s startupCredentialStore) Get(_ context.Context, account string) (string, error) {
	value, ok := s.values[account]
	if !ok {
		return "", settings.ErrCredentialNotFound
	}
	return value, nil
}
func (s startupCredentialStore) Set(context.Context, string, string) error { return nil }
func (s startupCredentialStore) Delete(context.Context, string) error      { return nil }

func TestStartupAppliesSavedOverridesBeforeComposition(t *testing.T) {
	environmentWorkspace := t.TempDir()
	savedWorkspace := t.TempDir()
	cfg := config.Config{
		Workspace: environmentWorkspace, DatabasePath: filepath.Join(environmentWorkspace, ".codeatlas", "codeatlas.db"),
		ListenAddress: "127.0.0.1:8080", MaxFileBytes: 100, LLMBaseURL: "https://env.test/v1", LLMModel: "env-model",
	}
	environment := settings.Environment{
		settings.FieldWorkspace: environmentWorkspace, settings.FieldListen: cfg.ListenAddress,
		settings.FieldMaxFileBytes: "100", settings.FieldLLMBaseURL: cfg.LLMBaseURL, settings.FieldLLMModel: cfg.LLMModel,
	}
	document := settings.Document{
		SchemaVersion: settings.SettingsSchemaVersion, Revision: 7,
		Overrides: settings.Overrides{
			settings.FieldWorkspace: savedWorkspace, settings.FieldMaxFileBytes: int64(200), settings.FieldLLMModel: "saved-model",
		},
		Credentials: settings.CredentialReferences{LLMAPIKeyGeneration: "saved-generation"},
	}
	loaded, err := loadStartupSettings(context.Background(), cfg, environment, startupDocumentStore{document}, startupCredentialStore{
		values: map[string]string{"llm-api-key:saved-generation": "vault-secret"},
	}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Workspace != savedWorkspace || loaded.Config.LLMModel != "saved-model" || loaded.Config.MaxFileBytes != 200 {
		t.Fatalf("startup config = %#v", loaded.Config)
	}
	if loaded.Config.LLMAPIKey != "vault-secret" || loaded.Resolved.Source(settings.FieldLLMAPIKey) != settings.SourceSettings {
		t.Fatal("vault credential was not resolved as a saved override")
	}
	wantDatabase := filepath.Join(savedWorkspace, ".codeatlas", "codeatlas.db")
	if loaded.Config.DatabasePath != wantDatabase {
		t.Fatalf("database path = %q, want %q", loaded.Config.DatabasePath, wantDatabase)
	}
}

type embeddingReconcileProvider struct {
	mu        sync.Mutex
	dimension int
	calls     int
}

func TestWorkspaceHasGoFilesSkipsGeneratedTrees(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vendor", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vendor", "dep", "ignored.go"), []byte("package dep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := workspaceHasGoFiles(root); err != nil || found {
		t.Fatalf("generated-only tree = found %v err %v", found, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal", "order"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "order", "service.go"), []byte("package order\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := workspaceHasGoFiles(root); err != nil || !found {
		t.Fatalf("workspace Go file = found %v err %v", found, err)
	}
}

func TestGoplsCapabilityIsOptionalWhenUnavailable(t *testing.T) {
	manager := gopls.NewManager(gopls.Config{Enable: gopls.EnableTrue, Path: "codeatlas-test-missing-gopls"}, t.TempDir(), nil)
	if err := manager.Start(context.Background(), true); err == nil {
		t.Fatal("missing gopls should fail its probe")
	}
	result := goplsCapability(manager, time.Millisecond)
	if result.Requirement != capabilities.Optional || result.State != capabilities.CapabilityUnavailable || result.ErrorCode != gopls.CodeNotFound {
		t.Fatalf("gopls capability = %+v", result)
	}
}

func TestGoplsCapabilityDoesNotExposeExecutablePath(t *testing.T) {
	manager := gopls.NewManager(gopls.Config{Enable: gopls.EnableFalse, Path: "/private/custom/gopls"}, t.TempDir(), nil)
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	result := goplsCapability(manager, time.Millisecond)
	for key, value := range result.Metadata {
		if strings.Contains(key, "path") || strings.Contains(value, "/private/") {
			t.Fatalf("private executable path leaked in capability metadata: %q=%q", key, value)
		}
	}
}

func TestWorkspaceHasJSTSFilesSkipsGeneratedTrees(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "dependency"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "dependency", "ignored.ts"), []byte("export {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := workspaceHasJSTSFiles(root); err != nil || found {
		t.Fatalf("generated-only tree = found %v err %v", found, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "web", "checkout.TSX"), []byte("export const Checkout = () => null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := workspaceHasJSTSFiles(root); err != nil || !found {
		t.Fatalf("workspace TSX file = found %v err %v", found, err)
	}
}

func TestTypeScriptLSPCapabilityIsOptionalWhenUnavailable(t *testing.T) {
	manager := typescriptlsp.NewManager(typescriptlsp.Config{Enable: typescriptlsp.EnableTrue, Path: "codeatlas-test-missing-typescript-lsp"}, t.TempDir(), nil)
	if err := manager.Start(context.Background(), true); err == nil {
		t.Fatal("missing TypeScript LSP should fail its probe")
	}
	result := typeScriptLSPCapability(manager, time.Millisecond)
	if result.Requirement != capabilities.Optional || result.State != capabilities.CapabilityUnavailable || result.ErrorCode != typescriptlsp.CodeNotFound {
		t.Fatalf("typescript lsp capability = %+v", result)
	}
}

func TestWorkspaceHasSwiftFilesSkipsGeneratedTrees(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build", "Ignored.swift"), []byte("struct Ignored {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := workspaceHasSwiftFiles(root); err != nil || found {
		t.Fatalf("generated-only Swift tree = found %v err %v", found, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Sources", "Order.SWIFT"), []byte("struct Order {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := workspaceHasSwiftFiles(root); err != nil || !found {
		t.Fatalf("workspace Swift file = found %v err %v", found, err)
	}
}

func TestSourceKitLSPCapabilityIsOptionalWhenUnavailable(t *testing.T) {
	manager := swiftlsp.NewManager(swiftlsp.Config{Enable: swiftlsp.EnableTrue, Path: "codeatlas-test-missing-sourcekit-lsp"}, t.TempDir(), nil)
	if err := manager.Start(context.Background(), true); err == nil {
		t.Fatal("missing SourceKit-LSP should fail its probe")
	}
	result := swiftLSPCapability(manager, time.Millisecond)
	if result.Requirement != capabilities.Optional || result.State != capabilities.CapabilityUnavailable || result.ErrorCode != swiftlsp.CodeNotFound {
		t.Fatalf("SourceKit-LSP capability = %+v", result)
	}
}

func TestWorkspaceHasPythonFilesSkipsGeneratedAndEnvironmentTrees(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"build", ".venv"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, directory, "ignored.py"), []byte("value = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if found, err := workspaceHasPythonFiles(root); err != nil || found {
		t.Fatalf("generated/environment-only Python tree = found %v err %v", found, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "commerce"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "commerce", "order.PY"), []byte("class Order:\n    pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := workspaceHasPythonFiles(root); err != nil || !found {
		t.Fatalf("workspace Python file = found %v err %v", found, err)
	}
}

func TestPythonLSPCapabilityIsOptionalWhenUnavailable(t *testing.T) {
	manager := pythonlsp.NewManager(pythonlsp.Config{Enable: pythonlsp.EnableTrue, Path: "codeatlas-test-missing-python-lsp"}, t.TempDir(), nil)
	if err := manager.Start(context.Background(), true); err == nil {
		t.Fatal("missing Pyright language server should fail its probe")
	}
	result := pythonLSPCapability(manager, time.Millisecond)
	if result.Requirement != capabilities.Optional || result.State != capabilities.CapabilityUnavailable || result.ErrorCode != pythonlsp.CodeNotFound {
		t.Fatalf("Python LSP capability = %+v", result)
	}
}

func TestWorkspaceHasRustFilesSkipsGeneratedTargetTree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "target", "debug"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target", "debug", "ignored.rs"), []byte("struct Ignored;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := workspaceHasRustFiles(root); err != nil || found {
		t.Fatalf("generated-only Rust tree = found %v err %v", found, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "lib.RS"), []byte("pub struct Order;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := workspaceHasRustFiles(root); err != nil || !found {
		t.Fatalf("workspace Rust file = found %v err %v", found, err)
	}
}

func TestRustAnalyzerCapabilityIsOptionalWhenUnavailable(t *testing.T) {
	manager := rustlsp.NewManager(rustlsp.Config{Enable: rustlsp.EnableTrue, Path: "codeatlas-test-missing-rust-analyzer"}, t.TempDir(), nil)
	if err := manager.Start(context.Background(), true); err == nil {
		t.Fatal("missing rust-analyzer should fail its probe")
	}
	result := rustLSPCapability(manager, time.Millisecond)
	if result.Requirement != capabilities.Optional || result.State != capabilities.CapabilityUnavailable || result.ErrorCode != rustlsp.CodeNotFound {
		t.Fatalf("Rust LSP capability = %+v", result)
	}
	for key, value := range result.Metadata {
		if strings.Contains(strings.ToLower(key), "path") || strings.Contains(value, "/private/") {
			t.Fatalf("executable path leaked in capability metadata: %q=%q", key, value)
		}
	}
}

func (p *embeddingReconcileProvider) Name() string    { return "test-embeddings" }
func (p *embeddingReconcileProvider) Available() bool { return true }
func (p *embeddingReconcileProvider) Complete(context.Context, string, string, int) (string, error) {
	return "", errors.New("completion endpoint should not be called")
}

func (p *embeddingReconcileProvider) Embed(_ context.Context, texts []string) ([][]float64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	out := make([][]float64, len(texts))
	for i := range texts {
		vector := make([]float64, p.dimension)
		for j := range vector {
			vector[j] = float64(i+j+1) / 10
		}
		out[i] = vector
	}
	return out, nil
}

func (p *embeddingReconcileProvider) resetCalls() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = 0
}

func (p *embeddingReconcileProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func newEmbeddingReconcileStore(t *testing.T) repository.Store {
	t.Helper()
	store, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.ReplaceFile(domain.ParsedFile{
		File: domain.File{Path: "service.go", Language: "go", Hash: "service", IndexedAt: now},
		Symbols: []domain.Symbol{
			{ID: "service.go:file", Path: "service.go", Name: "service.go", Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 8, Column: 1}}},
			{ID: "submit", Path: "service.go", Name: "SubmitOrder", QualifiedName: "service.go::SubmitOrder", Kind: "function", Range: domain.Range{Start: domain.Position{Line: 2, Column: 1}, End: domain.Position{Line: 4, Column: 1}}, Signature: "func SubmitOrder()", Summary: "Submits an order.", Code: "func SubmitOrder() {}"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestReconcileEmbeddingsInvalidatesWhenEmbeddingBaseURLChanges(t *testing.T) {
	store := newEmbeddingReconcileStore(t)
	provider := &embeddingReconcileProvider{dimension: 4}
	retriever := retrieval.NewHybrid(store, provider, true)
	registry := capabilities.NewRegistry()

	first := config.Config{
		EnableEmbeddings: true,
		EmbeddingModel:   "same-model",
		LLMBaseURL:       "http://chat.invalid/v1",
		EmbeddingBaseURL: "http://embed-a.invalid/v1",
	}
	needsRebuild, err := reconcileEmbeddings(retriever, store, registry, first)(context.Background())
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if !needsRebuild {
		t.Fatal("an empty dense index must request a background rebuild")
	}
	if state := embeddingCapabilityState(registry); state != "rebuilding" {
		t.Fatalf("capability state = %q, want rebuilding", state)
	}
	if err := retriever.RebuildEmbeddings(context.Background()); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	firstProvider := store.EmbeddingMetadata().Provider
	if strings.Contains(firstProvider, "embed-a.invalid") {
		t.Fatalf("metadata provider %q leaked the embedding endpoint", firstProvider)
	}

	provider.resetCalls()
	second := first
	second.EmbeddingBaseURL = "http://embed-b.invalid/v1"
	needsRebuild, err = reconcileEmbeddings(retriever, store, registry, second)(context.Background())
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !needsRebuild {
		t.Fatal("an endpoint change must request a background rebuild")
	}
	if err := retriever.RebuildEmbeddings(context.Background()); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if provider.callCount() <= 1 {
		t.Fatalf("Embed calls after endpoint change = %d, want probe plus rebuild", provider.callCount())
	}
	secondProvider := store.EmbeddingMetadata().Provider
	if secondProvider == firstProvider {
		t.Fatalf("metadata provider = %q after endpoint change, want a different fingerprint", secondProvider)
	}
	if strings.Contains(secondProvider, "embed-b.invalid") {
		t.Fatalf("metadata provider %q leaked the embedding endpoint", secondProvider)
	}
}

func embeddingCapabilityState(registry *capabilities.Registry) string {
	for _, result := range registry.Results() {
		if result.ID == "llm-embeddings" {
			return result.Metadata["state"]
		}
	}
	return ""
}
