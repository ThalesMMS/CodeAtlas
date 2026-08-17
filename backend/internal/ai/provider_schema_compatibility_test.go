package ai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
)

func TestExplanationSchemaUsesVLLMSupportedKeywords(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if containsJSONKeyword(request, "uniqueItems") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Grammar error: Unimplemented keys: [\"uniqueItems\"]","type":"BadRequestError","code":400}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test-model","choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := ai.NewOpenAICompatible(ai.Options{BaseURL: server.URL, Model: "test-model"})
	_, err := ai.Generate(context.Background(), provider, ai.GenerationRequest{
		SystemPrompt: "Return JSON.",
		UserPrompt:   "Explain this symbol.",
		OutputSchema: aiout.ExplanationSchema(),
	})
	if err != nil {
		t.Fatalf("Generate with the runtime explanation schema: %v", err)
	}
}

func containsJSONKeyword(value any, keyword string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == keyword || containsJSONKeyword(child, keyword) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsJSONKeyword(child, keyword) {
				return true
			}
		}
	}
	return false
}
