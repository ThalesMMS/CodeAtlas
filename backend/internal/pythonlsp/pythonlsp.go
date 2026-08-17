// Package pythonlsp integrates Pyright language server over the generic LSP client. It depends only on negotiated
// protocol/capabilities and stays replaceable. It probes the binary, initializes
// with allowlisted options, and keeps open Python documents synced to the
// OverlayStore's DocumentVersion. It never imports user modules, discovers or
// activates environments, runs package scripts, installs tooling, accepts
// workspace edits, or executes server commands.
package pythonlsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// EnableMode mirrors the shared language-server policy: false/true/auto.
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
	CodeNotFound           = "PYRIGHT_LSP_NOT_FOUND"
	CodeVersionProbeFailed = "PYRIGHT_LSP_VERSION_PROBE_FAILED"
	CodeStartFailed        = "PYRIGHT_LSP_START_FAILED"
	CodeInitializeFailed   = "PYRIGHT_LSP_INITIALIZE_FAILED"
	CodeCapabilityMissing  = "PYRIGHT_LSP_CAPABILITY_MISSING"
	CodeCrashed            = "PYRIGHT_LSP_CRASHED"
)

// ProviderID is the semantic provider id for the Pyright language server.
const ProviderID semantic.SemanticProviderID = "pyright-langserver"

// Config configures the Pyright language server integration.
type Config struct {
	Enable         EnableMode
	Path           string
	Args           []string
	StartTimeout   time.Duration
	RequestTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Enable == "" {
		c.Enable = EnableAuto
	}
	if c.Path == "" {
		c.Path = "pyright-langserver"
	}
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

// Manager owns the Pyright language server lifecycle for one workspace.
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
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			resolved, err := exec.LookPath(path)
			if err != nil {
				return "", err
			}
			// The npm package installs `pyright` beside `pyright-langserver`.
			// Probing only that explicit sibling reports the tool version without
			// searching virtual environments or importing workspace code.
			cli := filepath.Join(filepath.Dir(resolved), "pyright")
			versionOutput, err := exec.CommandContext(probeCtx, cli, "--version").CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("probe pyright version: %w", err)
			}
			match := pythonVersion.FindSubmatch(versionOutput)
			if len(match) != 2 {
				return "", errors.New("pyright version was not reported")
			}
			return string(match[1]), nil
		}, decision: DecisionDisabled,
		registered: map[string]struct{}{},
		documents:  lspadapter.NewDocumentState(), diagnostics: lspadapter.NewDiagnosticStore(),
	}
}

// Mandatory reports whether the provider is required for readiness.
func (m *Manager) Mandatory(hasPythonFiles bool) bool {
	switch m.config.Enable {
	case EnableTrue:
		return true
	case EnableAuto:
		return hasPythonFiles
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
func (m *Manager) Start(ctx context.Context, hasPythonFiles bool) error {
	if !lspadapter.ShouldStart(string(m.config.Enable), hasPythonFiles) {
		m.setDecision(DecisionDisabled, "")
		return nil
	}
	args := append([]string{"--stdio"}, m.config.Args...)
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
		return fmt.Errorf("pyright-langserver lifecycle: %w", err)
	}
	m.mu.Lock()
	m.documents.Reset()
	m.diagnostics.Reset()
	m.registered = make(map[string]struct{})
	m.client = client
	m.decision = DecisionEnabled
	m.reason = ""
	m.sessionSeq++
	m.sessionID = fmt.Sprintf("pyright-langserver:%d", m.sessionSeq)
	m.mu.Unlock()
	return nil
}

func (m *Manager) initialize(ctx context.Context, client LSPClient) error {
	rootURI := lspconv.PathToURI(m.workspaceRoot)
	params := map[string]any{
		"processId":        nil,
		"rootUri":          rootURI,
		"clientInfo":       map[string]string{"name": "CodeAtlas", "version": "1.0"},
		"workspaceFolders": []map[string]string{{"uri": rootURI, "name": "workspace"}},
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
		return fmt.Errorf("pyright-langserver initialize: %w", err)
	}
	if result.ServerInfo.Version != "" {
		m.mu.Lock()
		m.version = semantic.SanitizeDetail(result.ServerInfo.Version)
		m.mu.Unlock()
	}
	if !result.Capabilities.HoverProvider.Present() || !result.Capabilities.DefinitionProvider.Present() {
		m.setDecision(DecisionUnavailable, CodeCapabilityMissing)
		return errors.New("pyright-langserver: required capabilities missing")
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

// handleConfiguration returns one empty result per requested section. CodeAtlas
// never opts into environment discovery, execution, formatting, commands, or
// workspace-edit behavior.
func (m *Manager) handleConfiguration(_ context.Context, params json.RawMessage) (any, error) {
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return []map[string]any{}, nil
	}
	return make([]map[string]any, len(payload.Items)), nil
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

var pythonVersion = regexp.MustCompile(`(?i)pyright\s+([0-9]+(?:\.[0-9]+)+(?:[-+][^\s]+)?)`)
