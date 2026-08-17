package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/app"
	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/config"
	"github.com/ThalesMMS/CodeAtlas/internal/gopls"
	"github.com/ThalesMMS/CodeAtlas/internal/httpapi"
	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	"github.com/ThalesMMS/CodeAtlas/internal/mutation"
	"github.com/ThalesMMS/CodeAtlas/internal/observability"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/pythonlsp"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
	"github.com/ThalesMMS/CodeAtlas/internal/rustlsp"
	"github.com/ThalesMMS/CodeAtlas/internal/scheduler"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
	"github.com/ThalesMMS/CodeAtlas/internal/semevidence"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
	"github.com/ThalesMMS/CodeAtlas/internal/storemigrate"
	"github.com/ThalesMMS/CodeAtlas/internal/swiftlsp"
	"github.com/ThalesMMS/CodeAtlas/internal/typescriptlsp"
	"github.com/ThalesMMS/CodeAtlas/internal/watcher"
)

func main() {
	os.Exit(run())
}

// run is the composition root. It constructs the concrete dependencies and hands
// the lifecycle to app.Run, owning only the process exit code so internal
// packages never call os.Exit.
func run() int {
	if len(os.Args) > 1 && os.Args[1] == "store" {
		return runStoreCommand(os.Args[2:])
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return 1
	}

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	journalDir := filepath.Join(filepath.Dir(cfg.DatabasePath), "transactions")
	legacyJSONPath := filepath.Join(filepath.Dir(cfg.DatabasePath), "index.json")
	storeRef := repository.NewRef()
	defer storeRef.Close()

	metrics := observability.NewMetrics()
	rawProvider := ai.NewOpenAICompatible(ai.Options{
		BaseURL:               cfg.LLMBaseURL,
		EmbeddingBaseURL:      cfg.EmbeddingBaseURL,
		APIKey:                cfg.LLMAPIKey,
		EmbeddingsAPIKey:      cfg.EmbeddingsAPIKey,
		Model:                 cfg.LLMModel,
		ReasoningEffort:       cfg.LLMReasoningEffort,
		EmbeddingModel:        cfg.EmbeddingModel,
		EnableEmbeddings:      cfg.EnableEmbeddings,
		StructuredProbeSchema: aiout.ExplanationSchema(),
		ProbeTimeout:          cfg.ProbeTimeout,
		RequestTimeout:        cfg.LLMTimeout,
	})
	// Business calls go through the observing decorator (sanitized logs + metrics);
	// the raw provider is retained only for the typed capability probe.
	provider := observability.ObserveProvider(rawProvider, logger, metrics)
	workspace := service.NewWorkspace(cfg.Workspace)
	parserEngine := codeparser.New()
	coordinator := readiness.NewCoordinator()
	coordinator.SetEventDropObserver(metrics.SSEEventDropped)
	registry := capabilities.NewRegistry()
	sourceExtensions, sourceScanErr := workspaceSourceExtensions(cfg.Workspace)
	goplsManager, goplsProvider := startGopls(rootContext, cfg, workspace, registry, logger, hasSourceExtension(sourceExtensions, ".go"), sourceScanErr)
	typeScriptManager, typeScriptProvider := startTypeScriptLSP(rootContext, cfg, workspace, registry, logger, hasAnySourceExtension(sourceExtensions, ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts"), sourceScanErr)
	swiftManager, swiftProvider := startSwiftLSP(rootContext, cfg, workspace, registry, logger, hasSourceExtension(sourceExtensions, ".swift"), sourceScanErr)
	pythonManager, pythonProvider := startPythonLSP(rootContext, cfg, workspace, registry, logger, hasSourceExtension(sourceExtensions, ".py"), sourceScanErr)
	rustManager, rustProvider := startRustLSP(rootContext, cfg, workspace, registry, logger, hasSourceExtension(sourceExtensions, ".rs"), sourceScanErr)
	semanticProvider := semantic.NewPathRouter(map[string]semantic.SemanticProvider{
		".go":    goplsProvider,
		".js":    typeScriptProvider,
		".jsx":   typeScriptProvider,
		".mjs":   typeScriptProvider,
		".cjs":   typeScriptProvider,
		".ts":    typeScriptProvider,
		".tsx":   typeScriptProvider,
		".mts":   typeScriptProvider,
		".cts":   typeScriptProvider,
		".swift": swiftProvider,
		".py":    pythonProvider,
		".rs":    rustProvider,
	})
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdowns := []struct {
			name string
			run  func(context.Context) error
		}{
			{"gopls", goplsManager.Shutdown},
			{"TypeScript LSP", typeScriptManager.Shutdown},
			{"SourceKit-LSP", swiftManager.Shutdown},
			{"Pyright language server", pythonManager.Shutdown},
			{"rust-analyzer", rustManager.Shutdown},
		}
		var wait sync.WaitGroup
		wait.Add(len(shutdowns))
		for _, shutdown := range shutdowns {
			shutdown := shutdown
			go func() {
				defer wait.Done()
				if err := shutdown.run(shutdownCtx); err != nil {
					logger.Warn("failed to shut down "+shutdown.name, "error", err)
				}
			}()
		}
		wait.Wait()
	}()
	retriever := retrieval.NewHybrid(storeRef, provider, cfg.EnableEmbeddings)
	retriever.SetLogger(logger)
	internalMutations := mutation.NewMemoryRegistry(mutation.RegistryConfig{})
	defer internalMutations.Close()
	backgroundIndexer := indexer.New(cfg.Workspace, cfg.MaxFileBytes, parserEngine, storeRef, retriever)
	backgroundIndexer.SetObservability(logger, metrics)
	backgroundIndexer.SetMutationRegistry(internalMutations)
	indexScheduler, err := scheduler.NewController(scheduler.Options{
		Mode: cfg.WatchMode, Debounce: cfg.WatchDebounce, MaxBatchDelay: cfg.WatchMaxBatchDelay,
		ReconcileInterval: cfg.WatchReconcileInterval, PollInterval: cfg.PollInterval,
		Executor: backgroundIndexer, CapabilitySink: registry, Logger: logger,
		WatcherFactory: func() (watcher.WorkspaceWatcher, error) {
			return watcher.New(cfg.Workspace)
		},
	})
	if err != nil {
		logger.Error("invalid scheduler", "error", err)
		return 1
	}
	explainer := service.NewExplainer(storeRef, workspace, provider)
	semanticCollector := semevidence.NewCollector(semanticProvider, 4*time.Second, 24)
	explainer.SetSemanticSources(semanticProvider, semanticCollector)
	codemaps := service.NewCodemapService(storeRef, retriever, provider)
	codemaps.SetSemanticSource(semanticCollector)
	deepWiki := service.NewDeepWikiService(storeRef, provider)
	deepWiki.SetSemanticSource(semanticCollector)
	saver := service.NewSavePreparer(workspace, storeRef, parserEngine, retriever, cfg.MaxFileBytes)
	committer := service.NewWorkspaceCommitCoordinator(saver, workspace, storeRef, journalDir, cfg.DatabasePath)
	committer.SetMutationRegistry(internalMutations)

	api := httpapi.New(workspace, storeRef, backgroundIndexer, retriever, explainer, codemaps, deepWiki, committer, provider, coordinator, registry, logger)
	api.SetSemanticProvider(semanticProvider)
	api.SetMetrics(metrics)
	api.SetScheduler(indexScheduler)
	api.SetMutationRegistry(internalMutations)
	httpServer := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	httpServer.RegisterOnShutdown(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := api.ShutdownJobs(shutdownCtx); err != nil {
			logger.Warn("failed to shut down jobs", "error", err)
		}
	})

	var providerProbe ai.CapabilityProbe
	if probe, ok := rawProvider.(ai.CapabilityProbe); ok {
		providerProbe = probe
	}

	logger.Info("CodeAtlas starting", "address", "http://"+cfg.ListenAddress, "workspace", cfg.Workspace, "provider", provider.Name())
	err = app.Run(rootContext, app.RuntimeDeps{
		Logger:           logger,
		Coordinator:      coordinator,
		Registry:         registry,
		Probes:           capabilities.LocalProbes(cfg.Workspace, filepath.Dir(cfg.DatabasePath), cfg.DatabasePath),
		ProviderProbe:    providerProbe,
		EnableEmbeddings: cfg.EnableEmbeddings,
		MigrateStore: func(ctx context.Context) error {
			store, report, err := storemigrate.OpenSQLiteForStartup(ctx, storemigrate.StartupOptions{
				WorkspaceRoot:     cfg.Workspace,
				DatabasePath:      cfg.DatabasePath,
				LegacyJSONPath:    legacyJSONPath,
				EmbeddingsEnabled: cfg.EnableEmbeddings,
				RecoverLegacyTransactions: func() error {
					return service.RecoverTransactionsForIndexesObserved(
						cfg.Workspace, journalDir, metrics.JournalRecovered, legacyJSONPath, cfg.DatabasePath,
					)
				},
			})
			if err != nil {
				return err
			}
			storeRef.Set(store)
			logger.Info("SQLite store ready", "mode", report.Plan.Mode, "migrated", report.Migrated, "database", cfg.DatabasePath)
			return nil
		},
		InitialIndex:        backgroundIndexer.Scan,
		RunIndexer:          indexScheduler.Run,
		ReconcileEmbeddings: reconcileEmbeddings(retriever, storeRef, registry, cfg),
		Server:              httpServer,
		Listen:              func() (net.Listener, error) { return net.Listen("tcp", cfg.ListenAddress) },
		Persist:             nil,
	})
	if err != nil {
		logger.Error("runtime stopped with an error", "error", err)
		return 1
	}
	return 0
}

// startGopls wires the already-existing semantic adapter into the runtime. The
// language server is an optional quality enhancement: failure is observable in
// /api/capabilities, but never prevents AST-only Hover from operating.
func startGopls(ctx context.Context, cfg config.Config, workspace *service.Workspace, registry *capabilities.Registry, logger *slog.Logger, hasGoFiles bool, scanErr error) (*gopls.Manager, semantic.SemanticProvider) {
	if scanErr != nil {
		logger.Warn("could not detect Go files for gopls", "error", scanErr)
	}
	manager := gopls.NewManager(gopls.Config{
		Enable: gopls.EnableMode(cfg.GoplsMode),
		Path:   cfg.GoplsPath,
	}, cfg.Workspace, nil)
	started := time.Now()
	startErr := manager.Start(ctx, hasGoFiles)
	result := goplsCapability(manager, time.Since(started))
	registry.UpdateCapability(result)
	if startErr != nil {
		logger.Warn("gopls unavailable; Hover will use AST evidence", "code", result.ErrorCode, "error", startErr)
	} else if result.State == capabilities.CapabilityAvailable {
		logger.Info("gopls ready", "version", result.Metadata["version"], "encoding", result.Metadata["positionEncoding"])
	}
	provider := gopls.NewProvider(manager, cfg.Workspace, func(query semantic.SemanticQuery, relativePath string) ([]byte, error) {
		if query.UsesOpenDocument() && relativePath == query.Path && query.Content != nil {
			return query.Content, nil
		}
		return workspace.Read(relativePath)
	})
	return manager, provider
}

func goplsCapability(manager *gopls.Manager, duration time.Duration) capabilities.Result {
	decision, reason := manager.Decision()
	result := capabilities.Result{
		ID: capabilities.CapabilityGopls, Requirement: capabilities.Optional,
		CheckedAt: time.Now().UTC(), Duration: duration,
	}
	switch decision {
	case gopls.DecisionEnabled:
		result.State = capabilities.CapabilityAvailable
		semanticCapabilities := manager.Capabilities()
		result.Metadata = map[string]string{
			"version": manager.Version(), "positionEncoding": manager.Encoding(),
			"languageFamily":     "go",
			"extensions":         ".go",
			"semanticTokensFull": strconv.FormatBool(semanticCapabilities.SemanticTokensFull),
			"documentSync":       semanticCapabilities.DocumentSyncKind,
		}
	case gopls.DecisionUnavailable:
		result.State = capabilities.CapabilityUnavailable
		result.ErrorCode = reason
		result.Message = "gopls unavailable; AST-only semantic coverage"
	default:
		result.State = capabilities.CapabilityDisabled
	}
	return result
}

func workspaceHasGoFiles(root string) (bool, error) {
	return workspaceHasSourceFiles(root, map[string]struct{}{".go": {}})
}

func workspaceHasJSTSFiles(root string) (bool, error) {
	return workspaceHasSourceFiles(root, map[string]struct{}{
		".js": {}, ".jsx": {}, ".mjs": {}, ".cjs": {},
		".ts": {}, ".tsx": {}, ".mts": {}, ".cts": {},
	})
}

func workspaceHasSwiftFiles(root string) (bool, error) {
	return workspaceHasSourceFiles(root, map[string]struct{}{".swift": {}})
}

func workspaceHasPythonFiles(root string) (bool, error) {
	return workspaceHasSourceFiles(root, map[string]struct{}{".py": {}})
}

func workspaceHasRustFiles(root string) (bool, error) {
	return workspaceHasSourceFiles(root, map[string]struct{}{".rs": {}})
}

func workspaceHasSourceFiles(root string, extensions map[string]struct{}) (bool, error) {
	found, err := workspaceSourceExtensions(root)
	if err != nil {
		return false, err
	}
	for extension := range extensions {
		if hasSourceExtension(found, extension) {
			return true, nil
		}
	}
	return false, nil
}

func workspaceSourceExtensions(root string) (map[string]struct{}, error) {
	found := make(map[string]struct{})
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && current != root {
			switch entry.Name() {
			case ".git", ".codeatlas", "vendor", "node_modules", "dist", "build", "target", ".venv", "venv", ".tox", "__pycache__", ".mypy_cache", ".pytest_cache", ".ruff_cache":
				return fs.SkipDir
			}
		}
		if !entry.IsDir() {
			extension := strings.ToLower(filepath.Ext(entry.Name()))
			if extension != "" {
				found[extension] = struct{}{}
			}
		}
		return nil
	})
	return found, err
}

func hasSourceExtension(found map[string]struct{}, extension string) bool {
	_, ok := found[extension]
	return ok
}

func hasAnySourceExtension(found map[string]struct{}, extensions ...string) bool {
	for _, extension := range extensions {
		if hasSourceExtension(found, extension) {
			return true
		}
	}
	return false
}

// startTypeScriptLSP activates the pre-existing adapter only when policy and
// workspace content allow it. Failure is explicit in /api/capabilities while
// AST retrieval remains the degraded path.
func startTypeScriptLSP(ctx context.Context, cfg config.Config, workspace *service.Workspace, registry *capabilities.Registry, logger *slog.Logger, hasJSTSFiles bool, scanErr error) (*typescriptlsp.Manager, semantic.SemanticProvider) {
	if scanErr != nil {
		logger.Warn("could not detect JS/TS files for TypeScript LSP", "error", scanErr)
	}
	manager := typescriptlsp.NewManager(typescriptlsp.Config{
		Enable:  typescriptlsp.EnableMode(cfg.TypeScriptLSPMode),
		Path:    cfg.TypeScriptLSPPath,
		SDKPath: cfg.TypeScriptSDKPath,
	}, cfg.Workspace, nil)
	started := time.Now()
	startErr := manager.Start(ctx, hasJSTSFiles)
	result := typeScriptLSPCapability(manager, time.Since(started))
	registry.UpdateCapability(result)
	if startErr != nil {
		logger.Warn("TypeScript LSP unavailable; JS/TS will use AST evidence", "code", result.ErrorCode, "error", startErr)
	} else if result.State == capabilities.CapabilityAvailable {
		logger.Info("TypeScript LSP ready", "version", result.Metadata["version"], "encoding", result.Metadata["positionEncoding"])
	}
	provider := typescriptlsp.NewProvider(manager, cfg.Workspace, func(query semantic.SemanticQuery, relativePath string) ([]byte, error) {
		if query.UsesOpenDocument() && relativePath == query.Path && query.Content != nil {
			return query.Content, nil
		}
		return workspace.Read(relativePath)
	})
	return manager, provider
}

func typeScriptLSPCapability(manager *typescriptlsp.Manager, duration time.Duration) capabilities.Result {
	decision, reason := manager.Decision()
	result := capabilities.Result{
		ID: capabilities.CapabilityTypeScriptLSP, Requirement: capabilities.Optional,
		CheckedAt: time.Now().UTC(), Duration: duration,
	}
	switch decision {
	case typescriptlsp.DecisionEnabled:
		result.State = capabilities.CapabilityAvailable
		semanticCapabilities := manager.Capabilities()
		result.Metadata = map[string]string{
			"version": manager.Version(), "positionEncoding": manager.Encoding(),
			"languageFamily":     "javascript-typescript",
			"extensions":         ".js,.mjs,.cjs,.jsx,.ts,.mts,.cts,.tsx",
			"semanticTokensFull": strconv.FormatBool(semanticCapabilities.SemanticTokensFull),
			"documentSync":       semanticCapabilities.DocumentSyncKind,
		}
	case typescriptlsp.DecisionUnavailable:
		result.State = capabilities.CapabilityUnavailable
		result.ErrorCode = reason
		result.Message = "TypeScript LSP unavailable; AST-only semantic coverage"
	default:
		result.State = capabilities.CapabilityDisabled
	}
	return result
}

func startSwiftLSP(ctx context.Context, cfg config.Config, workspace *service.Workspace, registry *capabilities.Registry, logger *slog.Logger, hasSwiftFiles bool, scanErr error) (*swiftlsp.Manager, semantic.SemanticProvider) {
	if scanErr != nil {
		logger.Warn("could not detect Swift files for SourceKit-LSP", "error", scanErr)
	}
	manager := swiftlsp.NewManager(swiftlsp.Config{
		Enable: swiftlsp.EnableMode(cfg.SwiftLSPMode), Path: cfg.SwiftLSPPath,
	}, cfg.Workspace, nil)
	started := time.Now()
	startErr := manager.Start(ctx, hasSwiftFiles)
	result := swiftLSPCapability(manager, time.Since(started))
	registry.UpdateCapability(result)
	if startErr != nil {
		logger.Warn("SourceKit-LSP unavailable; Swift will use AST evidence", "code", result.ErrorCode, "error", startErr)
	} else if result.State == capabilities.CapabilityAvailable {
		logger.Info("SourceKit-LSP ready", "version", result.Metadata["version"], "encoding", result.Metadata["positionEncoding"])
	}
	provider := swiftlsp.NewProvider(manager, cfg.Workspace, func(query semantic.SemanticQuery, relativePath string) ([]byte, error) {
		if query.UsesOpenDocument() && relativePath == query.Path && query.Content != nil {
			return query.Content, nil
		}
		return workspace.Read(relativePath)
	})
	return manager, provider
}

func swiftLSPCapability(manager *swiftlsp.Manager, duration time.Duration) capabilities.Result {
	decision, reason := manager.Decision()
	result := capabilities.Result{ID: capabilities.CapabilitySourceKitLSP, Requirement: capabilities.Optional, CheckedAt: time.Now().UTC(), Duration: duration}
	switch decision {
	case swiftlsp.DecisionEnabled:
		result.State = capabilities.CapabilityAvailable
		semanticCapabilities := manager.Capabilities()
		result.Metadata = map[string]string{
			"version": manager.Version(), "positionEncoding": manager.Encoding(),
			"languageFamily": "swift", "extensions": ".swift",
			"semanticTokensFull": strconv.FormatBool(semanticCapabilities.SemanticTokensFull),
			"documentSync":       semanticCapabilities.DocumentSyncKind,
		}
	case swiftlsp.DecisionUnavailable:
		result.State = capabilities.CapabilityUnavailable
		result.ErrorCode = reason
		result.Message = "SourceKit-LSP unavailable; AST-only semantic coverage"
	default:
		result.State = capabilities.CapabilityDisabled
	}
	return result
}

func startPythonLSP(ctx context.Context, cfg config.Config, workspace *service.Workspace, registry *capabilities.Registry, logger *slog.Logger, hasPythonFiles bool, scanErr error) (*pythonlsp.Manager, semantic.SemanticProvider) {
	if scanErr != nil {
		logger.Warn("could not detect Python files for Pyright", "error", scanErr)
	}
	manager := pythonlsp.NewManager(pythonlsp.Config{
		Enable: pythonlsp.EnableMode(cfg.PythonLSPMode), Path: cfg.PythonLSPPath,
	}, cfg.Workspace, nil)
	started := time.Now()
	startErr := manager.Start(ctx, hasPythonFiles)
	result := pythonLSPCapability(manager, time.Since(started))
	registry.UpdateCapability(result)
	if startErr != nil {
		logger.Warn("Pyright unavailable; Python will use AST evidence", "code", result.ErrorCode, "error", startErr)
	} else if result.State == capabilities.CapabilityAvailable {
		logger.Info("Pyright ready", "version", result.Metadata["version"], "encoding", result.Metadata["positionEncoding"])
	}
	provider := pythonlsp.NewProvider(manager, cfg.Workspace, func(query semantic.SemanticQuery, relativePath string) ([]byte, error) {
		if query.UsesOpenDocument() && relativePath == query.Path && query.Content != nil {
			return query.Content, nil
		}
		return workspace.Read(relativePath)
	})
	return manager, provider
}

func pythonLSPCapability(manager *pythonlsp.Manager, duration time.Duration) capabilities.Result {
	decision, reason := manager.Decision()
	result := capabilities.Result{ID: capabilities.CapabilityPythonLSP, Requirement: capabilities.Optional, CheckedAt: time.Now().UTC(), Duration: duration}
	switch decision {
	case pythonlsp.DecisionEnabled:
		result.State = capabilities.CapabilityAvailable
		semanticCapabilities := manager.Capabilities()
		result.Metadata = map[string]string{
			"version": manager.Version(), "positionEncoding": manager.Encoding(),
			"languageFamily": "python", "extensions": ".py",
			"semanticTokensFull": strconv.FormatBool(semanticCapabilities.SemanticTokensFull),
			"documentSync":       semanticCapabilities.DocumentSyncKind,
		}
	case pythonlsp.DecisionUnavailable:
		result.State = capabilities.CapabilityUnavailable
		result.ErrorCode = reason
		result.Message = "Pyright unavailable; AST-only semantic coverage"
	default:
		result.State = capabilities.CapabilityDisabled
	}
	return result
}

func startRustLSP(ctx context.Context, cfg config.Config, workspace *service.Workspace, registry *capabilities.Registry, logger *slog.Logger, hasRustFiles bool, scanErr error) (*rustlsp.Manager, semantic.SemanticProvider) {
	if scanErr != nil {
		logger.Warn("could not detect Rust files for rust-analyzer", "error", scanErr)
	}
	manager := rustlsp.NewManager(rustlsp.Config{
		Enable: rustlsp.EnableMode(cfg.RustLSPMode), Path: cfg.RustLSPPath,
	}, cfg.Workspace, nil)
	started := time.Now()
	startErr := manager.Start(ctx, hasRustFiles)
	result := rustLSPCapability(manager, time.Since(started))
	registry.UpdateCapability(result)
	if startErr != nil {
		logger.Warn("rust-analyzer unavailable; Rust will use AST evidence", "code", result.ErrorCode, "error", startErr)
	} else if result.State == capabilities.CapabilityAvailable {
		logger.Info("rust-analyzer ready in safe standalone mode", "version", result.Metadata["version"], "encoding", result.Metadata["positionEncoding"])
	}
	provider := rustlsp.NewProvider(manager, cfg.Workspace, func(query semantic.SemanticQuery, relativePath string) ([]byte, error) {
		if query.UsesOpenDocument() && relativePath == query.Path && query.Content != nil {
			return query.Content, nil
		}
		return workspace.Read(relativePath)
	})
	return manager, provider
}

func rustLSPCapability(manager *rustlsp.Manager, duration time.Duration) capabilities.Result {
	decision, reason := manager.Decision()
	result := capabilities.Result{ID: capabilities.CapabilityRustAnalyzer, Requirement: capabilities.Optional, CheckedAt: time.Now().UTC(), Duration: duration}
	switch decision {
	case rustlsp.DecisionEnabled:
		result.State = capabilities.CapabilityAvailable
		semanticCapabilities := manager.Capabilities()
		result.Metadata = map[string]string{
			"version": manager.Version(), "positionEncoding": manager.Encoding(),
			"languageFamily": "rust", "extensions": ".rs",
			"semanticTokensFull": strconv.FormatBool(semanticCapabilities.SemanticTokensFull),
			"documentSync":       semanticCapabilities.DocumentSyncKind,
			"projectMode":        "standalone-safe",
		}
	case rustlsp.DecisionUnavailable:
		result.State = capabilities.CapabilityUnavailable
		result.ErrorCode = reason
		result.Message = "rust-analyzer unavailable; AST-only semantic coverage"
	default:
		result.State = capabilities.CapabilityDisabled
	}
	return result
}

// reconcileEmbeddings returns the startup dense-index reconciliation step. It is
// a no-op when embeddings are disabled; otherwise it rebuilds incompatible/legacy
// vectors and publishes the resulting metadata to the capability registry.
func reconcileEmbeddings(retriever *retrieval.Hybrid, repository repository.Store, registry *capabilities.Registry, cfg config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if !cfg.EnableEmbeddings {
			return nil
		}
		if err := retriever.Reconcile(ctx, embeddingProviderID(cfg), cfg.EmbeddingModel); err != nil {
			return err
		}
		metadata := repository.EmbeddingMetadata()
		registry.UpdateCapability(capabilities.Result{
			ID:          "llm-embeddings",
			Requirement: capabilities.Required,
			State:       capabilities.CapabilityAvailable,
			CheckedAt:   time.Now().UTC(),
			Metadata: map[string]string{
				"dimension":       strconv.Itoa(metadata.Dimension),
				"model":           metadata.Model,
				"provider":        metadata.Provider,
				"templateVersion": metadata.TemplateVersion,
				"distance":        metadata.Distance,
				"vectorCount":     strconv.Itoa(repository.EmbeddingCount()),
				"state":           "valid",
			},
		})
		return nil
	}
}

func embeddingProviderID(cfg config.Config) string {
	endpoint := strings.TrimSpace(cfg.EmbeddingBaseURL)
	if endpoint == "" {
		endpoint = cfg.LLMBaseURL
	}
	normalized := normalizeEndpointForFingerprint(endpoint)
	sum := sha256.Sum256([]byte(normalized))
	return "openai-compatible:embeddings:" + hex.EncodeToString(sum[:8])
}

func normalizeEndpointForFingerprint(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return endpoint
	}
	parsed.User = nil
	parsed.Fragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String()
}
