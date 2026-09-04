package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SettingsSchemaVersion is the version this build writes. Version 2 (#163)
// retired dense retrieval's four fields and its credential reference; a
// version-1 document is upgraded on load by dropping keys the catalog no longer
// documents, so an existing install never fails to start on a field this build
// stopped knowing about.
const SettingsSchemaVersion = 2

// oldestUpgradableSchemaVersion is the oldest persisted document this build can
// read. Anything older is rejected rather than silently reinterpreted.
const oldestUpgradableSchemaVersion = 1

type CredentialReferences struct {
	LLMAPIKeyGeneration string `json:"llmApiKeyGeneration,omitempty"`
}

type Document struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Revision      uint64               `json:"revision"`
	Overrides     Overrides            `json:"overrides"`
	Credentials   CredentialReferences `json:"credentials"`
}

type FileStore struct {
	path   string
	rename func(string, string) error
}

func NewFileStore(path string) *FileStore {
	return &FileStore{path: path, rename: os.Rename}
}

func DefaultPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(root, "CodeAtlas", "settings.json"), nil
}

func (s *FileStore) Load(ctx context.Context) (Document, error) {
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Document{SchemaVersion: SettingsSchemaVersion, Overrides: Overrides{}}, nil
	}
	if err != nil {
		return Document{}, fmt.Errorf("read settings: %w", err)
	}
	version, err := peekSchemaVersion(data)
	if err != nil {
		return Document{}, err
	}
	switch {
	case version == SettingsSchemaVersion:
		return decodeCurrentDocument(data)
	case version >= oldestUpgradableSchemaVersion && version < SettingsSchemaVersion:
		return upgradeDocument(data)
	default:
		return Document{}, fmt.Errorf("unsupported settings schema version %d", version)
	}
}

// peekSchemaVersion reads only schemaVersion so the decoder strictness applied
// afterwards can depend on which version wrote the file.
func peekSchemaVersion(data []byte) (int, error) {
	var header struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return 0, fmt.Errorf("decode settings: %w", err)
	}
	return header.SchemaVersion, nil
}

// decodeCurrentDocument is the strict path for a document this build wrote:
// an unknown key is a bug or hand-editing mistake, not a version difference.
func decodeCurrentDocument(data []byte) (Document, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("decode settings: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Document{}, err
	}
	if document.Overrides == nil {
		document.Overrides = Overrides{}
	}
	return document, nil
}

// upgradeDocument reads a document written by an older build. Overrides and
// credential references the current catalog no longer documents are dropped
// rather than rejected, and the result is stamped with the current version so
// the next Save persists the upgraded shape. A retired key is never re-applied.
func upgradeDocument(data []byte) (Document, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var legacy struct {
		Revision    uint64                     `json:"revision"`
		Overrides   map[string]json.RawMessage `json:"overrides"`
		Credentials CredentialReferences       `json:"credentials"`
	}
	if err := decoder.Decode(&legacy); err != nil {
		return Document{}, fmt.Errorf("decode settings: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Document{}, err
	}
	document := Document{
		SchemaVersion: SettingsSchemaVersion,
		Revision:      legacy.Revision,
		Overrides:     make(Overrides, len(legacy.Overrides)),
		Credentials:   legacy.Credentials,
	}
	for rawKey, raw := range legacy.Overrides {
		key := FieldKey(rawKey)
		definition, ok := Definition(key)
		if !ok || definition.Secret {
			continue
		}
		value, err := decodeOverrideValue(definition, raw)
		if err != nil {
			return Document{}, fmt.Errorf("decode %s: %w", rawKey, err)
		}
		document.Overrides[key] = value
	}
	return document, nil
}

func (s *FileStore) Save(ctx context.Context, document Document) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if document.SchemaVersion != SettingsSchemaVersion {
		return fmt.Errorf("unsupported settings schema version %d", document.SchemaVersion)
	}
	if document.Overrides == nil {
		document.Overrides = Overrides{}
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary settings: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close settings: %w", err)
	}
	if err := s.rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	if handle, err := os.Open(directory); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing settings data: %w", err)
	}
	return errors.New("settings file contains more than one JSON document")
}

func (o Overrides) MarshalJSON() ([]byte, error) {
	object := make(map[string]any, len(o))
	for key, value := range o {
		definition, ok := Definition(key)
		if !ok {
			return nil, fmt.Errorf("unknown settings field %s", key)
		}
		if definition.Secret {
			return nil, fmt.Errorf("secret field %s cannot be persisted as an override", key)
		}
		if err := validateOverrideType(definition, value); err != nil {
			return nil, err
		}
		object[string(key)] = value
	}
	return json.Marshal(object)
}

func (o *Overrides) UnmarshalJSON(data []byte) error {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&object); err != nil {
		return err
	}
	result := make(Overrides, len(object))
	for rawKey, raw := range object {
		key := FieldKey(rawKey)
		definition, ok := Definition(key)
		if !ok {
			return fmt.Errorf("unknown settings field %s", rawKey)
		}
		if definition.Secret {
			return fmt.Errorf("secret field %s cannot be persisted as an override", rawKey)
		}
		value, err := decodeOverrideValue(definition, raw)
		if err != nil {
			return fmt.Errorf("decode %s: %w", rawKey, err)
		}
		result[key] = value
	}
	*o = result
	return nil
}

func decodeOverrideValue(definition FieldDefinition, raw json.RawMessage) (any, error) {
	switch definition.Kind {
	case KindInteger:
		var value int64
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, errors.New("value must be an integer")
		}
		return value, nil
	case KindBoolean:
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, errors.New("value must be true or false")
		}
		return value, nil
	default:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, errors.New("value must be a string")
		}
		return value, nil
	}
}

func validateOverrideType(definition FieldDefinition, value any) error {
	switch definition.Kind {
	case KindInteger:
		switch value.(type) {
		case int, int64:
			return nil
		}
	case KindBoolean:
		if _, ok := value.(bool); ok {
			return nil
		}
	default:
		if _, ok := value.(string); ok {
			return nil
		}
	}
	return fmt.Errorf("invalid value type for %s", definition.Key)
}
