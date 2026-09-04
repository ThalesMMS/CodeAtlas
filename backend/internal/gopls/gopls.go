// Package gopls integrates a single gopls instance per workspace: it probes the
// binary, negotiates capabilities via the LSP initialize handshake, answers
// workspace/configuration conservatively, and keeps open Go documents synced to
// the OverlayStore's DocumentVersion. It never installs/updates gopls at
// runtime, runs no workspace commands, and accepts no workspace edits. Packaged
// desktop builds may provide a pinned gopls executable alongside CodeAtlas.
package gopls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/lspadapter"
	"github.com/ThalesMMS/CodeAtlas/internal/lspclient"
	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// EnableMode is the configured gopls policy.
type EnableMode string

const (
	EnableFalse EnableMode = "false" // capability disabled; never start a process
	EnableTrue  EnableMode = "true"  // attempt startup even without detected Go files
	EnableAuto  EnableMode = "auto"  // attempt for Go workspaces, disabled otherwise
)

// Decision is the effective, observable capability decision.
type Decision string

const (
	DecisionDisabled    Decision = "disabled"
	DecisionEnabled     Decision = "enabled"
	DecisionUnavailable Decision = "unavailable"
)

// Stable error codes surfaced as capability reasons.
const (
	CodeNotFound           = "GOPLS_NOT_FOUND"
	CodeVersionProbeFailed = "GOPLS_VERSION_PROBE_FAILED"
	CodeStartFailed        = "GOPLS_START_FAILED"
	CodeInitializeFailed   = "GOPLS_INITIALIZE_FAILED"
	CodeCapabilityMissing  = "GOPLS_CAPABILITY_MISSING"
	CodeCrashed            = "GOPLS_CRASHED"
)

// Config configures the gopls integration.
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
		c.Path = "gopls"
	}
	c.Path = resolveBundledExecutable(c.Path)
	if c.StartTimeout <= 0 {
		c.StartTimeout = 20 * time.Second
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 20 * time.Second
	}
	return c
}

// LSPClient is the subset of lspclient.Client the manager needs; an interface so a
// fake LSP server can drive the manager in tests.
type LSPClient = lspadapter.Client

// ClientFactory builds an LSPClient for a process config. Production uses
// lspclient.NewClient; tests inject a fake.
type ClientFactory = lspadapter.ClientFactory

// DefaultClientFactory wraps the real lspclient.Client.
func DefaultClientFactory(config lspclient.ProcessConfig) LSPClient {
	return lspclient.NewClient(config)
}

// Manager owns the gopls lifecycle for one workspace.
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
	sessionID   string
	sessionSeq  uint64
	documents   *lspadapter.DocumentState
	diagnostics *lspadapter.DiagnosticStore
}

// NewManager builds a manager. factory may be nil (uses the real client);
// hasGoFiles reflects the initial scan and resolves the auto policy.
func NewManager(config Config, workspaceRoot string, factory ClientFactory) *Manager {
	if factory == nil {
		factory = DefaultClientFactory
	}
	return &Manager{
		config:        config.withDefaults(),
		workspaceRoot: workspaceRoot,
		factory:       factory,
		versionProber: func(ctx context.Context, path string) (string, error) {
			return lspadapter.ProbeVersion(ctx, path, []string{"version"}, versionLine)
		},
		decision:    DecisionDisabled,
		documents:   lspadapter.NewDocumentState(),
		diagnostics: lspadapter.NewDiagnosticStore(),
	}
}

// Decision returns the effective decision and reason code (empty if none).
func (m *Manager) Decision() (Decision, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.decision, m.reason
}

// Version returns the probed gopls version, if any.
func (m *Manager) Version() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version
}

// Encoding returns the negotiated position encoding.
func (m *Manager) Encoding() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.encoding
}

// SessionID identifies one initialized gopls process. Facts from a previous
// process are rejected after a restart.
func (m *Manager) SessionID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessionID
}

// Start probes and initializes gopls per the enable policy. With EnableFalse it
// stays disabled without touching a process. A failure sets decision=unavailable
// with a stable reason; the composition root decides how to expose its optional
// AST-only degradation.
func (m *Manager) Start(ctx context.Context, hasGoFiles bool) error {
	if !lspadapter.ShouldStart(string(m.config.Enable), hasGoFiles) {
		m.setDecision(DecisionDisabled, "")
		return nil
	}
	client, version, err := lspadapter.Start(ctx, lspadapter.StartConfig{
		Executable: m.config.Path, Args: m.config.Args, WorkingDir: m.workspaceRoot,
		RequestTimeout: m.config.RequestTimeout, StartTimeout: m.config.StartTimeout,
		Probe: m.versionProber, Factory: m.factory,
		Configure: func(client lspadapter.Client) {
			client.OnRequest("workspace/applyEdit", lspadapter.DenyWorkspaceEdit)
			client.OnRequest("workspace/configuration", m.handleConfiguration)
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
				if errors.Is(startErr, exec.ErrNotFound) || strings.Contains(startErr.Error(), "executable file not found") {
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
		return fmt.Errorf("gopls lifecycle: %w", err)
	}

	m.mu.Lock()
	// A newly initialized process cannot own sync or diagnostics published by a
	// prior session.
	m.documents.Reset()
	m.diagnostics.Reset()
	m.client = client
	m.decision = DecisionEnabled
	m.reason = ""
	m.sessionSeq++
	m.sessionID = fmt.Sprintf("gopls:%d", m.sessionSeq)
	m.mu.Unlock()
	return nil
}

func (m *Manager) initialize(ctx context.Context, client LSPClient) error {
	rootURI := lspconv.PathToURI(m.workspaceRoot)
	params := initializeParams{
		ProcessID:        nil,
		RootURI:          rootURI,
		ClientInfo:       clientInfo{Name: "CodeAtlas", Version: "1.0"},
		WorkspaceFolders: []workspaceFolder{{URI: rootURI, Name: "workspace"}},
		InitializationOptions: initializationOptions{
			SemanticTokens: true,
		},
		Capabilities: clientCapabilities{
			// Position encodings in explicit preference order.
			General: generalCapabilities{PositionEncodings: []string{"utf-8", "utf-16"}},
			TextDocument: textDocumentClientCapabilities{
				Synchronization: syncCapabilities{DidSave: true},
				Hover:           &hoverClientCapabilities{},
				Definition:      &emptyCapability{},
				References:      &emptyCapability{},
				Implementation:  &emptyCapability{},
				CallHierarchy:   &emptyCapability{},
				SemanticTokens: semanticTokenClientCapabilities{
					DynamicRegistration: false,
					Requests: semanticTokenRequests{
						Range: false,
						Full:  true,
					},
					TokenTypes:              semantic.CanonicalSemanticTokenTypes,
					TokenModifiers:          semantic.CanonicalSemanticTokenModifiers,
					Formats:                 []string{"relative"},
					OverlappingTokenSupport: false,
					MultilineTokenSupport:   false,
				},
			},
			// Intentionally NOT announcing workspace edit / dynamic registration.
		},
	}
	var result initializeResult
	if err := client.Request(ctx, "initialize", params, &result); err != nil {
		m.setDecision(DecisionUnavailable, CodeInitializeFailed)
		return fmt.Errorf("gopls initialize: %w", err)
	}
	if !hasRequiredCapabilities(result.Capabilities) {
		m.setDecision(DecisionUnavailable, CodeCapabilityMissing)
		return errors.New("gopls: required capabilities missing")
	}
	encoding := result.Capabilities.PositionEncoding
	if encoding == "" {
		encoding = "utf-16" // LSP default when the server does not negotiate one
	}
	m.mu.Lock()
	m.serverCaps = result.Capabilities
	m.encoding = encoding
	m.mu.Unlock()

	if err := client.Notify(ctx, "initialized", struct{}{}); err != nil {
		m.setDecision(DecisionUnavailable, CodeInitializeFailed)
		return fmt.Errorf("gopls initialized: %w", err)
	}
	return nil
}

func hasRequiredCapabilities(caps serverCapabilities) bool {
	// Hover + definition are the minimum the adapter relies on.
	return caps.HoverProvider.Present() && caps.DefinitionProvider.Present()
}

// handleConfiguration answers workspace/configuration conservatively: no
// auto-editing actions, no commands, no secrets. The payload is fixed and testable.
func (m *Manager) handleConfiguration(_ context.Context, _ json.RawMessage) (any, error) {
	return []map[string]any{{
		"usePlaceholders":    false,
		"completeUnimported": false,
		"staticcheck":        false,
		"codelenses":         map[string]bool{"generate": false, "test": false, "tidy": false, "upgrade_dependency": false},
		"verboseOutput":      false,
	}}, nil
}

func (m *Manager) setDecision(decision Decision, reason string) {
	m.mu.Lock()
	m.decision = decision
	m.reason = reason
	m.mu.Unlock()
}

// Shutdown stops new queries and tears down gopls with the LSP shutdown/exit
// handshake, bounded by the request timeout, never deadlocking.
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

var versionLine = regexp.MustCompile(`v\d+\.\d+\.\d+\S*`)
