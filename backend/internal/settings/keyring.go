package settings

import (
	"context"
	"errors"

	"github.com/zalando/go-keyring"
)

const keyringServiceName = "CodeAtlas"

type KeyringCredentialStore struct {
	service string
}

func NewKeyringCredentialStore() *KeyringCredentialStore {
	return &KeyringCredentialStore{service: keyringServiceName}
}

func (s *KeyringCredentialStore) Get(ctx context.Context, account string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := keyring.Get(s.service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrCredentialNotFound
	}
	if err != nil {
		return "", errors.New("system credential vault read failed")
	}
	return value, nil
}

func (s *KeyringCredentialStore) Set(ctx context.Context, account, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := keyring.Set(s.service, account, value); err != nil {
		return errors.New("system credential vault write failed")
	}
	return nil
}

func (s *KeyringCredentialStore) Delete(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := keyring.Delete(s.service, account); errors.Is(err, keyring.ErrNotFound) {
		return ErrCredentialNotFound
	} else if err != nil {
		return errors.New("system credential vault delete failed")
	}
	return nil
}
