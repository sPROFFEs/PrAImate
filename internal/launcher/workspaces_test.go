package launcher

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// samplesDir returns the absolute path to the repo's bundled samples,
// regardless of where `go test` is invoked from.
func samplesDir(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// internal/launcher → up two = repo root.
	return filepath.Join(cwd, "..", "..", "samples", "workpaths")
}

func TestSeedSamples_SeedsIntoTemplatesDir(t *testing.T) {
	src := samplesDir(t)
	if _, err := os.Stat(src); err != nil {
		t.Skipf("no samples dir at %s: %v", src, err)
	}

	root := t.TempDir()
	seeded, err := SeedSamples(root, []string{src})
	if err != nil {
		t.Fatalf("SeedSamples: %v", err)
	}
	sort.Strings(seeded)
	if want := []string{"code-review", "reversing", "workpath-author"}; !equalStrings(seeded, want) {
		t.Errorf("seeded = %v, want %v", seeded, want)
	}

	// New layout: samples land under <root>/templates/<name>/workpath/.
	got, err := ListTemplates(root)
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListTemplates returned %d, want 3", len(got))
	}
	for _, tpl := range got {
		if _, err := os.Stat(tpl.WorkpathDir); err != nil {
			t.Errorf("workpath dir missing for %s: %v", tpl.Name, err)
		}
		if tpl.Description == "" {
			t.Errorf("template %s has empty description", tpl.Name)
		}
	}
	imports, err := readTemplateImports(root, "code-review")
	if err != nil {
		t.Fatalf("read code-review imports: %v", err)
	}
	if !containsString(imports, "_common/scrapegraph") {
		t.Errorf("code-review imports = %v, want _common/scrapegraph example bundle active", imports)
	}

	bundles, err := DiscoverBundles(root)
	if err != nil {
		t.Fatalf("DiscoverBundles: %v", err)
	}
	var bundleNames []string
	for _, b := range bundles {
		bundleNames = append(bundleNames, b.Name)
	}
	sort.Strings(bundleNames)
	if want := []string{"gstack", "scrapegraph"}; !equalStrings(bundleNames, want) {
		t.Errorf("bundles = %v, want %v", bundleNames, want)
	}
	if _, err := os.Stat(filepath.Join(root, TemplatesDir, "_common", "scrapegraph", "tools", "scrapegraph_search.sh")); err != nil {
		t.Errorf("scrapegraph bundle tool missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, TemplatesDir, "_common", "workpath")); !os.IsNotExist(err) {
		t.Errorf("_common should not be seeded as a template workpath, stat err=%v", err)
	}
}

func TestSeedSamples_SkipsExisting(t *testing.T) {
	src := samplesDir(t)
	if _, err := os.Stat(src); err != nil {
		t.Skipf("no samples dir at %s", src)
	}
	root := t.TempDir()
	if _, err := SeedSamples(root, []string{src}); err != nil {
		t.Fatalf("seed1: %v", err)
	}
	// Mutate a seeded file to detect clobbering on a re-seed.
	mark := filepath.Join(root, TemplatesDir, "reversing", "workpath", "mission.md")
	if err := os.WriteFile(mark, []byte("MUTATED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seeded, err := SeedSamples(root, []string{src})
	if err != nil {
		t.Fatalf("seed2: %v", err)
	}
	if len(seeded) != 0 {
		t.Errorf("second seed should be a no-op, got %v", seeded)
	}
	raw, _ := os.ReadFile(mark)
	if string(raw) != "MUTATED\n" {
		t.Error("second seed clobbered an existing template")
	}
}

func TestListTemplates_IgnoresHiddenAndNonWorkpathDirs(t *testing.T) {
	root := t.TempDir()
	tplRoot := filepath.Join(root, TemplatesDir)
	// real one
	must(t, os.MkdirAll(filepath.Join(tplRoot, "good", "workpath"), 0o755))
	must(t, os.WriteFile(filepath.Join(tplRoot, "good", "workpath", "mission.md"), []byte("# good\n"), 0o644))
	// hidden
	must(t, os.MkdirAll(filepath.Join(tplRoot, ".hidden", "workpath"), 0o755))
	// non-workpath dir
	must(t, os.MkdirAll(filepath.Join(tplRoot, "stray"), 0o755))

	got, err := ListTemplates(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("got %+v, want exactly [good]", got)
	}
}

func TestCreateWorkspace_ScaffoldsValidWorkpath(t *testing.T) {
	root := t.TempDir()
	ws, err := CreateWorkspace(root, "my-new-thing", "describes what this does")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	for _, f := range []string{"workpath.json", "mission.md", "playbook.md", "rules.md"} {
		if _, err := os.Stat(filepath.Join(ws.WorkpathDir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(ws.SandboxDir); err != nil {
		t.Errorf("sandbox not created: %v", err)
	}
	// Sandbox should be gitignored by default.
	gi, err := os.ReadFile(filepath.Join(ws.SandboxDir, ".gitignore"))
	if err != nil {
		t.Fatalf("read sandbox .gitignore: %v", err)
	}
	if !strings.Contains(string(gi), "*") {
		t.Errorf("sandbox .gitignore should ignore everything; got %q", gi)
	}
}

func TestCreateWorkspace_RejectsBadName(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"", "Bad-Caps", "_leading-underscore", "spaces here", "-leading"} {
		if _, err := CreateWorkspace(root, bad, "x"); err == nil {
			t.Errorf("CreateWorkspace(%q) should have failed", bad)
		}
	}
}

func TestCreateWorkspace_RefusesDuplicate(t *testing.T) {
	root := t.TempDir()
	if _, err := CreateWorkspace(root, "dup", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateWorkspace(root, "dup", "second"); err == nil {
		t.Error("second create should have failed")
	}
}

func TestEnsureSandbox_Idempotent(t *testing.T) {
	root := t.TempDir()
	ws, err := CreateWorkspace(root, "ws1", "x")
	if err != nil {
		t.Fatal(err)
	}
	// Write a marker file inside the sandbox.
	mark := filepath.Join(ws.SandboxDir, "mark")
	if err := os.WriteFile(mark, []byte("present"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Re-ensure — must not delete the marker.
	if err := EnsureSandbox(ws); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Errorf("EnsureSandbox wiped existing files: %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func readTemplateImports(root, name string) ([]string, error) {
	return workpath.ReadImports(filepath.Join(root, TemplatesDir, name, "workpath", "workpath.json"))
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
