// Package readiness implements a thread-safe lifecycle state machine and a
// ReadinessCoordinator that is the single source of truth for the operational
// state of CodeAtlas. The package is intentionally free of infrastructure
// dependencies (http, indexer, store, ai): it only models state, transitions
// and structured diagnostics so it can be embedded by any layer.
package readiness

import (
	"errors"
	"time"
)

// State is the operational lifecycle state of the process.
type State string

const (
	// StateBooting is the initial state before any capability has been probed.
	StateBooting State = "BOOTING"
	// StateProbingCapabilities runs the mandatory local/remote probes.
	StateProbingCapabilities State = "PROBING_CAPABILITIES"
	// StateMigratingStore migrates/rebuilds the store backend (JSON→SQLite) before
	// indexing. During this state only diagnostics/progress endpoints are served.
	StateMigratingStore State = "MIGRATING_STORE"
	// StateIndexing performs the initial indexing of the workspace.
	StateIndexing State = "INDEXING"
	// StateGeneratingArtifacts generates required artifacts (e.g. DeepWiki).
	StateGeneratingArtifacts State = "GENERATING_REQUIRED_ARTIFACTS"
	// StateReady means every mandatory capability is valid and the process serves traffic.
	StateReady State = "READY"
	// StateFailed is a terminal-until-restart state carrying a structured fatal cause.
	StateFailed State = "FAILED"
	// StateShuttingDown is the terminal state reached during graceful shutdown.
	StateShuttingDown State = "SHUTTING_DOWN"
)

// ErrInvalidTransition is returned when a transition is not permitted by the
// state machine. Callers can match it with errors.Is.
var ErrInvalidTransition = errors.New("readiness: invalid state transition")

// validTransitions encodes every permitted edge of the state machine.
//
// Notes:
//   - A fatal error may occur at any operational moment, so FAILED is reachable
//     from every operational state (but never from FAILED itself, which would
//     erase the first cause, nor from SHUTTING_DOWN).
//   - Graceful shutdown is reachable from any non-terminal state, including
//     FAILED.
//   - FAILED never returns to READY silently; recovery must start a new,
//     auditable cycle from a fresh coordinator.
//   - SHUTTING_DOWN is terminal.
var validTransitions = map[State][]State{
	StateBooting:             {StateProbingCapabilities, StateFailed, StateShuttingDown},
	StateProbingCapabilities: {StateMigratingStore, StateIndexing, StateFailed, StateShuttingDown},
	StateMigratingStore:      {StateIndexing, StateFailed, StateShuttingDown},
	StateIndexing:            {StateGeneratingArtifacts, StateReady, StateFailed, StateShuttingDown},
	StateGeneratingArtifacts: {StateReady, StateFailed, StateShuttingDown},
	StateReady:               {StateFailed, StateShuttingDown},
	StateFailed:              {StateShuttingDown},
	StateShuttingDown:        {},
}

// CanTransition reports whether moving from one state to another is permitted.
func CanTransition(from, to State) bool {
	for _, candidate := range validTransitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// CapabilityStatus describes the result of probing one mandatory capability.
type CapabilityStatus struct {
	Name      string    `json:"name"`
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checkedAt"`
	Error     string    `json:"error,omitempty"`
}

// FatalError is the structured cause of a FAILED state. The first cause is
// preserved; subsequent causes are accumulated in SecondaryErrors so the
// original failure is never lost.
type FatalError struct {
	Code            string    `json:"code"`
	Message         string    `json:"message"`
	OccurredAt      time.Time `json:"occurredAt"`
	SecondaryErrors []string  `json:"secondaryErrors,omitempty"`
}

// clone returns a deep copy so callers cannot mutate coordinator internals.
func (e *FatalError) clone() *FatalError {
	if e == nil {
		return nil
	}
	copied := *e
	if len(e.SecondaryErrors) > 0 {
		copied.SecondaryErrors = append([]string(nil), e.SecondaryErrors...)
	}
	return &copied
}

// TransitionRecord is one entry of the bounded transition history.
type TransitionRecord struct {
	From   State     `json:"from"`
	To     State     `json:"to"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

// Snapshot is an immutable, deep-copied view of the coordinator state. Mutating
// any field of a returned Snapshot never affects the coordinator.
type Snapshot struct {
	State             State              `json:"state"`
	LastTransition    time.Time          `json:"lastTransition"`
	CurrentStep       string             `json:"currentStep,omitempty"`
	FatalError        *FatalError        `json:"fatalError,omitempty"`
	Capabilities      []CapabilityStatus `json:"capabilities"`
	IndexRevision     string             `json:"indexRevision,omitempty"`
	TransitionHistory []TransitionRecord `json:"transitionHistory"`
}

// StateChangeEvent is delivered to subscribers after a successful transition.
// Snapshot carries the coordinator state as observed immediately after the
// change.
type StateChangeEvent struct {
	From     State     `json:"from"`
	To       State     `json:"to"`
	At       time.Time `json:"at"`
	Snapshot Snapshot  `json:"snapshot"`
}
