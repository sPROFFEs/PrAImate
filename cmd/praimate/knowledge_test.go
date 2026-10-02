package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentConvertCommandWithoutDatabase(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "review.md")
	dest := filepath.Join(dir, "review.yaml")
	if err := os.WriteFile(source, []byte("---\nmodel: local/test\npermission:\n  bash: deny\n---\nReview code."), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"agent", "convert", "--input", source, "--output", dest}); code != 0 {
		t.Fatalf("conversion returned %d", code)
	}
	raw, err := os.ReadFile(dest)
	if err != nil || !strings.Contains(string(raw), "foreign:") || !strings.Contains(string(raw), "local/test") {
		t.Fatalf("lost metadata: %s %v", raw, err)
	}
	if code := run([]string{"agent", "convert", "--input", source, "--output", dest}); code != 1 {
		t.Fatal("overwrote destination")
	}
	again, _ := os.ReadFile(dest)
	if string(again) != string(raw) {
		t.Fatal("modified destination on failure")
	}
	if code := run([]string{"agent", "convert", "--input", dest, "--output", filepath.Join(dir, "export.md")}); code != 0 {
		t.Fatalf("Markdown export returned %d", code)
	}
}
func TestKnowledgeCommandOffline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# JWT\nRefresh token interval is 900 seconds."), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"knowledge", "index", "--root", dir}, {"knowledge", "query", "--root", dir, "--question", "JWT", "--budget", "200"}, {"knowledge", "graph", "--root", dir}} {
		if code := run(args); code != 0 {
			t.Fatalf("%v returned %d", args, code)
		}
	}
	for _, args := range [][]string{{"knowledge"}, {"knowledge", "query", "--root", dir, "--question", "JWT", "--budget", "1"}, {"knowledge", "invalid", "--root", dir}, {"knowledge", "query"}} {
		if code := run(args); code == 0 {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
}
