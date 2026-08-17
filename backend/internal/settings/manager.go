package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
)

const (
	SettingsRevisionConflict = "SETTINGS_REVISION_CONFLICT"
	SettingsValidationFailed = "SETTINGS_VALIDATION_FAILED"
	SettingsVaultFailed      = "SETTINGS_VAULT_FAILED"
	SettingsPrepareFailed    = "SETTINGS_PREPARE_FAILED"
	SettingsSaveFailed       = "SETTINGS_SAVE_FAILED"
)

type DocumentStore interface {
	Load(context.Context) (Document, error)
	Save(context.Context, Document) error
}

type RuntimePreparer interface {
	Prepare(context.Context, Resolved, ChangeSet) (PreparedRuntime, error)
}

type PreparedRuntime interface {
	Activate() ActivationResult
	Abort(context.Context)
}

type ActivationResult struct {
	Applied        []Group `json:"applied,omitempty"`
	EmbeddingJobID string  `json:"embeddingJobId,omitempty"`
}

type ChangeSet struct {
	Fields  []FieldKey
	Live    []FieldKey
	Restart []FieldKey
}

func (c ChangeSet) Changed(key FieldKey) bool {
	for _, changed := range c.Fields {
		if changed == key {
			return true
		}
	}
	return false
}

type OverrideOperationName string

const (
	OverridePreserve OverrideOperationName = "preserve"
	OverrideReplace  OverrideOperationName = "replace"
	OverrideInherit  OverrideOperationName = "inherit"
)

type OverrideOperation struct {
	Operation OverrideOperationName `json:"operation"`
	Value     any                   `json:"value,omitempty"`
}

type UpdateRequest struct {
	Revision  uint64                         `json:"revision"`
	Overrides map[FieldKey]OverrideOperation `json:"overrides,omitempty"`
	Secrets   map[FieldKey]SecretOperation   `json:"secrets,omitempty"`
}

type UpdateResult struct {
	Snapshot        SanitizedSnapshot `json:"snapshot"`
	Applied         []Group           `json:"applied,omitempty"`
	RestartRequired []FieldKey        `json:"restartRequired"`
	EmbeddingJobID  string            `json:"embeddingJobId,omitempty"`
}

type ManagerError struct {
	Code     string            `json:"code"`
	Fields   []FieldError      `json:"fields,omitempty"`
	Snapshot SanitizedSnapshot `json:"snapshot,omitempty"`
}

func (e *ManagerError) Error() string { return e.Code }

type Manager struct {
	writes      sync.Mutex
	environment Environment
	store       DocumentStore
	credentials CredentialStore
	preparer    RuntimePreparer
	running     Values
	document    Document
	resolved    Resolved
	published   atomic.Pointer[SanitizedSnapshot]
}

func NewManager(ctx context.Context, environment Environment, store DocumentStore, credentials CredentialStore, preparer RuntimePreparer) (*Manager, error) {
	return newManager(ctx, environment, store, credentials, preparer, nil)
}

func NewManagerWithRunningValues(ctx context.Context, environment Environment, store DocumentStore, credentials CredentialStore, preparer RuntimePreparer, running Values) (*Manager, error) {
	return newManager(ctx, environment, store, credentials, preparer, &running)
}

func newManager(ctx context.Context, environment Environment, store DocumentStore, credentials CredentialStore, preparer RuntimePreparer, runningOverride *Values) (*Manager, error) {
	if store == nil {
		return nil, errors.New("settings document store is unavailable")
	}
	if credentials == nil {
		return nil, errors.New("credential store is unavailable")
	}
	if preparer == nil {
		return nil, errors.New("runtime preparer is unavailable")
	}
	document, resolved, err := LoadStartup(ctx, environment, store, credentials)
	if err != nil {
		return nil, err
	}
	running := resolved.Values
	if runningOverride != nil {
		running.Workspace = runningOverride.Workspace
		running.ListenAddress = runningOverride.ListenAddress
		running.MaxFileBytes = runningOverride.MaxFileBytes
	}
	manager := &Manager{
		environment: cloneEnvironment(environment),
		store:       store,
		credentials: credentials,
		preparer:    preparer,
		running:     running,
		document:    cloneDocument(document),
		resolved:    cloneResolved(resolved),
	}
	snapshot := makeSnapshot(document.Revision, resolved, manager.running)
	manager.published.Store(&snapshot)
	return manager, nil
}

func (m *Manager) Snapshot() SanitizedSnapshot {
	snapshot := m.published.Load()
	if snapshot == nil {
		return SanitizedSnapshot{Groups: map[Group][]FieldSnapshot{}}
	}
	return cloneSnapshot(*snapshot)
}

func (m *Manager) Update(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	m.writes.Lock()
	defer m.writes.Unlock()

	currentSnapshot := m.Snapshot()
	if request.Revision != m.document.Revision {
		return UpdateResult{Snapshot: currentSnapshot}, &ManagerError{
			Code: SettingsRevisionConflict, Snapshot: currentSnapshot,
		}
	}

	candidateOverrides, touched, fieldErrors := validateOverrideOperations(m.document.Overrides, request.Overrides)
	secretTouched, secretErrors := validateSecretOperations(request.Secrets)
	for key := range secretTouched {
		touched[key] = struct{}{}
	}
	fieldErrors = append(fieldErrors, secretErrors...)
	if len(fieldErrors) == 0 {
		localCandidate := Resolve(m.environment, candidateOverrides, nil)
		fieldErrors = append(fieldErrors, localCandidate.Errors...)
	}
	if len(fieldErrors) != 0 {
		return UpdateResult{Snapshot: currentSnapshot}, &ManagerError{
			Code: SettingsValidationFailed, Fields: fieldErrors, Snapshot: currentSnapshot,
		}
	}

	credentialTransaction, err := PrepareCredentialTransaction(
		ctx, m.credentials, m.document.Credentials, environmentSecrets(m.environment), request.Secrets,
	)
	if err != nil {
		return UpdateResult{Snapshot: currentSnapshot}, &ManagerError{Code: SettingsVaultFailed, Snapshot: currentSnapshot}
	}

	rollbackCredentials := true
	defer func() {
		if rollbackCredentials {
			_ = credentialTransaction.Rollback(context.Background())
		}
	}()
	candidateCredentials := savedCredentialValues(credentialTransaction.Values(), credentialTransaction.References())
	candidate := Resolve(m.environment, candidateOverrides, candidateCredentials)
	candidate.Credentials = credentialTransaction.References()
	if len(candidate.Errors) != 0 {
		return UpdateResult{Snapshot: currentSnapshot}, &ManagerError{
			Code: SettingsValidationFailed, Fields: candidate.Errors, Snapshot: currentSnapshot,
		}
	}
	changeSet := makeChangeSet(m.resolved, candidate, touched)
	prepared, err := m.preparer.Prepare(ctx, candidate, changeSet)
	if err != nil {
		return UpdateResult{Snapshot: currentSnapshot}, &ManagerError{
			Code: SettingsPrepareFailed, Fields: preparationFieldErrors(err), Snapshot: currentSnapshot,
		}
	}

	candidateDocument := Document{
		SchemaVersion: SettingsSchemaVersion,
		Revision:      m.document.Revision + 1,
		Overrides:     cloneOverrides(candidateOverrides),
		Credentials:   credentialTransaction.References(),
	}
	if err := m.store.Save(ctx, candidateDocument); err != nil {
		prepared.Abort(context.Background())
		return UpdateResult{Snapshot: currentSnapshot}, &ManagerError{Code: SettingsSaveFailed, Snapshot: currentSnapshot}
	}

	activation := prepared.Activate()
	m.document = cloneDocument(candidateDocument)
	m.resolved = cloneResolved(candidate)
	snapshot := makeSnapshot(candidateDocument.Revision, candidate, m.running)
	m.published.Store(&snapshot)
	rollbackCredentials = false
	_ = credentialTransaction.CleanupSuperseded(context.Background())
	return UpdateResult{
		Snapshot:        cloneSnapshot(snapshot),
		Applied:         append([]Group(nil), activation.Applied...),
		RestartRequired: append([]FieldKey(nil), snapshot.RestartRequired...),
		EmbeddingJobID:  activation.EmbeddingJobID,
	}, nil
}

func (m *Manager) Reset(ctx context.Context, revision uint64) (UpdateResult, error) {
	overrides := make(map[FieldKey]OverrideOperation)
	secrets := make(map[FieldKey]SecretOperation)
	for _, definition := range DocumentedFields() {
		if definition.Secret {
			secrets[definition.Key] = SecretOperation{Operation: SecretInherit}
		} else {
			overrides[definition.Key] = OverrideOperation{Operation: OverrideInherit}
		}
	}
	return m.Update(ctx, UpdateRequest{Revision: revision, Overrides: overrides, Secrets: secrets})
}

func validateOverrideOperations(current Overrides, operations map[FieldKey]OverrideOperation) (Overrides, map[FieldKey]struct{}, []FieldError) {
	candidate := cloneOverrides(current)
	touched := make(map[FieldKey]struct{})
	var issues []FieldError
	seen := make(map[FieldKey]bool, len(operations))
	for _, definition := range DocumentedFields() {
		operation, ok := operations[definition.Key]
		if !ok {
			continue
		}
		seen[definition.Key] = true
		touched[definition.Key] = struct{}{}
		if definition.Secret {
			issues = append(issues, FieldError{Field: definition.Key, Code: SettingsValidationFailed, Message: "secret fields require a secret operation"})
			continue
		}
		switch operation.Operation {
		case "", OverridePreserve:
			if operation.Value != nil {
				issues = append(issues, invalidOperationIssue(definition.Key))
			}
		case OverrideInherit:
			if operation.Value != nil {
				issues = append(issues, invalidOperationIssue(definition.Key))
				continue
			}
			delete(candidate, definition.Key)
		case OverrideReplace:
			value, err := normalizeOverrideValue(definition, operation.Value)
			if err != nil {
				issues = append(issues, FieldError{Field: definition.Key, Code: "SETTINGS_TYPE_INVALID", Message: "value has the wrong type"})
				continue
			}
			candidate[definition.Key] = value
		default:
			issues = append(issues, invalidOperationIssue(definition.Key))
		}
	}
	for key := range operations {
		if !seen[key] {
			issues = append(issues, FieldError{Field: key, Code: "SETTINGS_FIELD_UNKNOWN", Message: "unknown settings field"})
		}
	}
	return candidate, touched, issues
}

func validateSecretOperations(operations map[FieldKey]SecretOperation) (map[FieldKey]struct{}, []FieldError) {
	touched := make(map[FieldKey]struct{})
	var issues []FieldError
	seen := make(map[FieldKey]bool, len(operations))
	for _, definition := range DocumentedFields() {
		if !definition.Secret {
			continue
		}
		operation, ok := operations[definition.Key]
		if !ok {
			continue
		}
		seen[definition.Key] = true
		touched[definition.Key] = struct{}{}
		switch operation.Operation {
		case "", SecretPreserve:
			if operation.Value != "" {
				issues = append(issues, invalidOperationIssue(definition.Key))
			}
		case SecretInherit:
			if operation.Value != "" {
				issues = append(issues, invalidOperationIssue(definition.Key))
			}
		case SecretReplace:
			if operation.Value == "" {
				issues = append(issues, FieldError{Field: definition.Key, Code: "SETTINGS_VALUE_INVALID", Message: "replacement credential cannot be empty"})
			}
		default:
			issues = append(issues, invalidOperationIssue(definition.Key))
		}
	}
	for key := range operations {
		if !seen[key] {
			issues = append(issues, FieldError{Field: key, Code: "SETTINGS_FIELD_UNKNOWN", Message: "unknown secret field"})
		}
	}
	return touched, issues
}

func invalidOperationIssue(key FieldKey) FieldError {
	return FieldError{Field: key, Code: "SETTINGS_OPERATION_INVALID", Message: "invalid settings operation"}
}

func normalizeOverrideValue(definition FieldDefinition, value any) (any, error) {
	switch definition.Kind {
	case KindInteger:
		switch typed := value.(type) {
		case int:
			return int64(typed), nil
		case int64:
			return typed, nil
		case float64:
			if typed == float64(int64(typed)) {
				return int64(typed), nil
			}
		case json.Number:
			return typed.Int64()
		}
	case KindBoolean:
		if typed, ok := value.(bool); ok {
			return typed, nil
		}
	default:
		if typed, ok := value.(string); ok {
			return typed, nil
		}
	}
	return nil, errors.New("invalid override value type")
}

func makeChangeSet(current, candidate Resolved, touched map[FieldKey]struct{}) ChangeSet {
	var changes ChangeSet
	for _, definition := range DocumentedFields() {
		_, explicitlyTouched := touched[definition.Key]
		changed := explicitlyTouched || current.Source(definition.Key) != candidate.Source(definition.Key) ||
			!reflect.DeepEqual(valueForField(current.Values, definition.Key), valueForField(candidate.Values, definition.Key))
		if !changed {
			continue
		}
		changes.Fields = append(changes.Fields, definition.Key)
		if definition.ApplyMode == ApplyRestart {
			changes.Restart = append(changes.Restart, definition.Key)
		} else {
			changes.Live = append(changes.Live, definition.Key)
		}
	}
	return changes
}

type fieldErrorProvider interface{ FieldErrors() []FieldError }

func preparationFieldErrors(err error) []FieldError {
	var provider fieldErrorProvider
	if errors.As(err, &provider) {
		return append([]FieldError(nil), provider.FieldErrors()...)
	}
	return nil
}

func loadSavedCredentials(ctx context.Context, store CredentialStore, references CredentialReferences) (SecretValues, error) {
	result := make(SecretValues)
	for _, key := range []FieldKey{FieldLLMAPIKey, FieldEmbeddingsAPIKey} {
		generation := currentGeneration(references, key)
		if generation == "" {
			continue
		}
		value, err := store.Get(ctx, credentialAccount(key, generation))
		if err != nil {
			return nil, safeCredentialError(key, "read", err)
		}
		result[key] = value
	}
	return result, nil
}

func LoadStartup(ctx context.Context, environment Environment, store DocumentStore, credentials CredentialStore) (Document, Resolved, error) {
	if store == nil {
		return Document{}, Resolved{}, errors.New("settings document store is unavailable")
	}
	if credentials == nil {
		return Document{}, Resolved{}, errors.New("credential store is unavailable")
	}
	document, err := store.Load(ctx)
	if err != nil {
		return Document{}, Resolved{}, fmt.Errorf("load settings document: %w", err)
	}
	if document.SchemaVersion == 0 {
		document.SchemaVersion = SettingsSchemaVersion
	}
	if document.Overrides == nil {
		document.Overrides = Overrides{}
	}
	credentialValues, err := loadSavedCredentials(ctx, credentials, document.Credentials)
	if err != nil {
		return Document{}, Resolved{}, err
	}
	resolved := Resolve(cloneEnvironment(environment), cloneOverrides(document.Overrides), credentialValues)
	resolved.Credentials = document.Credentials
	if err := resolved.BootstrapError(); err != nil {
		return Document{}, Resolved{}, err
	}
	return cloneDocument(document), cloneResolved(resolved), nil
}

func environmentSecrets(environment Environment) SecretValues {
	result := make(SecretValues)
	for _, key := range []FieldKey{FieldLLMAPIKey, FieldEmbeddingsAPIKey} {
		if value := environment[key]; value != "" {
			result[key] = value
		}
	}
	return result
}

func savedCredentialValues(values SecretValues, references CredentialReferences) SecretValues {
	result := make(SecretValues)
	for _, key := range []FieldKey{FieldLLMAPIKey, FieldEmbeddingsAPIKey} {
		if currentGeneration(references, key) != "" {
			result[key] = values[key]
		}
	}
	return result
}

func cloneEnvironment(environment Environment) Environment {
	result := make(Environment, len(environment))
	for key, value := range environment {
		result[key] = value
	}
	return result
}

func cloneOverrides(overrides Overrides) Overrides {
	result := make(Overrides, len(overrides))
	for key, value := range overrides {
		result[key] = value
	}
	return result
}

func cloneDocument(document Document) Document {
	document.Overrides = cloneOverrides(document.Overrides)
	return document
}

func cloneResolved(resolved Resolved) Resolved {
	cloned := Resolved{Values: resolved.Values, Sources: make(map[FieldKey]Source, len(resolved.Sources)), Errors: append([]FieldError(nil), resolved.Errors...), Credentials: resolved.Credentials}
	for key, source := range resolved.Sources {
		cloned.Sources[key] = source
	}
	return cloned
}
