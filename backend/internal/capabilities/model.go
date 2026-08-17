// Package capabilities implements a typed registry of operational capabilities
// and a deterministic, panic-safe runner of local probes. It feeds the
// readiness state machine with auditable, structured results. This package owns
// no HTTP, indexer or AI logic; it only inspects the local environment.
package capabilities

import (
	"context"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

// CapabilityID is the stable identifier of a single capability.
type CapabilityID string

// Well-known local capability identifiers. These are stable strings: external
// diagnostics and tests may rely on them.
const (
	CapabilityWorkspace        CapabilityID = "workspace"
	CapabilityStateArea        CapabilityID = "state-area"
	CapabilityStore            CapabilityID = "store"
	CapabilityTreeSitterGo     CapabilityID = "tree-sitter-go"
	CapabilityTreeSitterJS     CapabilityID = "tree-sitter-js"
	CapabilityTreeSitterTS     CapabilityID = "tree-sitter-ts"
	CapabilityTreeSitterTSX    CapabilityID = "tree-sitter-tsx"
	CapabilityTreeSitterSwift  CapabilityID = "tree-sitter-swift"
	CapabilityTreeSitterPython CapabilityID = "tree-sitter-python"
	CapabilityTreeSitterRust   CapabilityID = "tree-sitter-rust"
	CapabilityFrontendAssets   CapabilityID = "frontend-assets"
	CapabilityWorkspaceWatch   CapabilityID = "workspace-watch"
	CapabilityGopls            CapabilityID = "gopls"
	CapabilityTypeScriptLSP    CapabilityID = "typescript-lsp"
	CapabilitySourceKitLSP     CapabilityID = "sourcekit-lsp"
	CapabilityPythonLSP        CapabilityID = "python-lsp"
	CapabilityRustAnalyzer     CapabilityID = "rust-analyzer"
)

// Requirement says whether a capability must be available for readiness.
type Requirement string

const (
	Required Requirement = "required"
	Optional Requirement = "optional"
)

// CapabilityState is the lifecycle state of a single capability.
type CapabilityState string

const (
	CapabilityUnknown     CapabilityState = "unknown"
	CapabilityChecking    CapabilityState = "checking"
	CapabilityAvailable   CapabilityState = "available"
	CapabilityUnavailable CapabilityState = "unavailable"
	CapabilityDisabled    CapabilityState = "disabled"
)

// Stable error codes for diagnosable failure modes. Probes return these instead
// of free-form text so callers can branch on the cause.
const (
	ErrCodePanic                 = "PROBE_PANIC"
	ErrCodeTimeout               = "PROBE_TIMEOUT"
	ErrCodeCancelled             = "PROBE_CANCELLED"
	ErrCodeWorkspaceMissing      = "WORKSPACE_MISSING"
	ErrCodeWorkspaceNotDir       = "WORKSPACE_NOT_DIR"
	ErrCodeWorkspaceUnreadable   = "WORKSPACE_UNREADABLE"
	ErrCodeWorkspaceUnresolvable = "WORKSPACE_UNRESOLVABLE"
	ErrCodeStateAreaUnwritable   = "STATE_AREA_UNWRITABLE"
	ErrCodeStoreUnreadable       = "STORE_UNREADABLE"
	ErrCodeStoreCorrupted        = "STORE_CORRUPTED"
	ErrCodeTreeSitterParse       = "TREE_SITTER_PARSE_FAILED"
	ErrCodeTreeSitterRoot        = "TREE_SITTER_UNEXPECTED_ROOT"
	ErrCodeFrontendAssetMissing  = "FRONTEND_ASSET_MISSING"
	ErrCodeFrontendAssetEmpty    = "FRONTEND_ASSET_EMPTY"
)

// Result is the outcome of running one probe. It is a value type with no shared
// references except Metadata, which the runner owns per result.
type Result struct {
	ID          CapabilityID      `json:"id"`
	Requirement Requirement       `json:"requirement"`
	State       CapabilityState   `json:"state"`
	CheckedAt   time.Time         `json:"checkedAt"`
	Duration    time.Duration     `json:"duration"`
	ErrorCode   string            `json:"errorCode,omitempty"`
	Message     string            `json:"message,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Probe inspects one capability of the local environment.
type Probe interface {
	ID() CapabilityID
	Requirement() Requirement
	Run(ctx context.Context) Result
}

// ResultReceiver is the minimal sink the runner uses to push results as they
// complete. It is defined here (the point of consumption) so that the
// capabilities package never imports readiness, avoiding a circular dependency.
// The readiness coordinator can satisfy this interface structurally.
type ResultReceiver interface {
	UpdateCapability(Result)
}

// probeBase carries the identity shared by every probe and provides result
// constructors so probes stay focused on their checks.
type probeBase struct {
	id          CapabilityID
	requirement Requirement
}

func (b probeBase) ID() CapabilityID         { return b.id }
func (b probeBase) Requirement() Requirement { return b.requirement }

func (b probeBase) available(start time.Time, metadata map[string]string) Result {
	return Result{
		ID:          b.id,
		Requirement: b.requirement,
		State:       CapabilityAvailable,
		CheckedAt:   time.Now().UTC(),
		Duration:    time.Since(start),
		Metadata:    metadata,
	}
}

func (b probeBase) unavailable(start time.Time, code, message string) Result {
	return Result{
		ID:          b.id,
		Requirement: b.requirement,
		State:       CapabilityUnavailable,
		CheckedAt:   time.Now().UTC(),
		Duration:    time.Since(start),
		ErrorCode:   code,
		Message:     sanitizeMessage(message),
	}
}

// sanitizeMessage collapses whitespace and bounds length so probe messages never
// leak file contents, tokens or secrets through verbose payloads.
func sanitizeMessage(message string) string {
	return textutil.CompactMessage(message, 300)
}
