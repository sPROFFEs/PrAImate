package targets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// loadByo loads the shared fixture used by every target test.
func loadByo(t *testing.T) *workpath.Workpath {
	t.Helper()
	wp, err := workpath.LoadDir(filepath.Join("..", "..", "testdata", "byo"))
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if err := workpath.Validate(wp); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return wp
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func TestRegistry_HasAllTargets(t *testing.T) {
	want := []string{"claude", "codex", "cursor", "generic", "mika"}
	got := Names()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Names() = %v, want %v", got, want)
	}
}

func TestClaude_EmitsSkillAndAgent(t *testing.T) {
	wp := loadByo(t)
	out := t.TempDir()
	tgt, err := Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	skillMD := filepath.Join(out, ".claude", "skills", "byo", "SKILL.md")
	body := mustRead(t, skillMD)
	if !strings.HasPrefix(body, "---\n") {
		t.Error("SKILL.md should start with YAML frontmatter")
	}
	if !strings.Contains(body, "name: byo") {
		t.Error("SKILL.md should declare name: byo")
	}
	if !strings.Contains(body, "description: Full-shape fixture") {
		t.Error("SKILL.md should carry description")
	}
	if !strings.Contains(body, "Exercise every loader code path") {
		t.Error("SKILL.md should include mission body verbatim")
	}

	// Scripts copied alongside.
	if _, err := os.Stat(filepath.Join(out, ".claude", "skills", "byo", "scripts", "greet.sh")); err != nil {
		t.Errorf("greet.sh missing: %v", err)
	}

	// Subagent emitted with namespaced name.
	agentMD := filepath.Join(out, ".claude", "agents", "byo__helper.md")
	abody := mustRead(t, agentMD)
	if !strings.Contains(abody, "name: byo__helper") {
		t.Error("agent file should declare namespaced name")
	}
}

func TestMika_EmitsModuleLayout(t *testing.T) {
	wp := loadByo(t)
	out := t.TempDir()
	tgt, err := Get("mika")
	if err != nil {
		t.Fatal(err)
	}
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	root := filepath.Join(out, "modules", "byo")
	for _, f := range []string{"module.md", "playbook.md", "rules.md", "tools/greet.sh", "tools/count.ps1", "agents/helper.md"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}

	module := mustRead(t, filepath.Join(root, "module.md"))
	if !strings.HasPrefix(module, "# byo\n") {
		t.Error("module.md should open with H1 of workpath name")
	}
	if !strings.Contains(module, "> Full-shape fixture") {
		t.Error("module.md should include description as blockquote")
	}
}

func TestCursor_EmitsRule(t *testing.T) {
	wp := loadByo(t)
	out := t.TempDir()
	tgt, err := Get("cursor")
	if err != nil {
		t.Fatal(err)
	}
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	rule := mustRead(t, filepath.Join(out, ".cursor", "rules", "byo.mdc"))
	if !strings.Contains(rule, "alwaysApply: false") {
		t.Error("cursor rule should set alwaysApply")
	}
	if !strings.Contains(rule, "## Tools") {
		t.Error("cursor rule should inline tool list")
	}
}

func TestGeneric_EmitsSingleFile(t *testing.T) {
	wp := loadByo(t)
	out := t.TempDir()
	tgt, err := Get("generic")
	if err != nil {
		t.Fatal(err)
	}
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	body := mustRead(t, filepath.Join(out, "byo.md"))
	if !strings.HasPrefix(body, "# byo\n") {
		t.Error("generic should start with workpath name as H1")
	}
	if !strings.Contains(body, "_Version 2 · MIT_") {
		t.Errorf("generic should include version+license; got: %q", body[:200])
	}
	if _, err := os.Stat(filepath.Join(out, "byo.assets", "tools", "greet.sh")); err != nil {
		t.Errorf("generic assets dir missing greet.sh: %v", err)
	}
}

func TestCodex_EmitsAgentsMD(t *testing.T) {
	wp := loadByo(t)
	out := t.TempDir()
	tgt, err := Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	body := mustRead(t, filepath.Join(out, "AGENTS.md"))
	if !strings.HasPrefix(body, "# byo\n") {
		t.Error("AGENTS.md should start with workpath name as H1")
	}
	if !strings.Contains(body, "## Tools") {
		t.Error("AGENTS.md should inline tool list")
	}
	if !strings.Contains(body, "AGENTS.assets/tools/") {
		t.Error("AGENTS.md should reference assets dir for tools")
	}
	if _, err := os.Stat(filepath.Join(out, "AGENTS.assets", "tools", "greet.sh")); err != nil {
		t.Errorf("assets dir missing greet.sh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "AGENTS.assets", "agents", "helper.md")); err != nil {
		t.Errorf("assets dir missing helper.md: %v", err)
	}
}

func TestGemini_EmitsGeminiMD(t *testing.T) {
	t.Skip("Gemini CLI support has been removed")
	wp := loadByo(t)
	out := t.TempDir()
	tgt, err := Get("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	body := mustRead(t, filepath.Join(out, "GEMINI.md"))
	if !strings.HasPrefix(body, "# byo\n") {
		t.Error("GEMINI.md should open with workpath H1")
	}
	if !strings.Contains(body, "GEMINI.assets/tools/") {
		t.Error("GEMINI.md should reference assets dir for tools")
	}
	if _, err := os.Stat(filepath.Join(out, "GEMINI.assets", "tools", "greet.sh")); err != nil {
		t.Errorf("assets dir missing greet.sh: %v", err)
	}
}

func TestAll_Compiles(t *testing.T) {
	wp := loadByo(t)
	out := t.TempDir()
	for _, tgt := range All() {
		if err := tgt.Compile(wp, out); err != nil {
			t.Errorf("%s: %v", tgt.Name(), err)
		}
	}
}

func TestTargets_PlatformPairedScriptsAllCopied(t *testing.T) {
	// Build a tiny workpath on the fly with foo.sh + foo.ps1 paired.
	src := t.TempDir()
	tools := filepath.Join(src, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "mission.md"),
		[]byte("# pair\n\nPaired-scripts test mission.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "foo.sh"),
		[]byte("#!/bin/sh\n# foo bash\necho hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "foo.ps1"),
		[]byte("# foo powershell\nWrite-Host hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wp, err := workpath.LoadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := workpath.Validate(wp); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"claude", "codex", "generic", "mika"} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			tgt, _ := Get(name)
			if err := tgt.Compile(wp, out); err != nil {
				t.Fatalf("Compile: %v", err)
			}
			// Both scripts must land in the output — at SOME path under out.
			shFound, ps1Found := false, false
			_ = filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if strings.HasSuffix(p, "foo.sh") {
					shFound = true
				}
				if strings.HasSuffix(p, "foo.ps1") {
					ps1Found = true
				}
				return nil
			})
			if !shFound || !ps1Found {
				t.Errorf("%s target: shFound=%v ps1Found=%v — both should be copied", name, shFound, ps1Found)
			}
		})
	}
}

func TestKebab(t *testing.T) {
	cases := map[string]string{
		"foo_bar":    "foo-bar",
		"FOO BAR":    "foo-bar",
		"already-ok": "already-ok",
	}
	for in, want := range cases {
		if got := kebab(in); got != want {
			t.Errorf("kebab(%q) = %q, want %q", in, got, want)
		}
	}
}

// buildKnowledgeWorkpath constructs a minimal workpath in a tempdir
// with a knowledge/ tree, then loads it through LoadDir so the
// resulting *workpath.Workpath is shaped exactly as the production
// loader would shape it. We don't pollute testdata/ — tempdir gets
// cleaned by t.TempDir's deferred removal.
func buildKnowledgeWorkpath(t *testing.T) *workpath.Workpath {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"mission.md":              "# kn-fixture\n\nExercise the knowledge plumbing.",
		"knowledge/notes.md":      "# Field Notes\n\nbackground reading about the domain.",
		"knowledge/datasheet.pdf": "%PDF-1.4",
	}
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wp, err := workpath.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	return wp
}

func TestCodex_StagesKnowledgeAndManifest(t *testing.T) {
	wp := buildKnowledgeWorkpath(t)
	out := t.TempDir()
	tgt, err := Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Both knowledge files should land at <out>/knowledge/<file>.
	if _, err := os.Stat(filepath.Join(out, "knowledge", "notes.md")); err != nil {
		t.Errorf("knowledge/notes.md not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "knowledge", "datasheet.pdf")); err != nil {
		t.Errorf("knowledge/datasheet.pdf not copied: %v", err)
	}

	// AGENTS.md should advertise both files in a "Knowledge base"
	// section. The PDF gets a (binary) annotation; the .md gets a
	// title + summary.
	body := mustRead(t, filepath.Join(out, "AGENTS.md"))
	if !strings.Contains(body, "## Knowledge base") {
		t.Error("AGENTS.md missing Knowledge base section")
	}
	if !strings.Contains(body, "knowledge/notes.md") {
		t.Error("AGENTS.md missing notes.md entry")
	}
	if !strings.Contains(body, "Field Notes") {
		t.Error("AGENTS.md missing title from manifest")
	}
	if !strings.Contains(body, "binary") {
		t.Error("AGENTS.md should mark PDF as (binary)")
	}
}

func TestClaude_StagesKnowledgeAndManifest(t *testing.T) {
	wp := buildKnowledgeWorkpath(t)
	out := t.TempDir()
	tgt, _ := Get("claude")
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "knowledge", "notes.md")); err != nil {
		t.Errorf("knowledge/notes.md not copied: %v", err)
	}
	// SKILL.md lives under <out>/.claude/skills/<kebab-name>/. The
	// fixture's name is derived from the random tempdir, so look up
	// the actual file rather than hard-coding "byo".
	skillDir := filepath.Join(out, ".claude", "skills", kebab(wp.Name))
	skill := mustRead(t, filepath.Join(skillDir, "SKILL.md"))
	if !strings.Contains(skill, "## Knowledge base") {
		t.Error("SKILL.md missing Knowledge base section")
	}
}

func TestGemini_StagesKnowledgeAndManifest(t *testing.T) {
	t.Skip("Gemini CLI support has been removed")
	wp := buildKnowledgeWorkpath(t)
	out := t.TempDir()
	tgt, _ := Get("gemini")
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "knowledge", "notes.md")); err != nil {
		t.Errorf("knowledge/notes.md not copied: %v", err)
	}
	gem := mustRead(t, filepath.Join(out, "GEMINI.md"))
	if !strings.Contains(gem, "## Knowledge base") {
		t.Error("GEMINI.md missing Knowledge base section")
	}
}
