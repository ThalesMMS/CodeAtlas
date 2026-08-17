package capabilities

import "testing"

func TestRegistryImplementsReceiver(t *testing.T) {
	t.Parallel()
	var _ ResultReceiver = NewRegistry()
}

func TestRegistryUpdateAndDeterministicOrder(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	registry.UpdateCapability(Result{ID: CapabilityWorkspace, State: CapabilityAvailable})
	registry.UpdateCapability(Result{ID: CapabilityStore, State: CapabilityUnavailable})
	registry.UpdateCapability(Result{ID: CapabilityFrontendAssets, State: CapabilityAvailable})
	// Replacing a capability keeps only the latest result.
	registry.UpdateCapability(Result{ID: CapabilityStore, State: CapabilityAvailable})

	results := registry.Results()
	want := []CapabilityID{CapabilityFrontendAssets, CapabilityStore, CapabilityWorkspace}
	if len(results) != len(want) {
		t.Fatalf("got %d results, want %d", len(results), len(want))
	}
	for i, id := range want {
		if results[i].ID != id {
			t.Fatalf("results order = %v, want %v", idsOf(results), want)
		}
	}
	if results[1].State != CapabilityAvailable {
		t.Fatalf("store state = %s, want latest available", results[1].State)
	}
}

func TestRegistryResultsAreCopies(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	registry.UpdateCapability(Result{ID: CapabilityWorkspace, State: CapabilityAvailable, Metadata: map[string]string{"symlinked": "false"}})

	results := registry.Results()
	results[0].Metadata["symlinked"] = "true"
	results[0].State = CapabilityUnavailable

	fresh := registry.Results()
	if fresh[0].Metadata["symlinked"] != "false" || fresh[0].State != CapabilityAvailable {
		t.Fatalf("registry mutated through returned copy: %#v", fresh[0])
	}
}

func idsOf(results []Result) []CapabilityID {
	ids := make([]CapabilityID, len(results))
	for i, result := range results {
		ids[i] = result.ID
	}
	return ids
}
