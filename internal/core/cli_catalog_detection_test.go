package core

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/ollama"
)

func TestCLIModelCatalogueParsesStructuredAndPlainIDs(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		qualified bool
		want      []string
	}{
		{"\x1b[32mprovider/z\x1b[0m\nprovider/a\nprovider/z\nLoading models...\nbare\n", true, []string{"provider/a", "provider/z"}},
		{`{"data":[{"id":"provider/nested/model"},{"id":"provider/a"},{"id":"provider/a"}]}`, true, []string{"provider/a", "provider/nested/model"}},
		{`{"models":[{"slug":"gemini-flash"},"gemini-pro"]}`, false, []string{"gemini-flash", "gemini-pro"}},
	} {
		if got := parseCLIModelIDs([]byte(tc.raw), tc.qualified); !slices.Equal(got, tc.want) {
			t.Fatalf("catalogue %q: %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestCLIModelCatalogueKeepsProviderIDsAndUsesManagedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := ollama.OpenCodeConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"provider":{"gpu":{"models":{"qwen":{}}},"local":{"models":{"qwen":{}}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "praimate-code"), []byte("#!/bin/sh\nprintf 'cloud/new-model\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	old, oldErr := GetCLIAdapter("praimate-code")
	RegisterCLIAdapter(&execAdapter{name: "praimate-code", bin: "praimate-code", extraDirs: []string{dir}})
	t.Cleanup(func() {
		if oldErr == nil {
			RegisterCLIAdapter(old)
		} else {
			UnregisterCLIAdapter("praimate-code")
		}
	})
	if got := RefreshCLIModels(context.Background(), "praimate-code"); !slices.Equal(got, []string{"cloud/new-model", "gpu/qwen", "local/qwen"}) {
		t.Fatalf("ambiguous aliases/static models leaked: %v", got)
	}
}

func TestCLIModelCatalogueCodexLiveListReplacesFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	fakeBinOnPath(t, "codex", `printf '%s\n' '{"models":[{"slug":"available-model","visibility":"list"},{"slug":"hidden","visibility":"hide"}]}'`)
	if got := RefreshCLIModels(context.Background(), "codex"); !slices.Equal(got, []string{"available-model"}) {
		t.Fatalf("unavailable defaults leaked into live catalogue: %v", got)
	}
}
