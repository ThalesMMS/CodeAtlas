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
	if len(fromCatalog) != 23 {
		t.Fatalf("catalog has %d fields, want 23", len(fromCatalog))
	}
}

func TestResolveAppliesDefaultEnvironmentAndSettingsPrecedence(t *testing.T) {
	environment := Environment{
		FieldWorkspace:        `C:\env-workspace`,
		FieldMaxFileBytes:     "2000",
		FieldLLMTimeout:       "45s",
		FieldEnableEmbeddings: "true",
		FieldLLMAPIKey:        "env-chat-secret",
	}
	overrides := Overrides{
		FieldWorkspace:        `C:\settings-workspace`,
		FieldMaxFileBytes:     int64(3000),
		FieldLLMTimeout:       "90s",
		FieldEnableEmbeddings: false,
	}
	resolved := Resolve(environment, overrides, SecretValues{FieldLLMAPIKey: "vault-chat-secret"})

	if resolved.Values.Workspace != `C:\settings-workspace` || resolved.Source(FieldWorkspace) != SourceSettings {
		t.Fatalf("workspace = %q/%q", resolved.Values.Workspace, resolved.Source(FieldWorkspace))
	}
	if resolved.Values.ListenAddress != "127.0.0.1:8080" || resolved.Source(FieldListen) != SourceDefault {
		t.Fatalf("listen = %q/%q", resolved.Values.ListenAddress, resolved.Source(FieldListen))
	}
	if resolved.Values.MaxFileBytes != 3000 || resolved.Values.LLMTimeout != 90*time.Second || resolved.Values.EnableEmbeddings {
		t.Fatalf("typed values = %#v", resolved.Values)
	}
	if resolved.Values.LLMAPIKey != "vault-chat-secret" || resolved.Source(FieldLLMAPIKey) != SourceSettings {
		t.Fatalf("secret source/value = %q/%q", resolved.Values.LLMAPIKey, resolved.Source(FieldLLMAPIKey))
	}
}

func TestResolvePreservesAllowedExplicitEmptyOverrides(t *testing.T) {
	resolved := Resolve(Environment{
		FieldEmbeddingBaseURL:  "https://env.example/v1",
		FieldTypeScriptSDKPath: `C:\env-sdk`,
	}, Overrides{
		FieldEmbeddingBaseURL:  "",
		FieldTypeScriptSDKPath: "",
	}, nil)

	if resolved.Values.EmbeddingBaseURL != "" || resolved.Source(FieldEmbeddingBaseURL) != SourceSettings {
		t.Fatalf("embedding base = %q/%q", resolved.Values.EmbeddingBaseURL, resolved.Source(FieldEmbeddingBaseURL))
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
