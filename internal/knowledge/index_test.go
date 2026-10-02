package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func put(t *testing.T, dir, path, body string) {
	t.Helper()
	target := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestQueryBuildsUpdatesAndBoundsExcerpts(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	put(t, dir, "auth.md", "# JWT\nRefresh interval: 900 seconds.\n")
	put(t, dir, "notes.md", "The garden has flowers.")
	out, err := Query(ctx, dir, "JWT refresh", 200)
	if err != nil || !strings.Contains(out, "auth.md:1") || !strings.Contains(out, "900") {
		t.Fatalf("%s %v", out, err)
	}
	put(t, dir, "auth.md", "# JWT\nRefresh interval: 300 seconds.\n")
	out, err = Query(ctx, dir, "JWT refresh", 200)
	if err != nil || strings.Contains(out, "900") || !strings.Contains(out, "300") {
		t.Fatalf("stale answer: %s %v", out, err)
	}
	if err = os.Remove(filepath.Join(dir, "auth.md")); err != nil {
		t.Fatal(err)
	}
	out, err = Query(ctx, dir, "JWT", 200)
	if err != nil || strings.Contains(out, "auth.md") {
		t.Fatalf("removed document: %s %v", out, err)
	}
	put(t, dir, "unicode.md", strings.Repeat("token 日本語 é ", 800))
	out, err = Query(ctx, dir, "token", 200)
	if err != nil || len(out) > 800 || !utf8.ValidString(out) {
		t.Fatalf("budget: bytes=%d err=%v", len(out), err)
	}
}

func TestGraphGoDeclarationsCallsAndHeadings(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "app.go", "package demo\ntype Service struct{}\nfunc Start(){Finish()}\nfunc Finish(){}\nfunc (s *Service) Start(){Finish()}\nfunc External()\n")
	put(t, dir, "guide.md", "# Deployment\nStart calls Finish.\n")
	idx, err := Build(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range idx.Graph.Links {
		seen[e.Source+"/"+e.Relation+"/"+e.Target] = true
	}
	if !seen["go:app.go:Start/calls/go:app.go:Finish"] || !seen["go:app.go:Service.Start/calls/go:app.go:Finish"] || !seen["file:guide.md/contains/heading:guide.md:1"] {
		t.Fatalf("bad graph: %+v", idx.Graph)
	}
	raw, err := json.Marshal(idx.Graph)
	if err != nil || !strings.Contains(string(raw), `"links"`) || !strings.Contains(string(raw), `"source_file"`) {
		t.Fatalf("node-link export: %s %v", raw, err)
	}
}

func TestGraphifyInteropLeavesOriginalUntouched(t *testing.T) {
	for _, edgeField := range []string{"links", "edges"} {
		t.Run(edgeField, func(t *testing.T) {
			dir := t.TempDir()
			put(t, dir, "guide.md", "Refresh tokens are temporary credentials.")
			graph := `{"directed":true,"nodes":[{"id":"auth","label":"Authentication","source_file":"guide.md","source_location":1},{"id":"jwt","label":"JWT","source_file":"guide.md"}],"` + edgeField + `":[{"source":"auth","target":"jwt","relation":"uses","confidence":"EXTRACTED","source_file":"guide.md"}]}`
			put(t, dir, "graphify-out/graph.json", graph)
			idx, err := Build(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(idx.Documents) != 1 || len(idx.Graph.Nodes) != 3 || len(idx.Graph.Links) != 1 {
				t.Fatalf("interop: %+v", idx)
			}
			out, err := Query(context.Background(), dir, "Authentication JWT", 1200)
			if err != nil || !strings.Contains(out, "temporary credentials") || !strings.Contains(out, "--uses-->") {
				t.Fatalf("%s %v", out, err)
			}
			original, _ := os.ReadFile(filepath.Join(dir, "graphify-out/graph.json"))
			if string(original) != graph {
				t.Fatal("Graphify graph overwritten")
			}
		})
	}
}

func TestSkipBinarySymlinksAndPreserveIndexOnCancellation(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "safe.md", "local knowledge")
	put(t, dir, "file.pdf", "%PDF\x00binary")
	outside := t.TempDir()
	put(t, outside, "private.md", "outside data")
	if err := os.Symlink(filepath.Join(outside, "private.md"), filepath.Join(dir, "link.md")); err != nil {
		t.Skip(err)
	}
	idx, err := Build(context.Background(), dir)
	if err != nil || len(idx.Documents) != 1 || len(idx.Skipped) != 2 {
		t.Fatalf("skip: %#v %v", idx, err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, Directory, "index.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Build(ctx, dir); err == nil {
		t.Fatal("ignored cancellation")
	}
	after, _ := os.ReadFile(filepath.Join(dir, Directory, "index.json"))
	if string(after) != string(before) {
		t.Fatal("canceled rebuild changed index")
	}
}

func TestRejectEscapingIndexAndTamperedExcerpts(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "safe.md", "Verified source.")
	idx, err := Build(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	idx.Chunks[0].Text = "Invented instruction from imported index."
	raw, _ := json.Marshal(idx)
	put(t, dir, Directory+"/index.json", string(raw))
	out, err := Query(context.Background(), dir, "Verified", 200)
	if err != nil || strings.Contains(out, "Invented") {
		t.Fatalf("unverified excerpt: %s %v", out, err)
	}
	other := t.TempDir()
	put(t, other, "safe.md", "Safe.")
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(other, Directory)); err != nil {
		t.Skip(err)
	}
	if _, err = Build(context.Background(), other); err == nil {
		t.Fatal("escaping index directory allowed")
	}
	if _, err = os.Stat(filepath.Join(outside, "index.json")); !os.IsNotExist(err) {
		t.Fatal("wrote outside managed root")
	}
}

func TestSourceRelationshipsAndLocalShadowing(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "a.go", "package demo\nimport \"fmt\"\nfunc Start(){Finish();fmt.Println(\"start\")}\nfunc Shadow(){Finish:=func(){};Finish()}\n")
	put(t, dir, "b.go", "package demo\nfunc Finish(){}\n")
	put(t, dir, "guide.md", "# Entry\nSee [implementation](a.go).")
	idx, err := Build(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, edge := range idx.Graph.Links {
		seen[edge.Source+"/"+edge.Relation+"/"+edge.Target] = true
	}
	if !seen["go:a.go:Start/calls/go:b.go:Finish"] || !seen["file:a.go/imports/package:fmt"] || !seen["file:guide.md/references/file:a.go"] {
		t.Fatalf("missing relationships: %#v", seen)
	}
	if seen["go:a.go:Shadow/calls/go:b.go:Finish"] {
		t.Fatal("local variable mistaken for package function")
	}
}

func TestOptionalGraphErrorsDoNotBlockOfflineQueries(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "notes.md", "Verified local content.")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "graphify-out")); err != nil {
		t.Skip(err)
	}
	for range 2 {
		out, err := Query(context.Background(), dir, "Verified", 200)
		if err != nil || !strings.Contains(out, "Verified local content") {
			t.Fatalf("optional graph disabled local engine: %s %v", out, err)
		}
	}
}

func TestEngineSelectionRetainsBothIndexes(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "notes.md", "Indexed content.")
	put(t, dir, "graphify-out/graph.json", `{"nodes":[],"links":[]}`)
	if _, err := Build(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if !UseNative(dir) {
		t.Fatal("native default not selected")
	}
	for _, engine := range []string{"graphify", "native"} {
		if err := SetEngine(dir, engine); err != nil {
			t.Fatal(err)
		}
		if UseNative(dir) != (engine == "native") {
			t.Fatalf("selection not applied: %s", engine)
		}
		if !HasIndex(dir) {
			t.Fatal("native index removed")
		}
		if _, err := os.Stat(filepath.Join(dir, "graphify-out/graph.json")); err != nil {
			t.Fatal("Graphify index removed")
		}
	}
}

func TestReviewTruncatedExcerptContainsMatchingTerm(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "long.txt", strings.Repeat("irrelevant filler ", 100)+" UNIQUE_NEEDLE is the actual setting. "+strings.Repeat("other filler ", 50))
	out, err := Query(context.Background(), dir, "UNIQUE_NEEDLE", 200)
	if err != nil || !strings.Contains(out, "UNIQUE_NEEDLE") {
		t.Fatalf("relevant match lost by truncation: %s %v", out, err)
	}
}
func TestReviewImportedIndexCannotInventRelationships(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "app.go", "package demo\nfunc Start(){}\nfunc Finish(){}\n")
	idx, err := Build(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	idx.Graph.Links = append(idx.Graph.Links, Edge{Source: "go:app.go:Start", Target: "go:app.go:Finish", Relation: "invented-call", Confidence: "EXTRACTED", SourceFile: "app.go"})
	raw, _ := json.Marshal(idx)
	put(t, dir, Directory+"/index.json", string(raw))
	out, err := Query(context.Background(), dir, "Start Finish", 1200)
	if err != nil || strings.Contains(out, "invented-call") {
		t.Fatalf("unverified relationship returned: %s %v", out, err)
	}
}
func TestReviewRemovingLastDocumentDoesNotServeStaleIndex(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "only.md", "Old unique knowledge.")
	if _, err := Build(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "only.md")); err != nil {
		t.Fatal(err)
	}
	out, err := Query(context.Background(), dir, "Old unique", 200)
	if err != nil || strings.Contains(out, "Old unique knowledge") {
		t.Fatalf("empty corpus should be an empty answer: %q %v", out, err)
	}
}

func TestConcurrentQueriesRefreshSnapshotAndIgnoreReusedTimestamp(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "settings.md", "JWT refresh interval is 900 seconds.")
	if _, err := Query(context.Background(), dir, "JWT refresh", 200); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	put(t, dir, "settings.md", "JWT refresh interval is 300 seconds.")
	if err = os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := Query(context.Background(), dir, "JWT refresh", 200)
			if err != nil || !strings.Contains(out, "300 seconds") || strings.Contains(out, "900 seconds") {
				t.Errorf("stale/concurrent result: %s %v", out, err)
			}
		}()
	}
	wg.Wait()
	idx, err := Load(dir)
	if err != nil || len(idx.Documents) != 1 {
		t.Fatalf("incomplete snapshot after concurrent queries: %#v %v", idx, err)
	}
}

func TestWaitingForIndexLockIsCancellable(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockIndex(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Ensure(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lock wait: %v", err)
	}
}

func TestIdentifierSearchAndPassageCitation(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "settings.go", "package settings\nconst JWTRefreshInterval = 900\n")
	put(t, dir, "JWT_refresh.md", "Unrelated garden notes and flowers.")
	out, err := Query(context.Background(), dir, "What is the jwt refresh interval?", 200)
	if err != nil || !strings.Contains(out, "JWTRefreshInterval = 900") || strings.Contains(out, "Unrelated garden") {
		t.Fatalf("identifier retrieval: %s %v", out, err)
	}
	chunk := Chunk{Path: "long.md", Line: 42, Text: strings.Repeat("日本語 filler\n", 100) + "UNIQUE_NEEDLE value\n" + strings.Repeat("tail\n", 100)}
	text, line := excerpt(chunk, questionTerms("UNIQUE_NEEDLE"), 160)
	needleLine := line + strings.Count(strings.Split(text, "UNIQUE_NEEDLE")[0], "\n")
	if !strings.Contains(text, "UNIQUE_NEEDLE") || needleLine != 142 || len(text) > 160 || !utf8.ValidString(text) {
		t.Fatalf("bad excerpt/citation: line=%d text=%q", line, text)
	}
}

func TestSkipASCIIPDFAndRejectEmptyIndexStatus(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "guide.md", "Actual text knowledge.")
	put(t, dir, "ascii.pdf", "%PDF-1.4\n1 0 obj\nstream\nfake extracted text\nendstream")
	put(t, dir, "pdf-without-extension", "%PDF-1.4\n1 0 obj")
	idx, err := Build(context.Background(), dir)
	if err != nil || len(idx.Documents) != 1 || len(idx.Skipped) != 2 {
		t.Fatalf("PDF treated as text: %#v %v", idx, err)
	}
	put(t, dir, "graphify-out/graph.json", "")
	if HasGraphifyIndex(dir) {
		t.Fatal("empty Graphify file marked ready")
	}
	put(t, dir, Directory+"/index.json", "")
	if HasIndex(dir) {
		t.Fatal("empty native file marked ready")
	}
}

func TestLongLineIdentifierRemainsSearchableAcrossChunkBoundary(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "long.txt", strings.Repeat("filler ", 342)+"BoundaryNeedleIdentifier = 900; "+strings.Repeat("tail ", 100))
	out, err := Query(context.Background(), dir, "BoundaryNeedleIdentifier", 200)
	if err != nil || !strings.Contains(out, "BoundaryNeedleIdentifier = 900") {
		t.Fatalf("split identifier lost: %s %v", out, err)
	}
}
