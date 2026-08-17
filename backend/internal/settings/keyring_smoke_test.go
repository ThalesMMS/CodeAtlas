package settings

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"runtime"
	"testing"
)

func TestSystemKeyringSmoke(t *testing.T) {
	if os.Getenv("CODEATLAS_TEST_SYSTEM_KEYRING") != "1" {
		t.Skip("set CODEATLAS_TEST_SYSTEM_KEYRING=1 to exercise the real OS credential vault")
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("system keyring smoke coverage is supported on Windows and macOS")
	}

	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	account := "CodeAtlas-test-" + hex.EncodeToString(random[:])
	secret := "smoke-" + hex.EncodeToString(random[:])
	store := NewKeyringCredentialStore()
	ctx := context.Background()

	if err := store.Set(ctx, account, secret); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Delete(context.Background(), account); err != nil && !errors.Is(err, ErrCredentialNotFound) {
			t.Errorf("cleanup Delete() error = %v", err)
		}
	})
	got, err := store.Get(ctx, account)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != secret {
		t.Fatal("Get() returned a different credential")
	}
	if err := store.Delete(ctx, account); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(ctx, account); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrCredentialNotFound", err)
	}
}
