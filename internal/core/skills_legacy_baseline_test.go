package core

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"
)

// P0 characterization: these assertions describe legacy behavior, including
// limitations. They must not become claims about v2 delivery or native reads.
func TestSkillsLegacyImportBaseline(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{
		{"collection/a/SKILL.md", "FIRST"},
		{"collection/b/SKILL.md", "SECOND"},
		{"collection/a/references/detail.md", "REFERENCE"},
	} {
		entry, err := w.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := concatMarkdownFromZipBytes(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	want := "<!-- collection/a/SKILL.md -->\n\nFIRST\n\n---\n\n<!-- collection/a/references/detail.md -->\n\nREFERENCE\n\n---\n\n<!-- collection/b/SKILL.md -->\n\nSECOND"
	if got != want {
		t.Fatalf("legacy import changed: %q", got)
	}
}

func TestSkillsLegacyFirstTurnAndResumeBaseline(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	mock := &mockAdapter{name: "skills-baseline", resumable: true, replies: []string{"first", "second"}}
	withMockAdapter(t, mock)
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cwd := t.TempDir()
	chat, err := c.StartCleanChat(ctx, mock.name, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	skill := Skill{ID: "baseline", Name: "Baseline", Body: "SYNTHETIC_SKILL_BODY_A"}
	if _, err := AddUserSkill(skill); err != nil {
		t.Fatal(err)
	}
	prefix := ResolveSkillsPrefix([]string{"missing", "baseline"})
	if prefix != "# Skill: Baseline\n\nSYNTHETIC_SKILL_BODY_A" {
		t.Fatalf("prefix = %q", prefix)
	}
	if _, err := c.ContinueChat(ctx, chat.ID, "first", cwd, prefix); err != nil {
		t.Fatal(err)
	}
	if len(mock.shots) != 1 || !strings.Contains(mock.shots[0].SystemPrompt, skill.Body) {
		t.Fatal("first adapter payload does not contain the selected body")
	}
	skill.Body = "SYNTHETIC_SKILL_BODY_B"
	if _, err := AddUserSkill(skill); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContinueChat(ctx, chat.ID, "second", cwd, ResolveSkillsPrefix([]string{"baseline"})); err != nil {
		t.Fatal(err)
	}
	if len(mock.resumes) != 1 || mock.resumes[0].Message != "second" {
		t.Fatalf("legacy resume changed: %+v", mock.resumes)
	}
	// ResumeOpts has no system prompt: the changed body is not redelivered.
}
