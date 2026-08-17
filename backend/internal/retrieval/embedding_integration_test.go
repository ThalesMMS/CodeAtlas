package retrieval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
)

// TestReconcileWithRealProviderOverHTTP wires the real OpenAI-compatible provider
// (not the fake) through Reconcile against a localhost embeddings server, so the
// provider's request/response JSON contract is exercised end to end alongside the
// rebuild pipeline and a follow-up hybrid search.
func TestReconcileWithRealProviderOverHTTP(t *testing.T) {
	t.Parallel()
	const dimension = 8
	var embedCalls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&embedCalls, 1)
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type item struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		data := make([]item, len(req.Input))
		for i := range req.Input {
			vec := make([]float64, dimension)
			for j := range vec {
				vec[j] = float64((i+j)%5) * 0.25
			}
			data[i] = item{Object: "embedding", Index: i, Embedding: vec}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "model": req.Model, "data": data})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	provider := ai.NewOpenAICompatible(ai.Options{
		BaseURL:          server.URL + "/v1",
		Model:            "default",
		EmbeddingModel:   "embed",
		EnableEmbeddings: true,
	})
	repository := reconcileStore(t)
	retriever := NewHybrid(repository, provider, true)

	if err := retriever.Reconcile(context.Background(), "openai-compatible", "embed"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got, want := repository.EmbeddingCount(), nonFileSymbolCount(repository); got != want {
		t.Fatalf("EmbeddingCount = %d, want %d", got, want)
	}
	md := repository.EmbeddingMetadata()
	if md.Dimension != dimension || md.Model != "embed" || md.TemplateVersion != EmbeddingTemplateVersion {
		t.Fatalf("metadata = %+v, want dim %d / model embed / template %s", md, dimension, EmbeddingTemplateVersion)
	}
	if calls := atomic.LoadInt32(&embedCalls); calls < 2 {
		t.Fatalf("embed calls = %d, want >=2 (dimension probe + rebuild)", calls)
	}

	// Dense retrieval now works end to end against the real provider HTTP path.
	hits, err := retriever.Search(context.Background(), "submit order", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("hybrid search returned no hits after a successful reconcile")
	}
}
