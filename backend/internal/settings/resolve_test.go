package settings

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDocumentedFieldCatalogMatchesEnvExample(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "..", ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var fromEnvExample []FieldKey
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "CODEATLAS_") || !strings.Contains(line, "=") {
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		fromEnvExample = append(fromEnvExample, FieldKey(key))
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	definitions := DocumentedFields()
	fromCatalog := make([]FieldKey, 0, len(definitions))
	for _, definition := range definitions {
		fromCatalog = append(fromCatalog, definition.Key)
	}
	if !reflect.DeepEqual(fromCatalog, fromEnvExample) {
		t.Fatalf("catalog = %#v, want .env.example order %#v", fromCatalog, fromEnvExample)
	}
	if len(fromCatalog) != 19 {
		t.Fatalf("catalog has %d fields, want 19", len(fromCatalog))
	}
}

func TestResolveAppliesDefaultEnvironmentAndSettingsPrecedence(t *testing.T) {
	environment := Environment{
		FieldWorkspace:    `C:\env-workspace`,
		FieldMaxFileBytes: "2000",
		FieldLLMTimeout:   "45s",
		FieldLLMModel:     "env-model",
		FieldLLMAPIKey:    "env-chat-secret",
	}
	overrides := Overrides{
		FieldWorkspace:    `C:\settings-workspace`,
		FieldMaxFileBytes: int64(3000),
		FieldLLMTimeout:   "90s",
	}
	resolved := Resolve(environment, overrides, SecretValues{FieldLLMAPIKey: "vault-chat-secret"})

	if resolved.Values.Workspace != `C:\settings-workspace` || resolved.Source(FieldWorkspace) != SourceSettings {
		t.Fatalf("workspace = %q/%q", resolved.Values.Workspace, resolved.Source(FieldWorkspace))
	}
	if resolved.Values.ListenAddress != DefaultListenAddress || resolved.Source(FieldListen) != SourceDefault {
		t.Fatalf("listen = %q/%q", resolved.Values.ListenAddress, resolved.Source(FieldListen))
	}
	if resolved.Values.MaxFileBytes != 3000 || resolved.Values.LLMTimeout != 90*time.Second {
		t.Fatalf("typed values = %#v", resolved.Values)
	}
	if resolved.Values.LLMModel != "env-model" || resolved.Source(FieldLLMModel) != SourceEnv {
		t.Fatalf("llm model = %q/%q", resolved.Values.LLMModel, resolved.Source(FieldLLMModel))
	}
	if resolved.Values.LLMAPIKey != "vault-chat-secret" || resolved.Source(FieldLLMAPIKey) != SourceSettings {
		t.Fatalf("secret source/value = %q/%q", resolved.Values.LLMAPIKey, resolved.Source(FieldLLMAPIKey))
	}
}

func TestResolvePreservesAllowedExplicitEmptyOverrides(t *testing.T) {
	resolved := Resolve(Environment{
		FieldLLMReasoningEffort: "high",
		FieldTypeScriptSDKPath:  `C:\env-sdk`,
	}, Overrides{
		FieldLLMReasoningEffort: "",
		FieldTypeScriptSDKPath:  "",
	}, nil)

	if resolved.Values.LLMReasoningEffort != "" || resolved.Source(FieldLLMReasoningEffort) != SourceSettings {
		t.Fatalf("reasoning effort = %q/%q", resolved.Values.LLMReasoningEffort, resolved.Source(FieldLLMReasoningEffort))
	}
	if resolved.HasError(FieldLLMReasoningEffort) {
		t.Fatalf("allowed empty override reported an error: %#v", resolved.Errors)
	}
	if resolved.Values.TypeScriptSDKPath != "" || resolved.Source(FieldTypeScriptSDKPath) != SourceSettings {
		t.Fatalf("typescript sdk = %q/%q", resolved.Values.TypeScriptSDKPath, resolved.Source(FieldTypeScriptSDKPath))
	}
}

func TestResolveReportsProviderErrorsWithoutBootstrapFailure(t *testing.T) {
	resolved := Resolve(Environment{
		FieldWorkspace:          ".",
		FieldLLMBaseURL:         "ftp://invalid.example",
		FieldLLMReasoningEffort: "extreme",
		FieldLLMTimeout:         "soon",
	}, nil, nil)

	for _, key := range []FieldKey{FieldLLMBaseURL, FieldLLMModel, FieldLLMReasoningEffort, FieldLLMTimeout} {
		if !resolved.HasError(key) {
			t.Fatalf("missing field error for %s: %#v", key, resolved.Errors)
		}
	}
	if err := resolved.BootstrapError(); err != nil {
		t.Fatalf("provider errors became bootstrap error: %v", err)
	}
}
