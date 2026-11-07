package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestVerifyPasswordDoesNotOpenOrChangeDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := InitializeWithPassword(path, "a fixture password for startup")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := VerifyPassword(path, "wrong password"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("bad password accepted: %v", err)
	}
	if err := VerifyPassword(path, "a fixture password for startup"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword("a fixture password for startup", "a replacement fixture password"); err != nil {
		t.Fatal("verification mutated the envelope:", err)
	}
}
