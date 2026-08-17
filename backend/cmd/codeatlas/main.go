package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
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
	"github.com/ThalesMMS/CodeAtlas/internal/lspruntime"
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
	"github.com/ThalesMMS/CodeAtlas/internal/settings"
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
	settingsPath, err := settings.DefaultPath()
	if err != nil {
		logger.Error("could not resolve per-user settings path", "error", err)
		return 1
	}
	settingsStore := settings.NewFileStore(settingsPath)
	credentialStore := settings.NewKeyringCredentialStore()
	settingsEnvironment := settings.EnvironmentFromLookup(os.LookupEnv)
	_, startupResolved, err := settings.LoadStartup(context.Background(), settingsEnvironment, settingsStore, credentialStore)
	if err != nil {
		logger.Error("could not load per-user settings", "error", err)
		return 1
	}
	cfg, err := config.LoadWithSettings(startupResolved.Values)
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
	providerRuntime := ai.NewRuntime()
	providerRuntime.Swap(observability.ObserveRuntimeCandidate(ai.RuntimeCandidate{Provider: rawProvider}, logger, metrics))
	var provider ai.Provider = providerRuntime
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
	semanticRouter := semantic.NewPathRouter(map[string]semantic.SemanticProvider{
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
	semanticProvider := semantic.NewRuntime(semanticRouter)
	initialLSPSlots := map[lspruntime.Family]lspruntime.Slot{
		lspruntime.FamilyGo: {
			Provider: goplsProvider, Capability: goplsCapability(goplsManager, 0), Shutdown: goplsManager.Shutdown,
		},
		lspruntime.FamilyTypeScript: {
			Provider: typeScriptProvider, Capability: typeScriptLSPCapability(typeScriptManager, 0), Shutdown: typeScriptManager.Shutdown,
		},
		lspruntime.FamilySwift: {
			Provider: swiftProvider, Capability: swiftLSPCapability(swiftManager, 0), Shutdown: swiftManager.Shutdown,
		},
		lspruntime.FamilyPython: {
			Provider: pythonProvider, Capability: pythonLSPCapability(pythonManager, 0), Shutdown: pythonManager.Shutdown,
		},
		lspruntime.FamilyRust: {
			Provider: rustProvider, Capability: rustLSPCapability(rustManager, 0), Shutdown: rustManager.Shutdown,
		},
	}
	lspCoordinator := lspruntime.NewCoordinator(
		semanticProvider,
		newLSPRuntimeFactories(cfg.Workspace, workspace, sourceExtensions),
		registry,
		initialLSPSlots,
		10*time.Second,
	)
	defer lspCoordinator.Shutdown(context.Background())
	embeddingRuntime := retrieval.NewEmbeddingRuntime(providerRuntime, cfg.EnableEmbeddings)
	retriever := retrieval.NewHybridWithRuntime(storeRef, embeddingRuntime)
	retriever.SetLogger(logger)
	settingsRuntime := app.NewSettingsRuntime(app.SettingsRuntimeOptions{
		AIRuntime: providerRuntime, EmbeddingRuntime: embeddingRuntime, EmbeddingStore: storeRef,
		LSPCoordinator: lspCoordinator, Logger: logger, Metrics: metrics, ProbeTimeout: cfg.ProbeTimeout,
		StructuredProbeSchema: aiout.ExplanationSchema(),
	})
	settingsManager, err := settings.NewManagerWithRunningValues(rootContext, settingsEnvironment, settingsStore, credentialStore, settingsRuntime, settings.Values{
		Workspace: cfg.Workspace, ListenAddress: cfg.ListenAddress, MaxFileBytes: cfg.MaxFileBytes,
	})
	if err != nil {
		logger.Error("could not initialize runtime settings", "error", err)
		return 1
	}
	_ = settingsManager
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
	settingsRuntime.SetEmbeddingScheduler(api.ScheduleEmbeddingRebuild)
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

	var providerProbe ai.CapabilityProbe = providerRuntime

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

type startupSettings struct {
	Config   config.Config
	Document settings.Document
	Resolved settings.Resolved
}

func loadStartupSettings(ctx context.Context, cfg config.Config, environment settings.Environment, store settings.DocumentStore, credentials settings.CredentialStore, operatorOverrides map[string]bool) (startupSettings, error) {
	document, resolved, err := settings.LoadStartup(ctx, environment, store, credentials)
	if err != nil {
		return startupSettings{}, err
	}
	values := resolved.Values
	if !operatorOverrides["workspace"] {
		workspace, err := filepath.Abs(values.Workspace)
		if err != nil {
			return startupSettings{}, err
		}
		info, err := os.Stat(workspace)
		if err != nil {
			return startupSettings{}, err
		}
		if !info.IsDir() {
			return startupSettings{}, fmt.Errorf("workspace is not a directory: %s", workspace)
		}
		cfg.Workspace = workspace
	}
	if !operatorOverrides["listen"] {
		cfg.ListenAddress = values.ListenAddress
	}
	if !operatorOverrides["db"] {
		cfg.DatabasePath = filepath.Join(cfg.Workspace, ".codeatlas", "codeatlas.db")
	}
	cfg.MaxFileBytes = values.MaxFileBytes
	cfg.LLMBaseURL = values.LLMBaseURL
	cfg.LLMAPIKey = values.LLMAPIKey
	cfg.LLMModel = values.LLMModel
	cfg.LLMReasoningEffort = values.LLMReasoningEffort
	cfg.LLMTimeout = values.LLMTimeout
	cfg.GoplsMode = values.GoplsMode
	cfg.GoplsPath = values.GoplsPath
	cfg.TypeScriptLSPMode = values.TypeScriptLSPMode
	cfg.TypeScriptLSPPath = values.TypeScriptLSPPath
	cfg.TypeScriptSDKPath = values.TypeScriptSDKPath
	cfg.SwiftLSPMode = values.SwiftLSPMode
	cfg.SwiftLSPPath = values.SwiftLSPPath
	cfg.PythonLSPMode = values.PythonLSPMode
	cfg.PythonLSPPath = values.PythonLSPPath
	cfg.RustLSPMode = values.RustLSPMode
	cfg.RustLSPPath = values.RustLSPPath
	cfg.EnableEmbeddings = values.EnableEmbeddings
	cfg.EmbeddingModel = values.EmbeddingModel
	cfg.EmbeddingBaseURL = values.EmbeddingBaseURL
	cfg.EmbeddingsAPIKey = values.EmbeddingsAPIKey
	return startupSettings{Config: cfg, Document: document, Resolved: resolved}, nil
}

func explicitlySetFlags() map[string]bool {
	result := make(map[string]bool)
	flag.CommandLine.Visit(func(current *flag.Flag) { result[current.Name] = true })
	return result
}

func newLSPRuntimeFactories(root string, workspace *service.Workspace, sourceExtensions map[string]struct{}) lspruntime.Factories {
	readContent := func(query semantic.SemanticQuery, relativePath string) ([]byte, error) {
		if query.UsesOpenDocument() && relativePath == query.Path && query.Content != nil {
			return query.Content, nil
		}
		return workspace.Read(relativePath)
	}
	cleanup := func(shutdown func(context.Context) error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	}
	return lspruntime.Factories{
		Go: func(ctx context.Context, values settings.Values) (lspruntime.Slot, error) {
			manager := gopls.NewManager(gopls.Config{Enable: gopls.EnableMode(values.GoplsMode), Path: values.GoplsPath}, root, nil)
			started := time.Now()
			if err := manager.Start(ctx, hasSourceExtension(sourceExtensions, ".go")); err != nil {
				cleanup(manager.Shutdown)
				return lspruntime.Slot{}, err
			}
			return lspruntime.Slot{
				Provider:   gopls.NewProvider(manager, root, readContent),
				Capability: goplsCapability(manager, time.Since(started)),
				Shutdown:   manager.Shutdown,
			}, nil
		},
		TypeScript: func(ctx context.Context, values settings.Values) (lspruntime.Slot, error) {
			manager := typescriptlsp.NewManager(typescriptlsp.Config{
				Enable: typescriptlsp.EnableMode(values.TypeScriptLSPMode), Path: values.TypeScriptLSPPath, SDKPath: values.TypeScriptSDKPath,
			}, root, nil)
			started := time.Now()
			if err := manager.Start(ctx, hasAnySourceExtension(sourceExtensions, ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts")); err != nil {
				cleanup(manager.Shutdown)
				return lspruntime.Slot{}, err
			}
			return lspruntime.Slot{
				Provider:   typescriptlsp.NewProvider(manager, root, readContent),
				Capability: typeScriptLSPCapability(manager, time.Since(started)),
				Shutdown:   manager.Shutdown,
			}, nil
		},
		Swift: func(ctx context.Context, values settings.Values) (lspruntime.Slot, error) {
			manager := swiftlsp.NewManager(swiftlsp.Config{Enable: swiftlsp.EnableMode(values.SwiftLSPMode), Path: values.SwiftLSPPath}, root, nil)
			started := time.Now()
			if err := manager.Start(ctx, hasSourceExtension(sourceExtensions, ".swift")); err != nil {
				cleanup(manager.Shutdown)
				return lspruntime.Slot{}, err
			}
			return lspruntime.Slot{
				Provider:   swiftlsp.NewProvider(manager, root, readContent),
				Capability: swiftLSPCapability(manager, time.Since(started)),
				Shutdown:   manager.Shutdown,
			}, nil
		},
		Python: func(ctx context.Context, values settings.Values) (lspruntime.Slot, error) {
			manager := pythonlsp.NewManager(pythonlsp.Config{Enable: pythonlsp.EnableMode(values.PythonLSPMode), Path: values.PythonLSPPath}, root, nil)
			started := time.Now()
			if err := manager.Start(ctx, hasSourceExtension(sourceExtensions, ".py")); err != nil {
				cleanup(manager.Shutdown)
				return lspruntime.Slot{}, err
			}
			return lspruntime.Slot{
				Provider:   pythonlsp.NewProvider(manager, root, readContent),
				Capability: pythonLSPCapability(manager, time.Since(started)),
				Shutdown:   manager.Shutdown,
			}, nil
		},
		Rust: func(ctx context.Context, values settings.Values) (lspruntime.Slot, error) {
			manager := rustlsp.NewManager(rustlsp.Config{Enable: rustlsp.EnableMode(values.RustLSPMode), Path: values.RustLSPPath}, root, nil)
			started := time.Now()
			if err := manager.Start(ctx, hasSourceExtension(sourceExtensions, ".rs")); err != nil {
				cleanup(manager.Shutdown)
				return lspruntime.Slot{}, err
			}
			return lspruntime.Slot{
				Provider:   rustlsp.NewProvider(manager, root, readContent),
				Capability: rustLSPCapability(manager, time.Since(started)),
				Shutdown:   manager.Shutdown,
			}, nil
		},
	}
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
