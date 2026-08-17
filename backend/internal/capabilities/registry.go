package capabilities

import (
	"sort"
	"sync"
)

// Registry is a thread-safe store of the latest probe Result per capability. It
// implements ResultReceiver so a Runner can push results into it, and exposes a
// deterministic, copied snapshot of those results for diagnostics (e.g. the
// /api/capabilities endpoint). It keeps only the most recent result per ID.
type Registry struct {
	mu      sync.RWMutex
	results map[CapabilityID]Result
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{results: make(map[CapabilityID]Result)}
}

// UpdateCapability records (or replaces) the latest result for a capability.
func (r *Registry) UpdateCapability(result Result) {
	r.mu.Lock()
	r.results[result.ID] = cloneResult(result)
	r.mu.Unlock()
}

// Results returns every stored result sorted by CapabilityID. Each result is a
// deep copy, so callers cannot mutate the registry through the returned slice.
func (r *Registry) Results() []Result {
	r.mu.RLock()
	defer r.mu.RUnlock()
	results := make([]Result, 0, len(r.results))
	for _, result := range r.results {
		results = append(results, cloneResult(result))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	return results
}

// cloneResult copies a Result including its Metadata map so the registry never
// shares mutable state with producers or consumers.
func cloneResult(result Result) Result {
	if result.Metadata != nil {
		metadata := make(map[string]string, len(result.Metadata))
		for key, value := range result.Metadata {
			metadata[key] = value
		}
		result.Metadata = metadata
	}
	return result
}
