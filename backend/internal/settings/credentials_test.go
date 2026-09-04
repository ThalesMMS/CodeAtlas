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
}

func newMemoryCredentialStore() *memoryCredentialStore {
	return &memoryCredentialStore{values: map[string]string{}}
}

// Get resolves the exact account, the way a real keyring does: a transaction
// that asks for a superseded generation must miss rather than be handed some
// other generation's secret.
func (s *memoryCredentialStore) Get(_ context.Context, account string) (string, error) {
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

func TestCredentialTransactionReplaceRotatesGenerationAndRetiresTheOldOne(t *testing.T) {
	store := newMemoryCredentialStore()
	store.values[credentialAccount(FieldLLMAPIKey, "old-chat")] = "old-chat-secret"
	references := CredentialReferences{LLMAPIKeyGeneration: "old-chat"}

	tx, err := PrepareCredentialTransaction(context.Background(), store, references, nil, map[FieldKey]SecretOperation{
		FieldLLMAPIKey: {Operation: SecretReplace, Value: "new-chat-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Values()[FieldLLMAPIKey] != "new-chat-secret" {
		t.Fatalf("candidate values = %#v", tx.Values())
	}
	if tx.References().LLMAPIKeyGeneration == "" || tx.References().LLMAPIKeyGeneration == "old-chat" {
		t.Fatalf("new chat generation = %q", tx.References().LLMAPIKeyGeneration)
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
	if store.values[credentialAccount(FieldLLMAPIKey, tx.References().LLMAPIKeyGeneration)] != "new-chat-secret" {
		t.Fatalf("new generation missing from vault: %#v", store.values)
	}
}

func TestCredentialTransactionInheritClearsGenerationAndFallsBackToEnvironment(t *testing.T) {
	store := newMemoryCredentialStore()
	store.values[credentialAccount(FieldLLMAPIKey, "old-chat")] = "old-chat-secret"
	references := CredentialReferences{LLMAPIKeyGeneration: "old-chat"}
	environment := SecretValues{FieldLLMAPIKey: "env-chat-secret"}

	tx, err := PrepareCredentialTransaction(context.Background(), store, references, environment, map[FieldKey]SecretOperation{
		FieldLLMAPIKey: {Operation: SecretInherit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Values()[FieldLLMAPIKey] != "env-chat-secret" {
		t.Fatalf("candidate values = %#v, want the environment value", tx.Values())
	}
	if tx.References().LLMAPIKeyGeneration != "" {
		t.Fatalf("chat generation = %q, want inherited", tx.References().LLMAPIKeyGeneration)
	}
	if len(store.sets) != 0 {
		t.Fatalf("vault sets = %#v, want none for inherit", store.sets)
	}
	if err := tx.CleanupSuperseded(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.values[credentialAccount(FieldLLMAPIKey, "old-chat")]; ok {
		t.Fatal("old chat generation was not deleted")
	}
}

func TestCredentialTransactionPreserveReadsExistingGenerationWithoutWriting(t *testing.T) {
	store := newMemoryCredentialStore()
	store.values[credentialAccount(FieldLLMAPIKey, "existing")] = "existing-secret"
	tx, err := PrepareCredentialTransaction(context.Background(), store, CredentialReferences{LLMAPIKeyGeneration: "existing"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Values()[FieldLLMAPIKey] != "existing-secret" || len(store.sets) != 0 {
		t.Fatalf("preserve values/sets = %#v/%#v", tx.Values(), store.sets)
	}
}

func TestCredentialTransactionRollsBackEveryNewGenerationAfterFailure(t *testing.T) {
	t.Run("vault write fails", func(t *testing.T) {
		store := newMemoryCredentialStore()
		store.failSetCall = 1
		_, err := PrepareCredentialTransaction(context.Background(), store, CredentialReferences{}, nil, map[FieldKey]SecretOperation{
			FieldLLMAPIKey: {Operation: SecretReplace, Value: "chat-sentinel"},
		})
		if err == nil {
			t.Fatal("Prepare succeeded despite a failing vault write")
		}
		if strings.Contains(err.Error(), "chat-sentinel") {
			t.Fatalf("error leaked secret: %v", err)
		}
		if len(store.values) != 0 {
			t.Fatalf("rollback left credentials behind: %#v", store.values)
		}
	})

	t.Run("unsupported field rejected after a successful write", func(t *testing.T) {
		store := newMemoryCredentialStore()
		_, err := PrepareCredentialTransaction(context.Background(), store, CredentialReferences{}, nil, map[FieldKey]SecretOperation{
			FieldLLMAPIKey: {Operation: SecretReplace, Value: "chat-sentinel"},
			FieldWorkspace: {Operation: SecretReplace, Value: "bogus-sentinel"},
		})
		if err == nil {
			t.Fatal("Prepare succeeded for a field that is not a credential")
		}
		if strings.Contains(err.Error(), "chat-sentinel") || strings.Contains(err.Error(), "bogus-sentinel") {
			t.Fatalf("error leaked secret: %v", err)
		}
		if len(store.values) != 0 || len(store.deletes) != 1 {
			t.Fatalf("rollback values/deletes = %#v/%#v", store.values, store.deletes)
		}
	})
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
