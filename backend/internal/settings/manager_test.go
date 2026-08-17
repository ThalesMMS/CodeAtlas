package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type managerDocumentStore struct {
	document Document
	events   *[]string
	failSave bool
}

func (s *managerDocumentStore) Load(context.Context) (Document, error) {
	return cloneDocument(s.document), nil
}

func (s *managerDocumentStore) Save(_ context.Context, document Document) error {
	*s.events = append(*s.events, "file:save")
	if s.failSave {
		return errors.New("disk unavailable")
	}
	s.document = cloneDocument(document)
	return nil
}

type managerCredentialStore struct {
	values  map[string]string
	events  *[]string
	failSet bool
}

func (s *managerCredentialStore) Get(_ context.Context, account string) (string, error) {
	value, ok := s.values[account]
	if !ok {
		return "", ErrCredentialNotFound
	}
	return value, nil
}

func (s *managerCredentialStore) Set(_ context.Context, account, value string) error {
	*s.events = append(*s.events, "vault:set")
	if s.failSet {
		return errors.New("vault locked: " + value)
	}
	s.values[account] = value
	return nil
}

func (s *managerCredentialStore) Delete(_ context.Context, account string) error {
	*s.events = append(*s.events, "vault:delete")
	delete(s.values, account)
	return nil
}

type managerRuntimePreparer struct {
	events      *[]string
	failPrepare bool
	prepared    int
	activated   int
}

func (p *managerRuntimePreparer) Prepare(_ context.Context, _ Resolved, _ ChangeSet) (PreparedRuntime, error) {
	*p.events = append(*p.events, "runtime:prepare")
	if p.failPrepare {
		return nil, errors.New("candidate probe failed")
	}
	p.prepared++
	return &managerPreparedRuntime{parent: p}, nil
}

type managerPreparedRuntime struct{ parent *managerRuntimePreparer }

func (p *managerPreparedRuntime) Activate() ActivationResult {
	*p.parent.events = append(*p.parent.events, "runtime:activate")
	p.parent.activated++
	return ActivationResult{Applied: []Group{GroupLLM}}
}

func (p *managerPreparedRuntime) Abort(context.Context) {
	*p.parent.events = append(*p.parent.events, "runtime:abort")
}

func TestManagerUpdateUsesTransactionalOrderAndPublishesSanitizedSnapshot(t *testing.T) {
	manager, store, credentials, runtime, events := newManagerFixture(t)

	result, err := manager.Update(context.Background(), UpdateRequest{
		Revision: 3,
		Overrides: map[FieldKey]OverrideOperation{
			FieldLLMModel:  {Operation: OverrideReplace, Value: "next-model"},
			FieldWorkspace: {Operation: OverrideReplace, Value: "next-workspace"},
		},
		Secrets: map[FieldKey]SecretOperation{
			FieldLLMAPIKey: {Operation: SecretReplace, Value: "new-secret-sentinel"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"vault:set", "runtime:prepare", "file:save", "runtime:activate", "vault:delete"}
	if !reflect.DeepEqual(*events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", *events, wantEvents)
	}
	if store.document.Revision != 4 || store.document.Overrides[FieldLLMModel] != "next-model" {
		t.Fatalf("saved document = %#v", store.document)
	}
	if runtime.prepared != 1 || runtime.activated != 1 {
		t.Fatalf("prepare/activate = %d/%d", runtime.prepared, runtime.activated)
	}
	if _, ok := credentials.values[credentialAccount(FieldLLMAPIKey, "old")]; ok {
		t.Fatal("superseded credential was not cleaned up")
	}
	if result.Snapshot.Revision != 4 || !containsField(result.RestartRequired, FieldWorkspace) {
		t.Fatalf("update result = %#v", result)
	}
	workspace := snapshotField(t, result.Snapshot, FieldWorkspace)
	if workspace.Value != "next-workspace" || workspace.RunningValue != "." {
		t.Fatalf("workspace snapshot = %#v", workspace)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "new-secret-sentinel") || strings.Contains(string(encoded), "old-secret-sentinel") {
		t.Fatalf("result leaked a credential: %s", encoded)
	}
	secret := snapshotField(t, result.Snapshot, FieldLLMAPIKey)
	if secret.Configured == nil || !*secret.Configured || secret.Value != nil {
		t.Fatalf("secret snapshot = %#v", secret)
	}
}

func TestManagerRejectsStaleRevisionWithoutWorkAndReturnsFreshSnapshot(t *testing.T) {
	manager, _, _, _, events := newManagerFixture(t)
	result, err := manager.Update(context.Background(), UpdateRequest{Revision: 2})
	if errorCode(err) != SettingsRevisionConflict {
		t.Fatalf("error = %v", err)
	}
	if result.Snapshot.Revision != 3 || len(*events) != 0 {
		t.Fatalf("result/events = %#v/%#v", result, *events)
	}
}

func TestManagerUpdateRejectsInvalidPatchesBeforeVaultOrRuntimeWork(t *testing.T) {
	tests := []struct {
		name    string
		request UpdateRequest
	}{
		{name: "unknown key", request: UpdateRequest{Revision: 3, Overrides: map[FieldKey]OverrideOperation{"UNKNOWN": {Operation: OverrideReplace, Value: "x"}}}},
		{name: "secret in overrides", request: UpdateRequest{Revision: 3, Overrides: map[FieldKey]OverrideOperation{FieldLLMAPIKey: {Operation: OverrideReplace, Value: "do-not-store"}}}},
		{name: "type mismatch", request: UpdateRequest{Revision: 3, Overrides: map[FieldKey]OverrideOperation{FieldMaxFileBytes: {Operation: OverrideReplace, Value: "large"}}}},
		{name: "unknown operation", request: UpdateRequest{Revision: 3, Overrides: map[FieldKey]OverrideOperation{FieldLLMModel: {Operation: "merge", Value: "x"}}}},
		{name: "invalid candidate before secret write", request: UpdateRequest{Revision: 3,
			Overrides: map[FieldKey]OverrideOperation{FieldLLMBaseURL: {Operation: OverrideReplace, Value: "not-a-url"}},
			Secrets:   map[FieldKey]SecretOperation{FieldLLMAPIKey: {Operation: SecretReplace, Value: "rejected-secret"}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, store, _, runtime, events := newManagerFixture(t)
			_, err := manager.Update(context.Background(), test.request)
			if errorCode(err) != SettingsValidationFailed {
				t.Fatalf("error = %v", err)
			}
			if len(*events) != 0 || store.document.Revision != 3 || runtime.prepared != 0 {
				t.Fatalf("invalid update did work: events=%#v document=%#v", *events, store.document)
			}
			if strings.Contains(err.Error(), "rejected-secret") || strings.Contains(err.Error(), "do-not-store") {
				t.Fatalf("error leaked input: %v", err)
			}
		})
	}
}

func TestManagerUpdateRollsBackEveryFallibleStage(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*managerDocumentStore, *managerCredentialStore, *managerRuntimePreparer)
		want      []string
	}{
		{name: "vault", configure: func(_ *managerDocumentStore, c *managerCredentialStore, _ *managerRuntimePreparer) { c.failSet = true }, want: []string{"vault:set"}},
		{name: "prepare", configure: func(_ *managerDocumentStore, _ *managerCredentialStore, r *managerRuntimePreparer) {
			r.failPrepare = true
		}, want: []string{"vault:set", "runtime:prepare", "vault:delete"}},
		{name: "save", configure: func(s *managerDocumentStore, _ *managerCredentialStore, _ *managerRuntimePreparer) { s.failSave = true }, want: []string{"vault:set", "runtime:prepare", "file:save", "runtime:abort", "vault:delete"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, store, credentials, runtime, events := newManagerFixture(t)
			test.configure(store, credentials, runtime)
			_, err := manager.Update(context.Background(), UpdateRequest{
				Revision:  3,
				Overrides: map[FieldKey]OverrideOperation{FieldLLMModel: {Operation: OverrideReplace, Value: "candidate"}},
				Secrets:   map[FieldKey]SecretOperation{FieldLLMAPIKey: {Operation: SecretReplace, Value: "rollback-secret"}},
			})
			if err == nil {
				t.Fatal("update unexpectedly succeeded")
			}
			if !reflect.DeepEqual(*events, test.want) {
				t.Fatalf("events = %#v, want %#v", *events, test.want)
			}
			if store.document.Revision != 3 || manager.Snapshot().Revision != 3 || runtime.activated != 0 {
				t.Fatalf("old state changed: document=%#v snapshot=%#v", store.document, manager.Snapshot())
			}
			for _, value := range credentials.values {
				if value == "rollback-secret" {
					t.Fatal("candidate credential survived rollback")
				}
			}
			if strings.Contains(err.Error(), "rollback-secret") {
				t.Fatalf("error leaked credential: %v", err)
			}
		})
	}
}

func TestManagerResetIsPreparedAllInheritUpdate(t *testing.T) {
	manager, store, _, runtime, events := newManagerFixture(t)
	result, err := manager.Reset(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.document.Overrides) != 0 || store.document.Credentials.LLMAPIKeyGeneration != "" {
		t.Fatalf("reset document = %#v", store.document)
	}
	if runtime.prepared != 1 || runtime.activated != 1 {
		t.Fatalf("prepare/activate = %d/%d", runtime.prepared, runtime.activated)
	}
	if result.Snapshot.Revision != 4 || snapshotField(t, result.Snapshot, FieldLLMModel).Source != SourceEnv {
		t.Fatalf("reset snapshot = %#v", result.Snapshot)
	}
	want := []string{"runtime:prepare", "file:save", "runtime:activate", "vault:delete"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("events = %#v, want %#v", *events, want)
	}
}

func TestManagerSnapshotReturnsDeepCopiesDuringConcurrentReads(t *testing.T) {
	manager, _, _, _, _ := newManagerFixture(t)
	first := manager.Snapshot()
	first.Groups[GroupLLM][0].Value = "mutated"
	delete(first.Groups, GroupGeneral)
	if _, ok := manager.Snapshot().Groups[GroupGeneral]; !ok {
		t.Fatal("caller mutated published snapshot")
	}

	var wait sync.WaitGroup
	for index := 0; index < 50; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for read := 0; read < 100; read++ {
				_ = manager.Snapshot()
			}
		}()
	}
	wait.Wait()
}

func newManagerFixture(t *testing.T) (*Manager, *managerDocumentStore, *managerCredentialStore, *managerRuntimePreparer, *[]string) {
	t.Helper()
	events := &[]string{}
	document := Document{
		SchemaVersion: SettingsSchemaVersion,
		Revision:      3,
		Overrides:     Overrides{FieldLLMModel: "saved-model"},
		Credentials:   CredentialReferences{LLMAPIKeyGeneration: "old"},
	}
	store := &managerDocumentStore{document: document, events: events}
	credentials := &managerCredentialStore{values: map[string]string{credentialAccount(FieldLLMAPIKey, "old"): "old-secret-sentinel"}, events: events}
	runtime := &managerRuntimePreparer{events: events}
	manager, err := NewManager(context.Background(), validManagerEnvironment(), store, credentials, runtime)
	if err != nil {
		t.Fatal(err)
	}
	return manager, store, credentials, runtime, events
}

func validManagerEnvironment() Environment {
	return Environment{
		FieldLLMBaseURL: "https://env.example/v1",
		FieldLLMModel:   "env-model",
		FieldLLMAPIKey:  "env-secret-sentinel",
	}
}

func snapshotField(t *testing.T, snapshot SanitizedSnapshot, key FieldKey) FieldSnapshot {
	t.Helper()
	definition, ok := Definition(key)
	if !ok {
		t.Fatalf("unknown field %s", key)
	}
	for _, field := range snapshot.Groups[definition.Group] {
		if field.Key == key {
			return field
		}
	}
	t.Fatalf("snapshot lacks %s", key)
	return FieldSnapshot{}
}

func containsField(fields []FieldKey, key FieldKey) bool {
	for _, field := range fields {
		if field == key {
			return true
		}
	}
	return false
}

func errorCode(err error) string {
	var managerError *ManagerError
	if errors.As(err, &managerError) {
		return managerError.Code
	}
	return ""
}
