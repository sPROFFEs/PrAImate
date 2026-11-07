package serverboot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionIsLocalPrivateAndCleanupCannotDeleteAReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	first := Session{Origin: "http://127.0.0.1:8844", Token: "synthetic-local-owner-credential-for-test"}
	if err := SaveSession(path, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Token += "-new"
	if err := SaveSession(path, second); err != nil {
		t.Fatal(err)
	}
	RemoveSession(path, first.Token)
	current, err := LoadSession(path)
	if err != nil || current.Token != second.Token {
		t.Fatal("old process removed the new owner's session")
	}
	RemoveSession(path, second.Token)
	if _, err := LoadSession(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("session retained")
	}
}
func TestSessionRejectsExternalAddressesAndEmbeddedCredentials(t *testing.T) {
	for _, origin := range []string{"http://example.com:8844", "http://203.0.113.123:8844", "http://user:password@127.0.0.1:8844", "http://127.0.0.1:8844/path", "http://0.0.0.0:8844"} {
		if err := (Session{Origin: origin, Token: "synthetic-local-owner-credential-for-test"}).Validate(); err == nil {
			t.Fatalf("accepted %q", origin)
		}
	}
}
