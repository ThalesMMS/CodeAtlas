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

func TestFileStoreMissingFileReturnsEmptyVersionOneDocument(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "CodeAtlas", "settings.json"))
	document, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != SettingsSchemaVersion || document.Revision != 0 || len(document.Overrides) != 0 {
		t.Fatalf("document = %#v", document)
	}
}

func TestFileStoreRoundTripsVersionOneWithoutSecretMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CodeAtlas", "settings.json")
	store := NewFileStore(path)
	want := Document{
		SchemaVersion: SettingsSchemaVersion,
		Revision:      7,
		Overrides: Overrides{
			FieldLLMBaseURL:        "https://provider.example/v1",
			FieldMaxFileBytes:      int64(2500000),
			FieldEnableEmbeddings:  true,
			FieldTypeScriptSDKPath: "",
		},
		Credentials: CredentialReferences{LLMAPIKeyGeneration: "generation-a", EmbeddingsAPIKeyGeneration: "generation-b"},
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
	for _, forbidden := range []string{"chat-secret", "embedding-secret", "apiKey"} {
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

func TestFileStoreRejectsUnknownFieldsAndFutureSchemaWithoutRewrite(t *testing.T) {
	for _, test := range []struct {
		name string
		json string
	}{
		{name: "unknown root", json: `{"schemaVersion":1,"revision":1,"overrides":{},"credentials":{},"future":true}`},
		{name: "unknown override", json: `{"schemaVersion":1,"revision":1,"overrides":{"CODEATLAS_FUTURE":"x"},"credentials":{}}`},
		{name: "secret in overrides", json: `{"schemaVersion":1,"revision":1,"overrides":{"CODEATLAS_LLM_API_KEY":"secret"},"credentials":{}}`},
		{name: "future schema", json: `{"schemaVersion":2,"revision":1,"overrides":{},"credentials":{}}`},
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
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatalf("failed load rewrote file: before=%q after=%q", before, after)
			}
		})
	}
}

func TestFileStoreFailedRenamePreservesPreviousDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store := NewFileStore(path)
	old := Document{SchemaVersion: 1, Revision: 1, Overrides: Overrides{FieldLLMModel: "old"}}
	if err := store.Save(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	store.rename = func(_, _ string) error { return errors.New("rename unavailable") }
	if err := store.Save(context.Background(), Document{SchemaVersion: 1, Revision: 2, Overrides: Overrides{FieldLLMModel: "new"}}); err == nil {
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
