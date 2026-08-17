package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type memoryCredentialStore struct {
	values      map[string]string
	sets        []string
	deletes     []string
	failSetCall int
	setCalls    int
	getErr      error
}

func newMemoryCredentialStore() *memoryCredentialStore {
	return &memoryCredentialStore{values: map[string]string{}}
}

func (s *memoryCredentialStore) Get(context.Context, string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	for _, value := range s.values {
		return value, nil
	}
	return "", ErrCredentialNotFound
}

func (s *memoryCredentialStore) GetAccount(_ context.Context, account string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	value, ok := s.values[account]
	if !ok {
		return "", ErrCredentialNotFound
	}
	return value, nil
}

func (s *memoryCredentialStore) Set(_ context.Context, account, value string) error {
	s.setCalls++
	if s.failSetCall > 0 && s.setCalls == s.failSetCall {
		return errors.New("vault locked with secret " + value)
	}
	s.values[account] = value
	s.sets = append(s.sets, account)
	return nil
}

func (s *memoryCredentialStore) Delete(_ context.Context, account string) error {
	delete(s.values, account)
	s.deletes = append(s.deletes, account)
	return nil
}

type accountCredentialStore struct{ *memoryCredentialStore }

func (s accountCredentialStore) Get(ctx context.Context, account string) (string, error) {
	return s.GetAccount(ctx, account)
}

func TestCredentialTransactionPreserveReplaceAndInherit(t *testing.T) {
	store := newMemoryCredentialStore()
	store.values[credentialAccount(FieldLLMAPIKey, "old-chat")] = "old-chat-secret"
	store.values[credentialAccount(FieldEmbeddingsAPIKey, "old-embed")] = "old-embed-secret"
	references := CredentialReferences{LLMAPIKeyGeneration: "old-chat", EmbeddingsAPIKeyGeneration: "old-embed"}
	environment := SecretValues{FieldEmbeddingsAPIKey: "env-embed-secret"}

	tx, err := PrepareCredentialTransaction(context.Background(), accountCredentialStore{store}, references, environment, map[FieldKey]SecretOperation{
		FieldLLMAPIKey:        {Operation: SecretReplace, Value: "new-chat-secret"},
		FieldEmbeddingsAPIKey: {Operation: SecretInherit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Values()[FieldLLMAPIKey] != "new-chat-secret" || tx.Values()[FieldEmbeddingsAPIKey] != "env-embed-secret" {
		t.Fatalf("candidate values = %#v", tx.Values())
	}
	if tx.References().LLMAPIKeyGeneration == "" || tx.References().LLMAPIKeyGeneration == "old-chat" {
		t.Fatalf("new chat generation = %q", tx.References().LLMAPIKeyGeneration)
	}
	if tx.References().EmbeddingsAPIKeyGeneration != "" {
		t.Fatalf("embedding generation = %q, want inherited", tx.References().EmbeddingsAPIKeyGeneration)
	}
	if len(store.sets) != 1 {
		t.Fatalf("vault sets = %#v, want one replacement", store.sets)
	}
	if err := tx.CleanupSuperseded(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.values[credentialAccount(FieldLLMAPIKey, "old-chat")]; ok {
		t.Fatal("old chat generation was not deleted")
	}
	if _, ok := store.values[credentialAccount(FieldEmbeddingsAPIKey, "old-embed")]; ok {
		t.Fatal("old embedding generation was not deleted")
	}
}

func TestCredentialTransactionPreserveReadsExistingGenerationWithoutWriting(t *testing.T) {
	store := newMemoryCredentialStore()
	store.values[credentialAccount(FieldLLMAPIKey, "existing")] = "existing-secret"
	tx, err := PrepareCredentialTransaction(context.Background(), accountCredentialStore{store}, CredentialReferences{LLMAPIKeyGeneration: "existing"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Values()[FieldLLMAPIKey] != "existing-secret" || len(store.sets) != 0 {
		t.Fatalf("preserve values/sets = %#v/%#v", tx.Values(), store.sets)
	}
}

func TestCredentialTransactionRollsBackEveryNewGenerationAfterPartialFailure(t *testing.T) {
	store := newMemoryCredentialStore()
	store.failSetCall = 2
	_, err := PrepareCredentialTransaction(context.Background(), accountCredentialStore{store}, CredentialReferences{}, nil, map[FieldKey]SecretOperation{
		FieldLLMAPIKey:        {Operation: SecretReplace, Value: "chat-sentinel"},
		FieldEmbeddingsAPIKey: {Operation: SecretReplace, Value: "embed-sentinel"},
	})
	if err == nil {
		t.Fatal("Prepare succeeded despite second vault write failure")
	}
	if strings.Contains(err.Error(), "chat-sentinel") || strings.Contains(err.Error(), "embed-sentinel") {
		t.Fatalf("error leaked secret: %v", err)
	}
	if len(store.values) != 0 || len(store.deletes) != 1 {
		t.Fatalf("rollback values/deletes = %#v/%#v", store.values, store.deletes)
	}
}

func TestSecretStatusNeverContainsValue(t *testing.T) {
	status := SecretStatusFor(SourceSettings, true)
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || string(encoded) != `{"configured":true,"source":"settings"}` {
		t.Fatalf("status JSON = %s", encoded)
	}
}
