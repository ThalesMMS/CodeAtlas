// Package typescriptlsp integrates the community typescript-language-server
// (run with --stdio) over the generic LSP client. It depends only on negotiated
// protocol/capabilities and stays replaceable. It probes the binary, initializes
// with allowlisted options, and keeps open JS/TS/TSX documents synced to the
// OverlayStore's DocumentVersion. It never runs npm/npx/package scripts, installs
// anything, accepts workspace edits, or runs server commands.
package typescriptlsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/lspadapter"
	"github.com/ThalesMMS/CodeAtlas/internal/lspclient"
	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// EnableMode mirrors the gopls policy: false/true/auto (auto requires JS/TS files).
type EnableMode string

const (
	EnableFalse EnableMode = "false"
	EnableTrue  EnableMode = "true"
	EnableAuto  EnableMode = "auto"
)

// Decision is the effective, observable decision.
type Decision string

const (
	DecisionDisabled    Decision = "disabled"
	DecisionEnabled     Decision = "enabled"
	DecisionUnavailable Decision = "unavailable"
)

// Stable error codes.
const (
	CodeNotFound           = "TYPESCRIPT_LSP_NOT_FOUND"
	CodeVersionProbeFailed = "TYPESCRIPT_LSP_VERSION_PROBE_FAILED"
	CodeSDKNotFound        = "TYPESCRIPT_SDK_NOT_FOUND"
	CodeStartFailed        = "TYPESCRIPT_LSP_START_FAILED"
	CodeInitializeFailed   = "TYPESCRIPT_LSP_INITIALIZE_FAILED"
	CodeCapabilityMissing  = "TYPESCRIPT_LSP_CAPABILITY_MISSING"
	CodeCrashed            = "TYPESCRIPT_LSP_CRASHED"
)

// ProviderID is the semantic provider id for the TypeScript language server.
const ProviderID semantic.SemanticProviderID = "typescript-lsp"

// Config configures the TypeScript LSP integration.
type Config struct {
	Enable         EnableMode
	Path           string
	Args           []string
	SDKPath        string // optional TypeScript SDK (tsserver) path; validated when set
	StartTimeout   time.Duration
	RequestTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Enable == "" {
		c.Enable = EnableAuto
	}
	if c.Path == "" {
		c.Path = "typescript-language-server"
	}
	c.Path = resolveBundledExecutable(c.Path)
	c.SDKPath = resolveBundledSDKPath(c.SDKPath)
	if c.StartTimeout <= 0 {
		c.StartTimeout = 25 * time.Second
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 20 * time.Second
	}
	return c
}

// LSPClient is the subset of lspclient.Client the manager needs (injectable for tests).
type LSPClient = lspadapter.Client

// ClientFactory builds an LSPClient for a process config.
type ClientFactory = lspadapter.ClientFactory

// DefaultClientFactory wraps the real lspclient.Client.
func DefaultClientFactory(config lspclient.ProcessConfig) LSPClient {
	return lspclient.NewClient(config)
}

// Manager owns the TypeScript LSP lifecycle for one workspace.
type Manager struct {
	config        Config
	workspaceRoot string
	factory       ClientFactory
	versionProber func(ctx context.Context, path string) (string, error)

	mu          sync.Mutex
	client      LSPClient
	decision    Decision
	reason      string
	version     string
	encoding    string
	serverCaps  serverCapabilities
	registered  map[string]struct{} // dynamic registration ids accepted
	sessionID   string
	sessionSeq  uint64
	documents   *lspadapter.DocumentState
	diagnostics *lspadapter.DiagnosticStore
}

// NewManager builds a manager.
func NewManager(config Config, workspaceRoot string, factory ClientFactory) *Manager {
	if factory == nil {
		factory = DefaultClientFactory
	}
	return &Manager{
		config: config.withDefaults(), workspaceRoot: workspaceRoot, factory: factory,
		versionProber: func(ctx context.Context, path string) (string, error) {
			return lspadapter.ProbeVersion(ctx, path, []string{"--version"}, versionLine)
		}, decision: DecisionDisabled,
		registered: map[string]struct{}{},
		documents:  lspadapter.NewDocumentState(), diagnostics: lspadapter.NewDiagnosticStore(),
	}
}

// Mandatory reports whether the provider is required for readiness.
func (m *Manager) Mandatory(hasJSTSFiles bool) bool {
	switch m.config.Enable {
	case EnableTrue:
		return true
	case EnableAuto:
		return hasJSTSFiles
	default:
		return false
	}
}

func (m *Manager) Decision() (Decision, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.decision, m.reason
}

func (m *Manager) Version() string   { m.mu.Lock(); defer m.mu.Unlock(); return m.version }
func (m *Manager) Encoding() string  { m.mu.Lock(); defer m.mu.Unlock(); return m.encoding }
func (m *Manager) SessionID() string { m.mu.Lock(); defer m.mu.Unlock(); return m.sessionID }

// Start probes and initializes per the enable policy.
func (m *Manager) Start(ctx context.Context, hasJSTSFiles bool) error {
	if !lspadapter.ShouldStart(string(m.config.Enable), hasJSTSFiles) {
		m.setDecision(DecisionDisabled, "")
		return nil
	}
	if m.config.SDKPath != "" {
		tsserverPath := filepath.Join(m.config.SDKPath, "tsserver.js")
		info, statErr := os.Stat(tsserverPath)
		if statErr != nil || !info.Mode().IsRegular() {
			m.setDecision(DecisionUnavailable, CodeSDKNotFound)
			return fmt.Errorf("typescript sdk not found at %s", tsserverPath)
		}
	}

	// --stdio is added internally, never duplicated.
	args := append([]string{}, m.config.Args...)
	if !containsArg(args, "--stdio") {
		args = append(args, "--stdio")
	}
	client, version, err := lspadapter.Start(ctx, lspadapter.StartConfig{
		Executable: m.config.Path, Args: args, WorkingDir: m.workspaceRoot,
		RequestTimeout: m.config.RequestTimeout, StartTimeout: m.config.StartTimeout,
		Probe: m.versionProber, Factory: m.factory,
		Configure: func(client lspadapter.Client) {
			client.OnRequest("workspace/applyEdit", lspadapter.DenyWorkspaceEdit)
			client.OnRequest("workspace/configuration", m.handleConfiguration)
			client.OnRequest("client/registerCapability", m.handleRegister)
			client.OnRequest("client/unregisterCapability", m.handleUnregister)
			client.OnNotification("textDocument/publishDiagnostics", m.handlePublishDiagnostics)
		},
		Initialize: m.initialize,
	})
	if version != "" {
		m.mu.Lock()
		m.version = version
		m.mu.Unlock()
	}
	if err != nil {
		var startErr *lspadapter.StartError
		if errors.As(err, &startErr) {
			switch startErr.Stage {
			case lspadapter.StageProbe:
				code := CodeVersionProbeFailed
				if errors.Is(startErr, exec.ErrNotFound) {
					code = CodeNotFound
				}
				m.setDecision(DecisionUnavailable, code)
			case lspadapter.StageStart:
				m.setDecision(DecisionUnavailable, CodeStartFailed)
			case lspadapter.StageInitialize:
				if decision, _ := m.Decision(); decision != DecisionUnavailable {
					m.setDecision(DecisionUnavailable, CodeInitializeFailed)
				}
			}
		}
		return fmt.Errorf("typescript lsp lifecycle: %w", err)
	}
	m.mu.Lock()
	m.documents.Reset()
	m.diagnostics.Reset()
	m.registered = make(map[string]struct{})
	m.client = client
	m.decision = DecisionEnabled
	m.reason = ""
	m.sessionSeq++
	m.sessionID = fmt.Sprintf("typescript-lsp:%d", m.sessionSeq)
	m.mu.Unlock()
	return nil
}

func (m *Manager) initialize(ctx context.Context, client LSPClient) error {
	rootURI := lspconv.PathToURI(m.workspaceRoot)
	initOptions := map[string]any{
		"hostInfo":                          "CodeAtlas",
		"preferences":                       map[string]any{"includeCompletionsForModuleExports": false},
		"disableAutomaticTypingAcquisition": true,
	}
	if m.config.SDKPath != "" {
		initOptions["tsserver"] = map[string]any{"path": filepath.Join(m.config.SDKPath, "tsserver.js")}
	}
	params := map[string]any{
		"processId":             nil,
		"rootUri":               rootURI,
		"clientInfo":            map[string]string{"name": "CodeAtlas", "version": "1.0"},
		"workspaceFolders":      []map[string]string{{"uri": rootURI, "name": "workspace"}},
		"initializationOptions": initOptions,
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-8", "utf-16"}},
			"textDocument": map[string]any{
				"synchronization": map[string]any{"didSave": true},
				"hover":           map[string]any{},
				"definition":      map[string]any{},
				"references":      map[string]any{},
				"implementation":  map[string]any{},
				"callHierarchy":   map[string]any{},
				"semanticTokens": map[string]any{
					"dynamicRegistration":     false,
					"requests":                map[string]any{"full": true, "range": false},
					"tokenTypes":              semantic.CanonicalSemanticTokenTypes,
					"tokenModifiers":          semantic.CanonicalSemanticTokenModifiers,
					"formats":                 []string{"relative"},
					"overlappingTokenSupport": false,
					"multilineTokenSupport":   false,
				},
			},
		},
	}
	var result initializeResult
	if err := client.Request(ctx, "initialize", params, &result); err != nil {
		m.setDecision(DecisionUnavailable, CodeInitializeFailed)
		return fmt.Errorf("typescript lsp initialize: %w", err)
	}
	if !result.Capabilities.HoverProvider.Present() || !result.Capabilities.DefinitionProvider.Present() {
		m.setDecision(DecisionUnavailable, CodeCapabilityMissing)
		return errors.New("typescript lsp: required capabilities missing")
	}
	encoding := result.Capabilities.PositionEncoding
	if encoding == "" {
		encoding = "utf-16"
	}
	m.mu.Lock()
	m.serverCaps = result.Capabilities
	m.encoding = encoding
	m.mu.Unlock()
	return client.Notify(ctx, "initialized", struct{}{})
}

// handleConfiguration returns conservative, allowlisted settings: no code actions
// on save, no organize imports, no plugins.
func (m *Manager) handleConfiguration(_ context.Context, _ json.RawMessage) (any, error) {
	return []map[string]any{{
		"typescript": map[string]any{"format": map[string]bool{"enable": false}},
		"javascript": map[string]any{"format": map[string]bool{"enable": false}},
	}}, nil
}

// allowlistedDynamic are the only methods accepted via client/registerCapability.
var allowlistedDynamic = map[string]struct{}{
	"textDocument/hover": {}, "textDocument/definition": {}, "textDocument/references": {},
	"textDocument/implementation": {}, "textDocument/didChange": {}, "textDocument/publishDiagnostics": {},
	"textDocument/semanticTokens": {},
}

func (m *Manager) handleRegister(_ context.Context, params json.RawMessage) (any, error) {
	var payload struct {
		Registrations []struct {
			ID     string `json:"id"`
			Method string `json:"method"`
		} `json:"registrations"`
	}
	_ = json.Unmarshal(params, &payload)
	m.mu.Lock()
	for _, registration := range payload.Registrations {
		if _, ok := allowlistedDynamic[registration.Method]; ok {
			m.registered[registration.ID] = struct{}{}
		}
		// Unknown/mutating methods are ignored with a protocol-valid (nil) response.
	}
	m.mu.Unlock()
	return nil, nil
}

func (m *Manager) handleUnregister(_ context.Context, params json.RawMessage) (any, error) {
	var payload struct {
		Unregisterations []struct {
			ID string `json:"id"`
		} `json:"unregisterations"`
	}
	_ = json.Unmarshal(params, &payload)
	m.mu.Lock()
	for _, item := range payload.Unregisterations {
		delete(m.registered, item.ID)
	}
	m.mu.Unlock()
	return nil, nil
}

func (m *Manager) setDecision(decision Decision, reason string) {
	m.mu.Lock()
	m.decision = decision
	m.reason = reason
	m.mu.Unlock()
}

// Shutdown stops queries and tears down with shutdown/exit, bounded by a timeout.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	client := m.client
	m.client = nil
	m.decision = DecisionDisabled
	m.documents.Reset()
	m.diagnostics.Reset()
	m.mu.Unlock()
	if client == nil {
		return nil
	}
	return lspadapter.Shutdown(ctx, client, m.config.RequestTimeout)
}

// --- helpers ---

var versionLine = regexp.MustCompile(`\d+\.\d+\.\d+\S*`)

func containsArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}
