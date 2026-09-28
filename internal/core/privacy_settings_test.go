package core

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestPrivacyPatterns_PersistAndLoad(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	st, err := store.InitializeWithPassword(path, "correct horse battery staple")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	c, err := New(Options{Store: st})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.AddPrivacyPattern(ctx, `internal-\d{6}`); err != nil {
		t.Fatalf("AddPrivacyPattern: %v", err)
	}
	if hits := c.PrivacyScanner().Match("case internal-123456"); len(hits) != 1 || hits[0].Category != CatCustom {
		t.Fatalf("custom pattern not active: %+v", hits)
	}
	_ = st.Close()

	st2, err := store.OpenWithPassword(path, "correct horse battery staple")
	if err != nil {
		t.Fatalf("store.Open second: %v", err)
	}
	defer st2.Close()
	c2, err := New(Options{Store: st2})
	if err != nil {
		t.Fatalf("New second: %v", err)
	}
	patterns, err := c2.ListPrivacyPatterns(ctx)
	if err != nil {
		t.Fatalf("ListPrivacyPatterns: %v", err)
	}
	if len(patterns) != 1 || patterns[0] != `internal-\d{6}` {
		t.Fatalf("patterns not persisted: %+v", patterns)
	}
	if hits := c2.PrivacyScanner().Match("case internal-654321"); len(hits) != 1 || hits[0].Category != CatCustom {
		t.Fatalf("persisted custom pattern not loaded: %+v", hits)
	}
}

func TestPrivacyPatterns_RejectInvalidRegex(t *testing.T) {
	c := newMemCore(t)
	if err := c.AddPrivacyPattern(context.Background(), `[invalid`); err == nil {
		t.Fatal("expected invalid regex error")
	}
	patterns, err := c.ListPrivacyPatterns(context.Background())
	if err != nil {
		t.Fatalf("ListPrivacyPatterns: %v", err)
	}
	if len(patterns) != 0 {
		t.Fatalf("invalid pattern should not persist: %+v", patterns)
	}
}

func TestPrivacyPatterns_Delete(t *testing.T) {
	c := newMemCore(t)
	ctx := context.Background()
	_ = c.AddPrivacyPattern(ctx, `one-\d+`)
	_ = c.AddPrivacyPattern(ctx, `two-\d+`)
	if err := c.DeletePrivacyPattern(ctx, 0); err != nil {
		t.Fatalf("DeletePrivacyPattern: %v", err)
	}
	patterns, err := c.ListPrivacyPatterns(ctx)
	if err != nil {
		t.Fatalf("ListPrivacyPatterns: %v", err)
	}
	if len(patterns) != 1 || patterns[0] != `two-\d+` {
		t.Fatalf("delete left wrong patterns: %+v", patterns)
	}
	if hits := c.PrivacyScanner().Match("one-1 two-2"); len(hits) != 1 || hits[0].Value != "two-2" {
		t.Fatalf("scanner not updated after delete: %+v", hits)
	}
}
