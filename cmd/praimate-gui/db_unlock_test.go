package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/store"
)

func TestChangeDatabasePasswordBindingUpdatesEnvelopeAndRememberedCredential(t *testing.T) {
	keyring.MockInit()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	const currentPassword = "current strong password"
	const newPassword = "replacement strong password"
	st, err := store.InitializeWithPassword(path, currentPassword)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{st: st, core: &core.Core{}, dbPath: path}

	result, err := a.ChangeDatabasePassword(currentPassword, newPassword, newPassword, true)
	if err != nil {
		t.Fatalf("ChangeDatabasePassword: %v", err)
	}
	if !result.Unlocked || result.Warning != "" {
		t.Fatalf("result = %+v", result)
	}
	remembered, err := store.RecalledPassword(path)
	if err != nil {
		t.Fatal(err)
	}
	if remembered != newPassword {
		t.Fatal("credential store was not updated with the new password")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenWithPassword(path, currentPassword); !errors.Is(err, store.ErrInvalidPassword) {
		t.Fatalf("old password error = %v, want ErrInvalidPassword", err)
	}
	reopened, err := store.OpenWithPassword(path, newPassword)
	if err != nil {
		t.Fatalf("open with new password: %v", err)
	}
	_ = reopened.Close()
}

func TestChangeDatabasePasswordBindingRejectsMismatchedConfirmation(t *testing.T) {
	a := &App{}
	_, err := a.ChangeDatabasePassword("current strong password", "replacement strong password", "different strong password", false)
	if err == nil || err.Error() != "new database passwords do not match" {
		t.Fatalf("error = %v", err)
	}
}
