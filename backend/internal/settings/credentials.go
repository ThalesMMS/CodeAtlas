package settings

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

var ErrCredentialNotFound = errors.New("credential not found")

type CredentialStore interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string) error
	Delete(context.Context, string) error
}

type SecretOperationName string

const (
	SecretPreserve SecretOperationName = "preserve"
	SecretReplace  SecretOperationName = "replace"
	SecretInherit  SecretOperationName = "inherit"
)

type SecretOperation struct {
	Operation SecretOperationName `json:"operation"`
	Value     string              `json:"value,omitempty"`
}

type SecretStatus struct {
	Configured bool   `json:"configured"`
	Source     Source `json:"source"`
}

func SecretStatusFor(source Source, configured bool) SecretStatus {
	return SecretStatus{Configured: configured, Source: source}
}

type CredentialTransaction struct {
	store      CredentialStore
	values     SecretValues
	references CredentialReferences
	created    []string
	superseded []string
}

func PrepareCredentialTransaction(ctx context.Context, store CredentialStore, current CredentialReferences, environment SecretValues, operations map[FieldKey]SecretOperation) (*CredentialTransaction, error) {
	if store == nil {
		return nil, errors.New("credential store is unavailable")
	}
	tx := &CredentialTransaction{
		store: store, values: make(SecretValues, 2), references: current,
	}
	for _, key := range []FieldKey{FieldLLMAPIKey, FieldEmbeddingsAPIKey} {
		operation := operations[key]
		if operation.Operation == "" {
			operation.Operation = SecretPreserve
		}
		if err := tx.prepareOne(ctx, key, currentGeneration(current, key), environment[key], operation); err != nil {
			_ = tx.Rollback(context.Background())
			return nil, err
		}
	}
	for key := range operations {
		if key != FieldLLMAPIKey && key != FieldEmbeddingsAPIKey {
			_ = tx.Rollback(context.Background())
			return nil, fmt.Errorf("credential operation is not allowed for %s", key)
		}
	}
	return tx, nil
}

func (t *CredentialTransaction) prepareOne(ctx context.Context, key FieldKey, currentGeneration, environmentValue string, operation SecretOperation) error {
	switch operation.Operation {
	case SecretPreserve:
		if operation.Value != "" {
			return fmt.Errorf("preserve operation cannot include a value for %s", key)
		}
		if currentGeneration == "" {
			t.values[key] = environmentValue
			return nil
		}
		value, err := t.store.Get(ctx, credentialAccount(key, currentGeneration))
		if err != nil {
			return safeCredentialError(key, "read", err)
		}
		t.values[key] = value
		return nil
	case SecretReplace:
		if operation.Value == "" {
			return fmt.Errorf("replacement credential cannot be empty for %s", key)
		}
		generation, err := newCredentialGeneration()
		if err != nil {
			return fmt.Errorf("create credential generation for %s", key)
		}
		account := credentialAccount(key, generation)
		if err := t.store.Set(ctx, account, operation.Value); err != nil {
			return safeCredentialError(key, "write", err)
		}
		t.created = append(t.created, account)
		if currentGeneration != "" {
			t.superseded = append(t.superseded, credentialAccount(key, currentGeneration))
		}
		t.values[key] = operation.Value
		setCurrentGeneration(&t.references, key, generation)
		return nil
	case SecretInherit:
		if operation.Value != "" {
			return fmt.Errorf("inherit operation cannot include a value for %s", key)
		}
		if currentGeneration != "" {
			t.superseded = append(t.superseded, credentialAccount(key, currentGeneration))
		}
		t.values[key] = environmentValue
		setCurrentGeneration(&t.references, key, "")
		return nil
	default:
		return fmt.Errorf("unknown credential operation for %s", key)
	}
}

func (t *CredentialTransaction) Values() SecretValues {
	copyValues := make(SecretValues, len(t.values))
	for key, value := range t.values {
		copyValues[key] = value
	}
	return copyValues
}

func (t *CredentialTransaction) References() CredentialReferences { return t.references }

func (t *CredentialTransaction) Rollback(ctx context.Context) error {
	var failures []error
	for index := len(t.created) - 1; index >= 0; index-- {
		if err := t.store.Delete(ctx, t.created[index]); err != nil && !errors.Is(err, ErrCredentialNotFound) {
			failures = append(failures, errors.New("delete candidate credential generation"))
		}
	}
	t.created = nil
	return errors.Join(failures...)
}

func (t *CredentialTransaction) CleanupSuperseded(ctx context.Context) error {
	var failures []error
	for _, account := range t.superseded {
		if err := t.store.Delete(ctx, account); err != nil && !errors.Is(err, ErrCredentialNotFound) {
			failures = append(failures, fmt.Errorf("delete superseded credential %s", account))
		}
	}
	t.superseded = nil
	return errors.Join(failures...)
}

func credentialAccount(key FieldKey, generation string) string {
	prefix := "llm-api-key:"
	if key == FieldEmbeddingsAPIKey {
		prefix = "embeddings-api-key:"
	}
	return prefix + generation
}

func currentGeneration(references CredentialReferences, key FieldKey) string {
	if key == FieldEmbeddingsAPIKey {
		return references.EmbeddingsAPIKeyGeneration
	}
	return references.LLMAPIKeyGeneration
}

func setCurrentGeneration(references *CredentialReferences, key FieldKey, generation string) {
	if key == FieldEmbeddingsAPIKey {
		references.EmbeddingsAPIKeyGeneration = generation
	} else {
		references.LLMAPIKeyGeneration = generation
	}
}

func newCredentialGeneration() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func safeCredentialError(key FieldKey, operation string, _ error) error {
	return fmt.Errorf("credential store could not %s %s", operation, key)
}
