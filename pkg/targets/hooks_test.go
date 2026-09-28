package targets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// scaffoldWorkpathWithHooks builds a tiny workpath in t.TempDir with
// the given hooks.json body. Returns the source dir.
func scaffoldWorkpathWithHooks(t *testing.T, hooksJSON string) string {
	t.Helper()
	dir := t.TempDir()
	must := func(rel, body string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("mission.md", "Hook emit test.\n")
	must("workpath.json", `{"description":"hooks emit"}`)
	must("hooks.json", hooksJSON)
	return dir
}

func TestClaudeTarget_EmitsHooksSettingsJSON(t *testing.T) {
	src := scaffoldWorkpathWithHooks(t, `{
  "hooks": [
    {"event": "pre_tool",      "matcher": "Bash",  "command": "echo before-bash"},
    {"event": "pre_tool",      "matcher": "Edit",  "command": "echo before-edit"},
    {"event": "session_start",                      "command": "scripts/setup.sh"},
    {"event": "session_stop",                       "command": "scripts/teardown.sh"}
  ]
}`)
	wp, err := workpath.LoadDir(src)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	out := t.TempDir()
	tgt, err := Get("claude")
	if err != nil {
		t.Fatalf("Get(claude): %v", err)
	}
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	settingsPath := filepath.Join(out, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher,omitempty"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}

	if got := len(settings.Hooks["PreToolUse"]); got != 2 {
		t.Errorf("PreToolUse entries = %d, want 2", got)
	}
	matchers := map[string]string{}
	for _, e := range settings.Hooks["PreToolUse"] {
		if len(e.Hooks) != 1 {
			t.Errorf("PreToolUse entry should have 1 inner hook, got %d", len(e.Hooks))
			continue
		}
		matchers[e.Matcher] = e.Hooks[0].Command
	}
	if matchers["Bash"] != "echo before-bash" {
		t.Errorf("Bash command = %q", matchers["Bash"])
	}
	if matchers["Edit"] != "echo before-edit" {
		t.Errorf("Edit command = %q", matchers["Edit"])
	}

	ss := settings.Hooks["SessionStart"]
	if len(ss) != 1 || ss[0].Matcher != "" || ss[0].Hooks[0].Command != "scripts/setup.sh" {
		t.Errorf("SessionStart = %+v", ss)
	}
	stop := settings.Hooks["Stop"]
	if len(stop) != 1 || stop[0].Hooks[0].Command != "scripts/teardown.sh" {
		t.Errorf("Stop = %+v", stop)
	}
}

func TestClaudeTarget_NoSettingsJSONWhenNoHooks(t *testing.T) {
	dir := t.TempDir()
	must := func(rel, body string) {
		full := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(body), 0o644)
	}
	must("mission.md", "nothing.\n")
	must("workpath.json", `{"description":"no hooks"}`)
	wp, err := workpath.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	out := t.TempDir()
	tgt, _ := Get("claude")
	if err := tgt.Compile(wp, out); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, ".claude", "settings.json")); err == nil {
		t.Errorf("settings.json should not be written when there are no hooks")
	}
}

func TestNonClaudeTarget_HookNoteAppended(t *testing.T) {
	src := scaffoldWorkpathWithHooks(t, `{
  "hooks": [
    {"event": "pre_tool", "matcher": "Bash", "command": "echo before-bash", "description": "audit Bash"}
  ]
}`)
	wp, err := workpath.LoadDir(src)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	for _, name := range []string{"codex", "generic"} {
		tgt, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		out := t.TempDir()
		if err := tgt.Compile(wp, out); err != nil {
			t.Fatalf("Compile %s: %v", name, err)
		}
		matches, _ := filepath.Glob(filepath.Join(out, "*.md"))
		var hit string
		for _, p := range matches {
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), "Hooks (declared, NOT wired") {
				hit = p
				break
			}
		}
		if hit == "" {
			t.Errorf("target=%s: no instruction file contained the hook note; checked %v", name, matches)
		}
	}
}
