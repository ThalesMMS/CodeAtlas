package retrieval

import (
	"context"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestEmbeddingRuntimePreparesFingerprintAndCompatibleState(t *testing.T) {
	provider := &reconcileProvider{available: true, dimension: 4}
	runtime := NewEmbeddingRuntime(provider, false)
	if runtime.Snapshot().State != EmbeddingDisabled {
		t.Fatalf("initial state = %q", runtime.Snapshot().State)
	}
	configuration := EmbeddingConfiguration{
		Provider: provider, Enabled: true, Model: "embed-v1",
		BaseURL: "HTTPS://User:password@Example.COM/v1/", CredentialGeneration: "generation-a",
	}

	prepared, err := runtime.Prepare(context.Background(), configuration, domain.EmbeddingIndexMetadata{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.NeedsRebuild() || prepared.Fingerprint().Dimension != 4 {
		t.Fatalf("prepared = %#v", prepared)
	}
	fingerprint := prepared.Fingerprint()
	if strings.Contains(fingerprint.Provider, "example") || strings.Contains(fingerprint.Provider, "password") || strings.Contains(fingerprint.ConfigurationHash, "example") {
		t.Fatalf("fingerprint exposed endpoint details: %#v", fingerprint)
	}
	prepared.Activate()
	if runtime.Snapshot().State != EmbeddingRebuilding {
		t.Fatalf("activated state = %q", runtime.Snapshot().State)
	}
	if !runtime.MarkAvailable(fingerprint) || runtime.Snapshot().State != EmbeddingAvailable {
		t.Fatal("current fingerprint did not become available")
	}

	metadata := fingerprint.Metadata()
	compatible, err := runtime.Prepare(context.Background(), configuration, metadata, 10)
	if err != nil {
		t.Fatal(err)
	}
	if compatible.NeedsRebuild() || compatible.State() != EmbeddingAvailable {
		t.Fatalf("compatible candidate = %#v", compatible)
	}
}

func TestEmbeddingRuntimeEndpointModelAndCredentialChangesInvalidateOldJobs(t *testing.T) {
	provider := &reconcileProvider{available: true, dimension: 3}
	runtime := NewEmbeddingRuntime(provider, false)
	prepare := func(model, endpoint, generation string) *PreparedEmbedding {
		t.Helper()
		candidate, err := runtime.Prepare(context.Background(), EmbeddingConfiguration{
			Provider: provider, Enabled: true, Model: model, BaseURL: endpoint, CredentialGeneration: generation,
		}, domain.EmbeddingIndexMetadata{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		candidate.Activate()
		return candidate
	}

	first := prepare("model-a", "https://one.example/v1", "generation-a")
	second := prepare("model-a", "https://two.example/v1", "generation-a")
	third := prepare("model-a", "https://two.example/v1", "generation-b")
	fourth := prepare("model-b", "https://two.example/v1", "generation-b")
	if first.Fingerprint() == second.Fingerprint() || second.Fingerprint() == third.Fingerprint() || third.Fingerprint() == fourth.Fingerprint() {
		t.Fatal("an embedding-affecting change preserved the fingerprint")
	}
	if runtime.MarkAvailable(first.Fingerprint()) {
		t.Fatal("superseded rebuild reactivated an old fingerprint")
	}
	if runtime.Snapshot().State != EmbeddingRebuilding || runtime.Snapshot().Fingerprint != fourth.Fingerprint() {
		t.Fatalf("stale mark changed current snapshot: %#v", runtime.Snapshot())
	}
	if !runtime.MarkFailed(fourth.Fingerprint()) || runtime.Snapshot().State != EmbeddingUnavailable {
		t.Fatal("current failed rebuild did not become unavailable")
	}
}

func TestEmbeddingRuntimeDisabledPreparationDoesNotProbe(t *testing.T) {
	provider := &reconcileProvider{available: true, dimension: 4}
	runtime := NewEmbeddingRuntime(provider, true)
	prepared, err := runtime.Prepare(context.Background(), EmbeddingConfiguration{Provider: provider, Enabled: false}, domain.EmbeddingIndexMetadata{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Activate()
	if provider.calls() != 0 || runtime.Snapshot().State != EmbeddingDisabled {
		t.Fatalf("disabled prepare calls/state = %d/%q", provider.calls(), runtime.Snapshot().State)
	}
}
