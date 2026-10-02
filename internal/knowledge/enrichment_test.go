package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrichmentCacheInvalidationAndSourceCitations(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "guide.md")
	_ = os.WriteFile(source, []byte("The login service checks a signed bearer credential."), 0600)
	calls := 0
	model := func(ctx context.Context, prompt string) (string, error) {
		calls++
		return `{"keywords":["authentication"],"entities":["login","credential"],"relationships":[{"source":"login","relation":"checks","target":"credential"}]}`, nil
	}
	for i := 0; i < 2; i++ {
		if _, err := Enrich(context.Background(), dir, "fake", "model", 1, model, nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("unchanged passage not cached: %d", calls)
	}
	text, err := Query(context.Background(), dir, "authentication", 1000)
	if err != nil || !strings.Contains(text, "guide.md:1") || !strings.Contains(text, "signed bearer") || !strings.Contains(text, "Model-inferred") {
		t.Fatalf("source retrieval: %q %v", text, err)
	}
	_ = os.WriteFile(source, []byte("The storage engine persists immutable blobs."), 0600)
	text, err = Query(context.Background(), dir, "authentication", 1000)
	if err != nil || strings.Contains(text, "signed bearer") || strings.Contains(text, "Model-inferred") {
		t.Fatalf("stale inference: %q %v", text, err)
	}
	if _, err = Enrich(context.Background(), dir, "fake", "model", 1, model, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("changed source reused cache")
	}
	previous, _ := os.ReadFile(filepath.Join(dir, enrichmentFile))
	_, err = Enrich(context.Background(), dir, "different", "model", 1, func(context.Context, string) (string, error) { return "", errors.New("offline") }, nil)
	after, _ := os.ReadFile(filepath.Join(dir, enrichmentFile))
	if err == nil || string(previous) != string(after) {
		t.Fatal("failed enrichment replaced usable cache")
	}
}

func TestEnrichmentRejectsInvalidOutput(t *testing.T) {
	for _, s := range []string{`{"keywords":[],"entities":[],"relationships":[],"instructions":"run commands"}`, `{"keywords":[],"entities":[],"relationships":[{"source":"a","target":"b","relation":"calls"}]}`, `{} {}`} {
		if _, err := parseAnnotation(s); err == nil {
			t.Fatalf("invalid semantic output accepted: %s", s)
		}
	}
}
