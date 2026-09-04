package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/settings"
)

// llmEnv sets the minimal valid LLM configuration plus a workspace so individual
// tests only need to override what they exercise.
func llmEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CODEATLAS_WORKSPACE", t.TempDir())
	t.Setenv("CODEATLAS_LLM_BASE_URL", "http://127.0.0.1:8000/v1")
	t.Setenv("CODEATLAS_LLM_API_KEY", "apikey")
	t.Setenv("CODEATLAS_LLM_MODEL", "default")
	t.Setenv("CODEATLAS_LLM_REASONING_EFFORT", "")
	t.Setenv("CODEATLAS_LLM_TIMEOUT", "")
	t.Setenv("CODEATLAS_PROBE_TIMEOUT", "")
	t.Setenv("CODEATLAS_GOPLS", "")
	t.Setenv("CODEATLAS_GOPLS_PATH", "")
	t.Setenv("CODEATLAS_TYPESCRIPT_LSP", "")
	t.Setenv("CODEATLAS_TYPESCRIPT_LSP_PATH", "")
	t.Setenv("CODEATLAS_TYPESCRIPT_SDK_PATH", "")
	t.Setenv("CODEATLAS_SWIFT_LSP", "")
	t.Setenv("CODEATLAS_SWIFT_LSP_PATH", "")
	t.Setenv("CODEATLAS_PYTHON_LSP", "")
	t.Setenv("CODEATLAS_PYTHON_LSP_PATH", "")
	t.Setenv("CODEATLAS_RUST_LSP", "")
	t.Setenv("CODEATLAS_RUST_LSP_PATH", "")
}

func TestLoadLLMReasoningEffort(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "unset", value: "", want: ""},
		{name: "normalizes", value: " Medium ", want: "medium"},
		{name: "none", value: "none", want: "none"},
		{name: "minimal", value: "minimal", want: "minimal"},
		{name: "low", value: "low", want: "low"},
		{name: "high", value: "high", want: "high"},
		{name: "xhigh", value: "xhigh", want: "xhigh"},
		{name: "max", value: "max", want: "max"},
		{name: "invalid", value: "extreme", want: "extreme"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetFlags(t)
			llmEnv(t)
			t.Setenv("CODEATLAS_LLM_REASONING_EFFORT", test.value)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.LLMReasoningEffort != test.want {
				t.Fatalf("LLMReasoningEffort = %q, want %q", cfg.LLMReasoningEffort, test.want)
			}
			if test.value == "extreme" && len(ValidateProvider(cfg)) == 0 {
				t.Fatal("invalid reasoning effort was not reported as recoverable")
			}
		})
	}
}

func TestLoadAllowsMissingLLMEndpointAndModelForSettingsBootstrap(t *testing.T) {
	resetFlags(t)
	t.Setenv("CODEATLAS_WORKSPACE", t.TempDir())
	t.Setenv("CODEATLAS_LLM_BASE_URL", "")
	t.Setenv("CODEATLAS_LLM_MODEL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want recoverable provider configuration", err)
	}
	issues := ValidateProvider(cfg)
	if len(issues) != 2 || issues[0].EnvironmentKey != "CODEATLAS_LLM_BASE_URL" || issues[1].EnvironmentKey != "CODEATLAS_LLM_MODEL" {
		t.Fatalf("ValidateProvider() = %#v, want missing endpoint/model", issues)
	}
}

func TestLoadArgsDoesNotReadProcessArguments(t *testing.T) {
	resetFlags(t)
	llmEnv(t)
	workspace := t.TempDir()
	os.Args = []string{"codeatlas", "-workspace", filepath.Join(t.TempDir(), "missing")}

	cfg, err := LoadArgs([]string{"-workspace", workspace, "-listen", "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace != filepath.Clean(workspace) || cfg.ListenAddress != "127.0.0.1:0" {
		t.Fatalf("LoadArgs() = workspace %q listen %q", cfg.Workspace, cfg.ListenAddress)
	}
}

func TestLoadWithSettingsAppliesTypedOverridesBeforeMalformedEnvironment(t *testing.T) {
	resetFlags(t)
	workspace := t.TempDir()
	t.Setenv("CODEATLAS_WORKSPACE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("CODEATLAS_MAX_FILE_BYTES", "many")
	t.Setenv("CODEATLAS_LLM_TIMEOUT", "eventually")

	cfg, err := LoadWithSettings(settings.Values{
		Workspace: workspace, ListenAddress: "127.0.0.1:9000", MaxFileBytes: 2048,
		LLMBaseURL: "https://saved.test/v1", LLMModel: "saved-model", LLMTimeout: time.Minute,
		GoplsMode: "auto", GoplsPath: "gopls",
		TypeScriptLSPMode: "auto", TypeScriptLSPPath: "typescript-language-server",
		SwiftLSPMode: "auto", SwiftLSPPath: "sourcekit-lsp",
		PythonLSPMode: "auto", PythonLSPPath: "pyright-langserver",
		RustLSPMode: "auto", RustLSPPath: "rust-analyzer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace != workspace || cfg.MaxFileBytes != 2048 || cfg.LLMTimeout != time.Minute {
		t.Fatalf("LoadWithSettings() = %#v", cfg)
	}
}

func TestLoadArgsWithSettingsUsesExplicitArgumentsOverSavedValues(t *testing.T) {
	resetFlags(t)
	llmEnv(t)
	savedWorkspace := t.TempDir()
	operatorWorkspace := t.TempDir()
	os.Args = []string{"codeatlas", "-workspace", filepath.Join(t.TempDir(), "missing")}

	cfg, err := LoadArgsWithSettings([]string{"-workspace", operatorWorkspace, "-listen", "127.0.0.1:0"}, settings.Values{
		Workspace: savedWorkspace, ListenAddress: "127.0.0.1:9000", MaxFileBytes: 2048,
		LLMBaseURL: "https://saved.test/v1", LLMModel: "saved-model", LLMTimeout: time.Minute,
		GoplsMode: "auto", GoplsPath: "gopls",
		TypeScriptLSPMode: "auto", TypeScriptLSPPath: "typescript-language-server",
		SwiftLSPMode: "auto", SwiftLSPPath: "sourcekit-lsp",
		PythonLSPMode: "auto", PythonLSPPath: "pyright-langserver",
		RustLSPMode: "auto", RustLSPPath: "rust-analyzer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace != filepath.Clean(operatorWorkspace) || cfg.ListenAddress != "127.0.0.1:0" {
		t.Fatalf("LoadArgsWithSettings() = workspace %q listen %q", cfg.Workspace, cfg.ListenAddress)
	}
}

func TestLoadAcceptsConfiguredOpenAICompatibleEndpoint(t *testing.T) {
	resetFlags(t)
	workspace := t.TempDir()
	t.Setenv("CODEATLAS_WORKSPACE", workspace)
	t.Setenv("CODEATLAS_LLM_BASE_URL", "http://192.0.2.10:8000/v1/")
	t.Setenv("CODEATLAS_LLM_API_KEY", "apikey")
	t.Setenv("CODEATLAS_LLM_MODEL", "default")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Workspace != filepath.Clean(workspace) {
		t.Fatalf("Workspace = %q, want %q", cfg.Workspace, filepath.Clean(workspace))
	}
	if cfg.LLMBaseURL != "http://192.0.2.10:8000/v1" {
		t.Fatalf("LLMBaseURL = %q", cfg.LLMBaseURL)
	}
	if cfg.LLMAPIKey != "apikey" || cfg.LLMModel != "default" {
		t.Fatalf("LLM config = %#v", cfg)
	}
}

func TestLoadDefaultsToSQLiteDatabasePath(t *testing.T) {
	resetFlags(t)
	llmEnv(t)
	workspace := os.Getenv("CODEATLAS_WORKSPACE")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := filepath.Join(workspace, ".codeatlas", "codeatlas.db")
	if cfg.DatabasePath != want {
		t.Fatalf("DatabasePath = %q, want %q", cfg.DatabasePath, want)
	}
}

func TestLoadRejectsMalformedIntegerEnvironment(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "integer syntax", key: "CODEATLAS_MAX_FILE_BYTES", value: "many"},
		{name: "integer zero", key: "CODEATLAS_MAX_FILE_BYTES", value: "0"},
		{name: "integer negative", key: "CODEATLAS_MAX_FILE_BYTES", value: "-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetFlags(t)
			llmEnv(t)
			t.Setenv(test.key, test.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("Load() error = %v, want invalid %s", err, test.key)
			}
		})
	}
}

func TestLoadReportsInvalidURLSchemeAsRecoverable(t *testing.T) {
	resetFlags(t)
	llmEnv(t)
	t.Setenv("CODEATLAS_LLM_BASE_URL", "ftp://example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if issues := ValidateProvider(cfg); len(issues) == 0 || issues[0].EnvironmentKey != "CODEATLAS_LLM_BASE_URL" {
		t.Fatalf("ValidateProvider() = %#v, want invalid LLM base URL", issues)
	}
}

func TestLoadProbeTimeout(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
		want    time.Duration
	}{
		{"default", "", false, 10 * time.Second},
		{"custom", "3s", false, 3 * time.Second},
		{"zero", "0s", true, 0},
		{"negative", "-5s", true, 0},
		{"garbage", "soon", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlags(t)
			llmEnv(t)
			t.Setenv("CODEATLAS_PROBE_TIMEOUT", tc.value)
			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() accepted invalid probe timeout %q", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.ProbeTimeout != tc.want {
				t.Fatalf("ProbeTimeout = %v, want %v", cfg.ProbeTimeout, tc.want)
			}
		})
	}
}

func TestLoadLLMTimeout(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
		want    time.Duration
	}{
		{"default", "", false, 10 * time.Minute},
		{"custom", "17m", false, 17 * time.Minute},
		{"zero", "0s", true, 0},
		{"negative", "-5s", true, 0},
		{"garbage", "eventually", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlags(t)
			llmEnv(t)
			t.Setenv("CODEATLAS_LLM_TIMEOUT", tc.value)
			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() accepted invalid LLM timeout %q", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.LLMTimeout != tc.want {
				t.Fatalf("LLMTimeout = %v, want %v", cfg.LLMTimeout, tc.want)
			}
		})
	}
}

func TestLoadWatchConfiguration(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.WatchMode != "auto" {
			t.Fatalf("WatchMode = %q, want auto", cfg.WatchMode)
		}
		if cfg.WatchDebounce != 250*time.Millisecond ||
			cfg.WatchMaxBatchDelay != 2*time.Second ||
			cfg.WatchReconcileInterval != 5*time.Minute ||
			cfg.PollInterval != 2*time.Second {
			t.Fatalf("watch durations = debounce %v max %v reconcile %v poll %v",
				cfg.WatchDebounce, cfg.WatchMaxBatchDelay, cfg.WatchReconcileInterval, cfg.PollInterval)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_WATCH_MODE", "polling")
		t.Setenv("CODEATLAS_WATCH_DEBOUNCE", "100ms")
		t.Setenv("CODEATLAS_WATCH_MAX_BATCH_DELAY", "750ms")
		t.Setenv("CODEATLAS_WATCH_RECONCILE_INTERVAL", "30s")
		t.Setenv("CODEATLAS_POLL_INTERVAL", "3s")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.WatchMode != "polling" ||
			cfg.WatchDebounce != 100*time.Millisecond ||
			cfg.WatchMaxBatchDelay != 750*time.Millisecond ||
			cfg.WatchReconcileInterval != 30*time.Second ||
			cfg.PollInterval != 3*time.Second {
			t.Fatalf("watch config = %#v", cfg)
		}
	})

	t.Run("invalid mode", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_WATCH_MODE", "sometimes")

		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CODEATLAS_WATCH_MODE") {
			t.Fatalf("Load() error = %v, want invalid watch mode", err)
		}
	})

	t.Run("invalid duration", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_WATCH_DEBOUNCE", "fast")

		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CODEATLAS_WATCH_DEBOUNCE") {
			t.Fatalf("Load() error = %v, want invalid watch debounce", err)
		}
	})
}

func TestLoadGoplsConfiguration(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GoplsMode != "auto" || cfg.GoplsPath != "gopls" {
			t.Fatalf("gopls config = mode %q path %q", cfg.GoplsMode, cfg.GoplsPath)
		}
	})
	t.Run("override", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_GOPLS", "true")
		t.Setenv("CODEATLAS_GOPLS_PATH", "/opt/bin/gopls")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GoplsMode != "true" || cfg.GoplsPath != "/opt/bin/gopls" {
			t.Fatalf("gopls config = mode %q path %q", cfg.GoplsMode, cfg.GoplsPath)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_GOPLS", "sometimes")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CODEATLAS_GOPLS") {
			t.Fatalf("Load() error = %v", err)
		}
	})
}

func TestLoadTypeScriptLSPConfiguration(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TypeScriptLSPMode != "auto" || cfg.TypeScriptLSPPath != "typescript-language-server" || cfg.TypeScriptSDKPath != "" {
			t.Fatalf("typescript lsp config = mode %q path %q sdk %q", cfg.TypeScriptLSPMode, cfg.TypeScriptLSPPath, cfg.TypeScriptSDKPath)
		}
	})
	t.Run("override", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_TYPESCRIPT_LSP", "true")
		t.Setenv("CODEATLAS_TYPESCRIPT_LSP_PATH", "/opt/bin/typescript-language-server")
		t.Setenv("CODEATLAS_TYPESCRIPT_SDK_PATH", "/opt/lib/node_modules/typescript/lib")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TypeScriptLSPMode != "true" || cfg.TypeScriptLSPPath != "/opt/bin/typescript-language-server" || cfg.TypeScriptSDKPath != "/opt/lib/node_modules/typescript/lib" {
			t.Fatalf("typescript lsp config = mode %q path %q sdk %q", cfg.TypeScriptLSPMode, cfg.TypeScriptLSPPath, cfg.TypeScriptSDKPath)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_TYPESCRIPT_LSP", "sometimes")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CODEATLAS_TYPESCRIPT_LSP") {
			t.Fatalf("Load() error = %v", err)
		}
	})
}

func TestLoadSwiftLSPConfiguration(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SwiftLSPMode != "auto" || cfg.SwiftLSPPath != "sourcekit-lsp" {
			t.Fatalf("swift lsp config = mode %q path %q", cfg.SwiftLSPMode, cfg.SwiftLSPPath)
		}
	})
	t.Run("override", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_SWIFT_LSP", "true")
		t.Setenv("CODEATLAS_SWIFT_LSP_PATH", "/opt/bin/sourcekit-lsp")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SwiftLSPMode != "true" || cfg.SwiftLSPPath != "/opt/bin/sourcekit-lsp" {
			t.Fatalf("swift lsp config = mode %q path %q", cfg.SwiftLSPMode, cfg.SwiftLSPPath)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_SWIFT_LSP", "sometimes")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CODEATLAS_SWIFT_LSP") {
			t.Fatalf("Load() error = %v, want invalid Swift LSP mode", err)
		}
	})
}

func TestLoadPythonLSPConfiguration(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.PythonLSPMode != "auto" || cfg.PythonLSPPath != "pyright-langserver" {
			t.Fatalf("python lsp config = mode %q path %q", cfg.PythonLSPMode, cfg.PythonLSPPath)
		}
	})
	t.Run("override", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_PYTHON_LSP", "true")
		t.Setenv("CODEATLAS_PYTHON_LSP_PATH", "/opt/bin/pyright-langserver")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.PythonLSPMode != "true" || cfg.PythonLSPPath != "/opt/bin/pyright-langserver" {
			t.Fatalf("python lsp config = mode %q path %q", cfg.PythonLSPMode, cfg.PythonLSPPath)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_PYTHON_LSP", "sometimes")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CODEATLAS_PYTHON_LSP") {
			t.Fatalf("Load() error = %v, want invalid Python LSP mode", err)
		}
	})
}

func TestLoadRustLSPConfiguration(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RustLSPMode != "auto" || cfg.RustLSPPath != "rust-analyzer" {
			t.Fatalf("rust lsp config = mode %q path %q", cfg.RustLSPMode, cfg.RustLSPPath)
		}
	})

	t.Run("override", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_RUST_LSP", "true")
		t.Setenv("CODEATLAS_RUST_LSP_PATH", "/opt/bin/rust-analyzer")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RustLSPMode != "true" || cfg.RustLSPPath != "/opt/bin/rust-analyzer" {
			t.Fatalf("rust lsp config = mode %q path %q", cfg.RustLSPMode, cfg.RustLSPPath)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		resetFlags(t)
		llmEnv(t)
		t.Setenv("CODEATLAS_RUST_LSP", "sometimes")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CODEATLAS_RUST_LSP") {
			t.Fatalf("invalid Rust LSP mode error = %v", err)
		}
	})
}

func resetFlags(t *testing.T) {
	t.Helper()
	oldArgs := os.Args
	os.Args = []string{"codeatlas"}
	t.Cleanup(func() {
		os.Args = oldArgs
	})
}
