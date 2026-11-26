package installer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGraphifyUsesCanonicalBundledInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable fixture uses POSIX shell")
	}
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	bundled, err := BundledGraphifyPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(bundled), 0755); err != nil {
		t.Fatal(err)
	}
	for path, version := range map[string]string{bundled: "0.8.36", filepath.Join(filepath.Dir(bundled), "graphify"): "0.1.0"} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho graphify "+version+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range DetectTools(context.Background()) {
		if tool.ID == ToolGraphify && (!tool.Available || tool.Binary != bundled) {
			t.Fatalf("catalog selected %+v, expected %s", tool, bundled)
		}
	}
	if path, ok := ResolveGraphify(context.Background()); !ok || path != bundled {
		t.Fatalf("RAG selected %q, expected %s", path, bundled)
	}
	if err := os.WriteFile(bundled, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(filepath.Dir(bundled), "graphify")
	if path, ok := ResolveGraphify(context.Background()); !ok || path != legacy {
		t.Fatalf("RAG did not fall back after the bundled probe failed: %q", path)
	}

}
