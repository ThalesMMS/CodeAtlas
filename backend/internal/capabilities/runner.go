package capabilities

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Default runner bounds. A zero value on the Runner falls back to these.
const (
	defaultGlobalTimeout   = 30 * time.Second
	defaultPerProbeTimeout = 10 * time.Second
	defaultMaxConcurrency  = 4
)

// Runner executes probes concurrently with bounded parallelism, per-probe and
// global timeouts, and panic isolation. Results are returned in a deterministic
// order regardless of completion order. The runner holds no mutex while probes
// run I/O or while invoking the optional ResultReceiver.
type Runner struct {
	GlobalTimeout   time.Duration
	PerProbeTimeout time.Duration
	MaxConcurrency  int
}

func (r Runner) globalTimeout() time.Duration {
	if r.GlobalTimeout > 0 {
		return r.GlobalTimeout
	}
	return defaultGlobalTimeout
}

func (r Runner) perProbeTimeout() time.Duration {
	if r.PerProbeTimeout > 0 {
		return r.PerProbeTimeout
	}
	return defaultPerProbeTimeout
}

func (r Runner) maxConcurrency() int {
	if r.MaxConcurrency > 0 {
		return r.MaxConcurrency
	}
	return defaultMaxConcurrency
}

// Run executes every probe and returns the results sorted by CapabilityID. When
// receiver is non-nil it is notified once per result as soon as the result is
// available. A panicking, hanging or cancelled probe yields a structured
// unavailable result instead of crashing the process.
func (r Runner) Run(ctx context.Context, probes []Probe, receiver ResultReceiver) []Result {
	if len(probes) == 0 {
		return nil
	}

	runCtx, cancel := context.WithTimeout(ctx, r.globalTimeout())
	defer cancel()

	semaphore := make(chan struct{}, r.maxConcurrency())
	results := make(chan Result, len(probes))
	var waitGroup sync.WaitGroup

	for _, probe := range probes {
		waitGroup.Add(1)
		go func(probe Probe) {
			defer waitGroup.Done()
			start := time.Now()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-runCtx.Done():
				results <- contextResult(probe, start, runCtx.Err())
				return
			}
			result := runProbe(runCtx, probe, r.perProbeTimeout())
			if receiver != nil {
				receiver.UpdateCapability(result)
			}
			results <- result
		}(probe)
	}

	go func() {
		waitGroup.Wait()
		close(results)
	}()

	collected := make([]Result, 0, len(probes))
	for result := range results {
		collected = append(collected, result)
	}
	sort.Slice(collected, func(i, j int) bool { return collected[i].ID < collected[j].ID })
	return collected
}

// runProbe runs a single probe under a per-probe deadline, recovering panics and
// enforcing the timeout even if the probe ignores its context.
func runProbe(parent context.Context, probe Probe, perProbe time.Duration) Result {
	start := time.Now()
	probeCtx, cancel := context.WithTimeout(parent, perProbe)
	defer cancel()

	done := make(chan Result, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- Result{
					ID:          probe.ID(),
					Requirement: probe.Requirement(),
					State:       CapabilityUnavailable,
					CheckedAt:   time.Now().UTC(),
					Duration:    time.Since(start),
					ErrorCode:   ErrCodePanic,
					Message:     sanitizeMessage(fmt.Sprintf("probe panicked: %v", recovered)),
				}
			}
		}()
		done <- probe.Run(probeCtx)
	}()

	select {
	case result := <-done:
		return result
	case <-probeCtx.Done():
		return contextResult(probe, start, probeCtx.Err())
	}
}

// contextResult maps a context error to a structured unavailable result.
func contextResult(probe Probe, start time.Time, err error) Result {
	code := ErrCodeCancelled
	message := "probe cancelled"
	if errors.Is(err, context.DeadlineExceeded) {
		code = ErrCodeTimeout
		message = "probe timed out"
	}
	return Result{
		ID:          probe.ID(),
		Requirement: probe.Requirement(),
		State:       CapabilityUnavailable,
		CheckedAt:   time.Now().UTC(),
		Duration:    time.Since(start),
		ErrorCode:   code,
		Message:     message,
	}
}
