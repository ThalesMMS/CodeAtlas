package config

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/settings"
)

type Config struct {
	Workspace              string
	DatabasePath           string
	ListenAddress          string
	FrontendDevURL         string
	MaxFileBytes           int64
	LLMBaseURL             string
	LLMAPIKey              string
	LLMModel               string
	LLMReasoningEffort     string
	LLMTimeout             time.Duration
	EmbeddingBaseURL       string
	EmbeddingModel         string
	EmbeddingsAPIKey       string
	EnableEmbeddings       bool
	ProbeTimeout           time.Duration
	WatchMode              string
	WatchDebounce          time.Duration
	WatchMaxBatchDelay     time.Duration
	WatchReconcileInterval time.Duration
	PollInterval           time.Duration
	GoplsMode              string
	GoplsPath              string
	TypeScriptLSPMode      string
	TypeScriptLSPPath      string
	TypeScriptSDKPath      string
	SwiftLSPMode           string
	SwiftLSPPath           string
	PythonLSPMode          string
	PythonLSPPath          string
	RustLSPMode            string
	RustLSPPath            string
}

type ValidationIssue struct {
	EnvironmentKey string
	Message        string
}

func Load() (Config, error) {
	return load(nil)
}

// LoadWithSettings applies already-resolved per-user values before parsing the
// remaining environment-only configuration. Explicit CLI flags still override
// workspace/listen values because they are parsed after these defaults.
func LoadWithSettings(values settings.Values) (Config, error) {
	return load(&values)
}

func load(saved *settings.Values) (Config, error) {
	workspaceDefault := envOr("CODEATLAS_WORKSPACE", ".")
	listenDefault := envOr("CODEATLAS_LISTEN", "127.0.0.1:8080")
	if saved != nil {
		workspaceDefault = saved.Workspace
		listenDefault = saved.ListenAddress
	}
	workspaceFlag := flag.String("workspace", workspaceDefault, "workspace directory to index")
	listenFlag := flag.String("listen", listenDefault, "HTTP listen address")
	dbFlag := flag.String("db", envOr("CODEATLAS_DB", ""), "SQLite database path")
	flag.Parse()

	workspace, err := filepath.Abs(*workspaceFlag)
	if err != nil {
		return Config{}, fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return Config{}, fmt.Errorf("workspace: %w", err)
	}
	if !info.IsDir() {
		return Config{}, fmt.Errorf("workspace is not a directory: %s", workspace)
	}

	databasePath := *dbFlag
	if databasePath == "" {
		databasePath = filepath.Join(workspace, ".codeatlas", "codeatlas.db")
	}
	databasePath, err = filepath.Abs(databasePath)
	if err != nil {
		return Config{}, fmt.Errorf("resolve database path: %w", err)
	}

	maxFileBytes := int64(1_500_000)
	enableEmbeddings := false
	llmTimeout := 10 * time.Minute
	if saved == nil {
		maxFileBytesValue, err := envInt("CODEATLAS_MAX_FILE_BYTES", 1_500_000)
		if err != nil {
			return Config{}, err
		}
		maxFileBytes = int64(maxFileBytesValue)
		enableEmbeddings, err = envBool("CODEATLAS_ENABLE_EMBEDDINGS", false)
		if err != nil {
			return Config{}, err
		}
		llmTimeout, err = envDuration("CODEATLAS_LLM_TIMEOUT", 10*time.Minute)
		if err != nil {
			return Config{}, err
		}
	} else {
		maxFileBytes = saved.MaxFileBytes
		enableEmbeddings = saved.EnableEmbeddings
		llmTimeout = saved.LLMTimeout
	}
	probeTimeout, err := envDuration("CODEATLAS_PROBE_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	watchDebounce, err := envDuration("CODEATLAS_WATCH_DEBOUNCE", 250*time.Millisecond)
	if err != nil {
		return Config{}, err
	}
	watchMaxBatchDelay, err := envDuration("CODEATLAS_WATCH_MAX_BATCH_DELAY", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	watchReconcileInterval, err := envDuration("CODEATLAS_WATCH_RECONCILE_INTERVAL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	pollInterval, err := envDuration("CODEATLAS_POLL_INTERVAL", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Workspace:              workspace,
		DatabasePath:           databasePath,
		ListenAddress:          *listenFlag,
		FrontendDevURL:         strings.TrimRight(os.Getenv("CODEATLAS_FRONTEND_DEV_URL"), "/"),
		MaxFileBytes:           maxFileBytes,
		LLMBaseURL:             strings.TrimRight(os.Getenv("CODEATLAS_LLM_BASE_URL"), "/"),
		LLMAPIKey:              os.Getenv("CODEATLAS_LLM_API_KEY"),
		LLMModel:               envOr("CODEATLAS_LLM_MODEL", ""),
		LLMReasoningEffort:     strings.ToLower(strings.TrimSpace(os.Getenv("CODEATLAS_LLM_REASONING_EFFORT"))),
		LLMTimeout:             llmTimeout,
		EmbeddingBaseURL:       strings.TrimRight(os.Getenv("CODEATLAS_EMBEDDING_BASE_URL"), "/"),
		EmbeddingModel:         envOr("CODEATLAS_EMBEDDING_MODEL", ""),
		EmbeddingsAPIKey:       os.Getenv("CODEATLAS_EMBEDDINGS_API_KEY"),
		EnableEmbeddings:       enableEmbeddings,
		ProbeTimeout:           probeTimeout,
		WatchMode:              envOr("CODEATLAS_WATCH_MODE", "auto"),
		WatchDebounce:          watchDebounce,
		WatchMaxBatchDelay:     watchMaxBatchDelay,
		WatchReconcileInterval: watchReconcileInterval,
		PollInterval:           pollInterval,
		GoplsMode:              strings.ToLower(envOr("CODEATLAS_GOPLS", "auto")),
		GoplsPath:              envOr("CODEATLAS_GOPLS_PATH", "gopls"),
		TypeScriptLSPMode:      strings.ToLower(envOr("CODEATLAS_TYPESCRIPT_LSP", "auto")),
		TypeScriptLSPPath:      envOr("CODEATLAS_TYPESCRIPT_LSP_PATH", "typescript-language-server"),
		TypeScriptSDKPath:      strings.TrimSpace(os.Getenv("CODEATLAS_TYPESCRIPT_SDK_PATH")),
		SwiftLSPMode:           strings.ToLower(envOr("CODEATLAS_SWIFT_LSP", "auto")),
		SwiftLSPPath:           envOr("CODEATLAS_SWIFT_LSP_PATH", "sourcekit-lsp"),
		PythonLSPMode:          strings.ToLower(envOr("CODEATLAS_PYTHON_LSP", "auto")),
		PythonLSPPath:          envOr("CODEATLAS_PYTHON_LSP_PATH", "pyright-langserver"),
		RustLSPMode:            strings.ToLower(envOr("CODEATLAS_RUST_LSP", "auto")),
		RustLSPPath:            envOr("CODEATLAS_RUST_LSP_PATH", "rust-analyzer"),
	}
	if saved != nil {
		cfg.LLMBaseURL = saved.LLMBaseURL
		cfg.LLMAPIKey = saved.LLMAPIKey
		cfg.LLMModel = saved.LLMModel
		cfg.LLMReasoningEffort = saved.LLMReasoningEffort
		cfg.EmbeddingBaseURL = saved.EmbeddingBaseURL
		cfg.EmbeddingModel = saved.EmbeddingModel
		cfg.EmbeddingsAPIKey = saved.EmbeddingsAPIKey
		cfg.GoplsMode = saved.GoplsMode
		cfg.GoplsPath = saved.GoplsPath
		cfg.TypeScriptLSPMode = saved.TypeScriptLSPMode
		cfg.TypeScriptLSPPath = saved.TypeScriptLSPPath
		cfg.TypeScriptSDKPath = saved.TypeScriptSDKPath
		cfg.SwiftLSPMode = saved.SwiftLSPMode
		cfg.SwiftLSPPath = saved.SwiftLSPPath
		cfg.PythonLSPMode = saved.PythonLSPMode
		cfg.PythonLSPPath = saved.PythonLSPPath
		cfg.RustLSPMode = saved.RustLSPMode
		cfg.RustLSPPath = saved.RustLSPPath
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.MaxFileBytes <= 0 {
		return fmt.Errorf("CODEATLAS_MAX_FILE_BYTES must be positive")
	}
	if c.ProbeTimeout <= 0 {
		return fmt.Errorf("CODEATLAS_PROBE_TIMEOUT must be positive")
	}
	if c.LLMTimeout <= 0 {
		return fmt.Errorf("CODEATLAS_LLM_TIMEOUT must be positive")
	}
	switch c.WatchMode {
	case "auto", "native", "polling":
	default:
		return fmt.Errorf("CODEATLAS_WATCH_MODE must be auto, native, or polling")
	}
	switch c.GoplsMode {
	case "auto", "true", "false":
	default:
		return fmt.Errorf("CODEATLAS_GOPLS must be auto, true, or false")
	}
	if strings.TrimSpace(c.GoplsPath) == "" {
		return fmt.Errorf("CODEATLAS_GOPLS_PATH cannot be empty")
	}
	switch c.TypeScriptLSPMode {
	case "auto", "true", "false":
	default:
		return fmt.Errorf("CODEATLAS_TYPESCRIPT_LSP must be auto, true, or false")
	}
	if strings.TrimSpace(c.TypeScriptLSPPath) == "" {
		return fmt.Errorf("CODEATLAS_TYPESCRIPT_LSP_PATH cannot be empty")
	}
	switch c.SwiftLSPMode {
	case "auto", "true", "false":
	default:
		return fmt.Errorf("CODEATLAS_SWIFT_LSP must be auto, true, or false")
	}
	if strings.TrimSpace(c.SwiftLSPPath) == "" {
		return fmt.Errorf("CODEATLAS_SWIFT_LSP_PATH cannot be empty")
	}
	switch c.PythonLSPMode {
	case "auto", "true", "false":
	default:
		return fmt.Errorf("CODEATLAS_PYTHON_LSP must be auto, true, or false")
	}
	if strings.TrimSpace(c.PythonLSPPath) == "" {
		return fmt.Errorf("CODEATLAS_PYTHON_LSP_PATH cannot be empty")
	}
	switch c.RustLSPMode {
	case "auto", "true", "false":
	default:
		return fmt.Errorf("CODEATLAS_RUST_LSP must be auto, true, or false")
	}
	if strings.TrimSpace(c.RustLSPPath) == "" {
		return fmt.Errorf("CODEATLAS_RUST_LSP_PATH cannot be empty")
	}
	if c.WatchDebounce <= 0 {
		return fmt.Errorf("CODEATLAS_WATCH_DEBOUNCE must be positive")
	}
	if c.WatchMaxBatchDelay <= 0 {
		return fmt.Errorf("CODEATLAS_WATCH_MAX_BATCH_DELAY must be positive")
	}
	if c.WatchReconcileInterval <= 0 {
		return fmt.Errorf("CODEATLAS_WATCH_RECONCILE_INTERVAL must be positive")
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("CODEATLAS_POLL_INTERVAL must be positive")
	}
	return nil
}

// ValidateProvider reports recoverable provider configuration problems without
// making workspace/listener startup fail. Runtime settings can repair these
// fields after the local HTTP UI has bound successfully.
func ValidateProvider(c Config) []ValidationIssue {
	issues := make([]ValidationIssue, 0, 6)
	if strings.TrimSpace(c.LLMBaseURL) == "" {
		issues = append(issues, ValidationIssue{EnvironmentKey: "CODEATLAS_LLM_BASE_URL", Message: "LLM base URL is required"})
	} else if parsed, err := url.Parse(c.LLMBaseURL); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		issues = append(issues, ValidationIssue{EnvironmentKey: "CODEATLAS_LLM_BASE_URL", Message: "LLM base URL must be an http(s) URL with a valid host"})
	}
	if strings.TrimSpace(c.LLMModel) == "" {
		issues = append(issues, ValidationIssue{EnvironmentKey: "CODEATLAS_LLM_MODEL", Message: "LLM model is required"})
	}
	switch c.LLMReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		issues = append(issues, ValidationIssue{EnvironmentKey: "CODEATLAS_LLM_REASONING_EFFORT", Message: "unsupported reasoning effort"})
	}
	if c.LLMTimeout <= 0 {
		issues = append(issues, ValidationIssue{EnvironmentKey: "CODEATLAS_LLM_TIMEOUT", Message: "LLM timeout must be positive"})
	}
	if c.EnableEmbeddings {
		if strings.TrimSpace(c.EmbeddingModel) == "" {
			issues = append(issues, ValidationIssue{EnvironmentKey: "CODEATLAS_EMBEDDING_MODEL", Message: "embedding model is required"})
		}
		if strings.TrimSpace(c.EmbeddingBaseURL) != "" {
			parsed, err := url.Parse(c.EmbeddingBaseURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				issues = append(issues, ValidationIssue{EnvironmentKey: "CODEATLAS_EMBEDDING_BASE_URL", Message: "embedding base URL must be an http(s) URL with a valid host"})
			}
		}
	}
	return issues
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return parsed, nil
}

func envBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid %s: %w", key, err)
	}
	return parsed, nil
}

// envDuration parses a Go duration (e.g. "10s") from the environment. An unset
// value yields the fallback; an invalid or non-positive value is a hard error so
// misconfiguration is caught at startup rather than silently ignored.
func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return parsed, nil
}
