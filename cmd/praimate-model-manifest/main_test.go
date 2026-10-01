package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/artifacts"
)

func TestPublisherProducesManifestAcceptedByClient(t *testing.T) {
	root := t.TempDir()
	model := []byte("publisher test model")
	if err := os.WriteFile(filepath.Join(root, "model.gguf"), model, 0600); err != nil {
		t.Fatal(err)
	}
	m := artifacts.Manifest{CatalogVersion: "test-v1", Artifacts: map[string]artifacts.Artifact{"model/v1": {Filename: "model.gguf", Format: "file", LicenseName: "Test", LicenseURL: "https://example.com/license", UpstreamProject: "fixture", UpstreamVersion: "v1", UpstreamSHA256: strings.Repeat("a", 64)}}}
	raw, _ := json.Marshal(m)
	input := filepath.Join(root, "template.json")
	os.WriteFile(input, raw, 0600)
	var log bytes.Buffer
	out := filepath.Join(root, "output")
	if err := publish(input, root, out, &log); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := artifacts.VerifyManifest(manifest, sums)
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifacts["model/v1"].Size != int64(len(model)) {
		t.Fatal("publisher did not derive size from actual payload")
	}
	manifest[0] ^= 0xff
	if _, err := artifacts.VerifyManifest(manifest, sums); err == nil {
		t.Fatal("accepted corrupt manifest")
	}
}
