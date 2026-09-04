package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestFileStoreMissingFileReturnsEmptyCurrentVersionDocument(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "CodeAtlas", "settings.json"))
	document, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != SettingsSchemaVersion || document.Revision != 0 || len(document.Overrides) != 0 {
		t.Fatalf("document = %#v", document)
	}
}

func TestFileStoreRoundTripsCurrentVersionWithoutSecretMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CodeAtlas", "settings.json")
	store := NewFileStore(path)
	want := Document{
		SchemaVersion: SettingsSchemaVersion,
		Revision:      7,
		Overrides: Overrides{
			FieldLLMBaseURL:        "https://provider.example/v1",
			FieldMaxFileBytes:      int64(2500000),
			FieldLLMModel:          "default",
			FieldTypeScriptSDKPath: "",
		},
		Credentials: CredentialReferences{LLMAPIKeyGeneration: "generation-a"},
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"chat-secret", "apiKey"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("settings file contains forbidden secret marker %q: %s", forbidden, data)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o, want 600", info.Mode().Perm())
		}
	}
}

// A document this build wrote is decoded strictly: an unknown key there is a
// bug or a hand-editing mistake, never a version difference, so the leniency
// added for the schema-1 upgrade must not reach the current-version path.
func TestFileStoreRejectsUnknownFieldsAndFutureSchemaWithoutRewrite(t *testing.T) {
	// wantError pins the reason each case is rejected. Without it a case could
	// pass on an incidental error and stop testing strictness altogether -- the
	// exact way the schema-1 retarget could have gone quietly wrong.
	for _, test := range []struct {
		name      string
		json      string
		wantError string
	}{
		{name: "unknown root", json: `{"schemaVersion":2,"revision":1,"overrides":{},"credentials":{},"future":true}`,
			wantError: `unknown field "future"`},
		{name: "unknown override", json: `{"schemaVersion":2,"revision":1,"overrides":{"CODEATLAS_FUTURE":"x"},"credentials":{}}`,
			wantError: "unknown settings field CODEATLAS_FUTURE"},
		{name: "retired override", json: `{"schemaVersion":2,"revision":1,"overrides":{"CODEATLAS_ENABLE_EMBEDDINGS":true},"credentials":{}}`,
			wantError: "unknown settings field CODEATLAS_ENABLE_EMBEDDINGS"},
		{name: "unknown credential", json: `{"schemaVersion":2,"revision":1,"overrides":{},"credentials":{"embeddingsApiKeyGeneration":"generation-b"}}`,
			wantError: `unknown field "embeddingsApiKeyGeneration"`},
		{name: "secret in overrides", json: `{"schemaVersion":2,"revision":1,"overrides":{"CODEATLAS_LLM_API_KEY":"secret"},"credentials":{}}`,
			wantError: "secret field CODEATLAS_LLM_API_KEY cannot be persisted as an override"},
		{name: "future schema", json: `{"schemaVersion":3,"revision":1,"overrides":{},"credentials":{}}`,
			wantError: "unsupported settings schema version 3"},
		{name: "schema below the upgrade floor", json: `{"schemaVersion":0,"revision":1,"overrides":{},"credentials":{}}`,
			wantError: "unsupported settings schema version 0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(test.json), 0o600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			_, err := NewFileStore(path).Load(context.Background())
			if err == nil {
				t.Fatal("Load succeeded, want strict schema error")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want it to mention %q", err, test.wantError)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatalf("failed load rewrote file: before=%q after=%q", before, after)
			}
		})
	}
}

// The upgrade path is what stops an existing install from failing to start on
// a field this build stopped documenting (#163). A schema-1 document still
// carries the four retired dense-retrieval overrides and the retired
// embeddings credential reference; Load must drop exactly those, keep
// everything else, and report the current schema version.
func TestFileStoreUpgradesSchemaOneByDroppingRetiredKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy, err := os.ReadFile(filepath.Join("testdata", "schema-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(path)

	document, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() of a schema-1 document failed: %v", err)
	}
	want := Document{
		SchemaVersion: SettingsSchemaVersion,
		Revision:      12,
		Overrides: Overrides{
			FieldLLMBaseURL: "http://127.0.0.1:8000/v1",
			FieldLLMModel:   "default",
		},
		Credentials: CredentialReferences{LLMAPIKeyGeneration: "generation-a"},
	}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("upgraded document = %#v, want %#v", document, want)
	}

	// Loading must not rewrite the file on its own; the upgraded shape is
	// persisted by the next Save, and that result must survive a strict reload.
	if unchanged, _ := os.ReadFile(path); string(unchanged) != string(legacy) {
		t.Fatalf("Load rewrote the legacy file: %s", unchanged)
	}
	if err := store.Save(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"CODEATLAS_ENABLE_EMBEDDINGS", "CODEATLAS_EMBEDDING_MODEL", "embeddingsApiKeyGeneration"} {
		if strings.Contains(string(persisted), retired) {
			t.Fatalf("upgraded file still carries retired key %q: %s", retired, persisted)
		}
	}
	reloaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("strict reload of the upgraded document failed: %v", err)
	}
	if !reflect.DeepEqual(reloaded, want) {
		t.Fatalf("reloaded document = %#v, want %#v", reloaded, want)
	}
}

func TestFileStoreUpgradeDropsRetiredSecretOverrideWithoutAdoptingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := `{"schemaVersion":1,"revision":4,"overrides":{"CODEATLAS_LLM_API_KEY":"leaked-secret","CODEATLAS_LLM_MODEL":"default"},"credentials":{}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := NewFileStore(path).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := document.Overrides[FieldLLMAPIKey]; ok {
		t.Fatalf("upgrade adopted a secret as a plaintext override: %#v", document.Overrides)
	}
	if document.Overrides[FieldLLMModel] != "default" || len(document.Overrides) != 1 {
		t.Fatalf("upgraded overrides = %#v", document.Overrides)
	}
}

func TestFileStoreFailedRenamePreservesPreviousDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store := NewFileStore(path)
	old := Document{SchemaVersion: SettingsSchemaVersion, Revision: 1, Overrides: Overrides{FieldLLMModel: "old"}}
	if err := store.Save(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	store.rename = func(_, _ string) error { return errors.New("rename unavailable") }
	if err := store.Save(context.Background(), Document{SchemaVersion: SettingsSchemaVersion, Revision: 2, Overrides: Overrides{FieldLLMModel: "new"}}); err == nil {
		t.Fatal("Save succeeded despite rename failure")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("target changed after failed rename: before=%q after=%q", before, after)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".settings-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %#v, err=%v", matches, err)
	}
}

func TestDefaultSettingsPathUsesCodeAtlasDirectory(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := filepath.Join("CodeAtlas", "settings.json")
	if !strings.HasSuffix(path, wantSuffix) || !filepath.IsAbs(path) {
		t.Fatalf("DefaultPath() = %q, want absolute *%q", path, wantSuffix)
	}
}
