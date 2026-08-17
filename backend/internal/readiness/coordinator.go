package readiness

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// maxHistory bounds the in-memory transition history kept for diagnostics.
const maxHistory = 20

// subscriberBuffer is the per-subscriber channel buffer. Slow subscribers drop
// events instead of blocking transitions.
const subscriberBuffer = 16

// Coordinator is the single source of truth for the operational state. It is
// safe for concurrent use and never blocks a transition on a slow subscriber.
type Coordinator struct {
	mu                 sync.RWMutex
	state              State
	lastTransition     time.Time
	currentStep        string
	indexRevision      string
	fatal              *FatalError
	capabilities       map[string]CapabilityStatus
	history            []TransitionRecord
	subscribers        map[chan StateChangeEvent]struct{}
	onEventDropped     func()
	configurationRetry chan struct{}
}

// SetEventDropObserver installs a non-blocking callback for each subscriber
// delivery dropped because its buffer is full.
func (c *Coordinator) SetEventDropObserver(observer func()) {
	c.mu.Lock()
	c.onEventDropped = observer
	c.mu.Unlock()
}

// NewCoordinator returns a coordinator in the BOOTING state with empty
// collections. The BOOTING entry is recorded so history always has an origin.
func NewCoordinator() *Coordinator {
	now := time.Now().UTC()
	return &Coordinator{
		state:              StateBooting,
		lastTransition:     now,
		capabilities:       make(map[string]CapabilityStatus),
		history:            []TransitionRecord{{From: StateBooting, To: StateBooting, At: now, Reason: "bootstrap"}},
		subscribers:        make(map[chan StateChangeEvent]struct{}),
		configurationRetry: make(chan struct{}, 1),
	}
}

// SignalConfigurationRetry wakes a bootstrap that is waiting for repaired
// provider settings. The single-slot channel coalesces repeated successful
// applies, and signals outside AWAITING_CONFIGURATION are intentionally dropped.
func (c *Coordinator) SignalConfigurationRetry() {
	c.mu.RLock()
	waiting := c.state == StateAwaitingConfiguration
	c.mu.RUnlock()
	if !waiting {
		return
	}
	select {
	case c.configurationRetry <- struct{}{}:
	default:
	}
}

func (c *Coordinator) ConfigurationRetries() <-chan struct{} { return c.configurationRetry }

// Transition moves the machine to a new state, validating against the allowed
// edges. It returns ErrInvalidTransition (wrapped) when the move is not
// permitted. Subscribers are notified after the lock is released.
//
// Reaching StateFailed through Transition is permitted but does not attach a
// structured cause; prefer Fail for failures.
func (c *Coordinator) Transition(to State, reason string) error {
	c.mu.Lock()
	from := c.state
	if !CanTransition(from, to) {
		c.mu.Unlock()
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	event := c.applyLocked(from, to, reason)
	c.mu.Unlock()
	c.publish(event)
	return nil
}

// Fail moves the machine to FAILED with a structured cause. The first cause is
// preserved; if the machine is already FAILED, the new cause is appended to
// SecondaryErrors and no state change event is emitted. Fail returns
// ErrInvalidTransition only when FAILED is unreachable from the current state
// (e.g. during SHUTTING_DOWN).
func (c *Coordinator) Fail(code, message string) error {
	c.mu.Lock()
	from := c.state
	if from == StateFailed {
		c.appendSecondaryLocked(code, message)
		c.mu.Unlock()
		return nil
	}
	if !CanTransition(from, StateFailed) {
		c.mu.Unlock()
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, StateFailed)
	}
	c.fatal = &FatalError{Code: code, Message: message, OccurredAt: time.Now().UTC()}
	event := c.applyLocked(from, StateFailed, message)
	c.mu.Unlock()
	c.publish(event)
	return nil
}

// Shutdown moves the machine to SHUTTING_DOWN from any non-terminal state. It is
// a no-op returning nil when already shutting down.
func (c *Coordinator) Shutdown(reason string) error {
	c.mu.Lock()
	from := c.state
	if from == StateShuttingDown {
		c.mu.Unlock()
		return nil
	}
	if !CanTransition(from, StateShuttingDown) {
		c.mu.Unlock()
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, StateShuttingDown)
	}
	event := c.applyLocked(from, StateShuttingDown, reason)
	c.mu.Unlock()
	c.publish(event)
	return nil
}

// applyLocked records the transition and returns the event to publish. The
// caller must hold the write lock.
func (c *Coordinator) applyLocked(from, to State, reason string) StateChangeEvent {
	now := time.Now().UTC()
	c.state = to
	c.lastTransition = now
	c.history = append(c.history, TransitionRecord{From: from, To: to, At: now, Reason: reason})
	if len(c.history) > maxHistory {
		c.history = c.history[len(c.history)-maxHistory:]
	}
	return StateChangeEvent{From: from, To: to, At: now, Snapshot: c.snapshotLocked()}
}

// appendSecondaryLocked accumulates an additional fatal cause while preserving
// the first one. The caller must hold the write lock.
func (c *Coordinator) appendSecondaryLocked(code, message string) {
	cause := message
	if code != "" {
		cause = code + ": " + message
	}
	if c.fatal == nil {
		c.fatal = &FatalError{Code: code, Message: message, OccurredAt: time.Now().UTC()}
		return
	}
	c.fatal.SecondaryErrors = append(c.fatal.SecondaryErrors, cause)
}

// SetCapability records the status of one mandatory capability.
func (c *Coordinator) SetCapability(name string, available bool, errMessage string) {
	c.mu.Lock()
	c.capabilities[name] = CapabilityStatus{
		Name:      name,
		Available: available,
		CheckedAt: time.Now().UTC(),
		Error:     errMessage,
	}
	c.mu.Unlock()
}

// SetStep updates the human-readable description of the current step.
func (c *Coordinator) SetStep(step string) {
	c.mu.Lock()
	c.currentStep = step
	c.mu.Unlock()
}

// SetIndexRevision records the identifier/revision of the active index.
func (c *Coordinator) SetIndexRevision(revision string) {
	c.mu.Lock()
	c.indexRevision = revision
	c.mu.Unlock()
}

// CurrentState returns the current state without copying the rest of the snapshot.
func (c *Coordinator) CurrentState() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// Snapshot returns an immutable, deep-copied view of the coordinator state.
func (c *Coordinator) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshotLocked()
}

// snapshotLocked builds a deep copy of the current state. The caller must hold
// at least a read lock.
func (c *Coordinator) snapshotLocked() Snapshot {
	capabilities := make([]CapabilityStatus, 0, len(c.capabilities))
	for _, capability := range c.capabilities {
		capabilities = append(capabilities, capability)
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })

	history := append([]TransitionRecord(nil), c.history...)

	return Snapshot{
		State:             c.state,
		LastTransition:    c.lastTransition,
		CurrentStep:       c.currentStep,
		FatalError:        c.fatal.clone(),
		Capabilities:      capabilities,
		IndexRevision:     c.indexRevision,
		TransitionHistory: history,
	}
}

// Subscribe registers a non-blocking subscriber. It returns a buffered receive
// channel and a function that unsubscribes and closes the channel. The
// unsubscribe function is idempotent.
func (c *Coordinator) Subscribe() (<-chan StateChangeEvent, func()) {
	channel := make(chan StateChangeEvent, subscriberBuffer)
	c.mu.Lock()
	c.subscribers[channel] = struct{}{}
	c.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			c.mu.Lock()
			if _, exists := c.subscribers[channel]; exists {
				delete(c.subscribers, channel)
				close(channel)
			}
			c.mu.Unlock()
		})
	}
	return channel, cancel
}

// publish delivers an event to every subscriber without blocking. The write
// lock must be released before calling this. A full subscriber buffer drops the
// event rather than stalling the transition.
func (c *Coordinator) publish(event StateChangeEvent) {
	c.mu.RLock()
	observer := c.onEventDropped
	dropped := 0
	for subscriber := range c.subscribers {
		select {
		case subscriber <- event:
		default:
			dropped++
		}
	}
	c.mu.RUnlock()
	if observer != nil {
		for range dropped {
			observer()
		}
	}
}
