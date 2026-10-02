package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestMarkdownAgentRoundTripPreservesExternalOptions(t *testing.T) {
	source := `---
description: Review without edits
mode: subagent
model: anthropic/claude-sonnet-4
temperature: 0.1
permission:
  edit: deny
  bash:
    '*': ask
    'git diff': allow
tools:
  write: false
custom_option: [one, two]
---

# Review
Check correctness and cite evidence.
`
	a, err := ParseAgentMarkdown(strings.NewReader(source), "code-review.md")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "code-review" || a.Instructions != "# Review\nCheck correctness and cite evidence." || len(a.Tools) != 0 {
		t.Fatalf("bad conversion: %#v", a)
	}
	raw, err := MarshalAgentYAML(a)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseAgentYAML(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	md, err := MarshalAgentMarkdown(b)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseAgentMarkdown(bytes.NewReader(md), "renamed.md")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Foreign, c.Foreign) || c.ID != a.ID || c.Instructions != a.Instructions {
		t.Fatalf("lost metadata: %s", md)
	}
}

func TestMarkdownAgentNativeMetadataAndEditedBody(t *testing.T) {
	a := &Agent{Schema: AgentSchemaV2, ID: "review", Name: "Review", Instructions: "old prompt", Supports: []string{"codex"}, Knowledge: "rag", Surfaces: []string{"chat"}, Workflows: []Workflow{{Name: "check", Steps: []WorkflowStep{{Kind: StepUserMessage, Template: "Review {{.project}}"}}}}}
	md, err := MarshalAgentMarkdown(a)
	if err != nil {
		t.Fatal(err)
	}
	md = bytes.Replace(md, []byte("old prompt"), []byte("new prompt"), 1)
	b, err := ParseAgentMarkdown(bytes.NewReader(md), "export.md")
	if err != nil {
		t.Fatal(err)
	}
	if b.Instructions != "new prompt" || b.Schema != AgentSchemaV2 || b.Knowledge != "rag" || !reflect.DeepEqual(a.Workflows, b.Workflows) || !reflect.DeepEqual(a.Supports, b.Supports) {
		t.Fatalf("lost native metadata: %#v", b)
	}
}

func TestMarkdownAgentValidation(t *testing.T) {
	for _, body := range []string{"---\nmodel: test\nbody", "---\n- invalid\n---\nprompt", "---\nmodel: a\nmodel: b\n---\nprompt", "---\nname: test\n---\n", string([]byte{0xff}), strings.Repeat("a", (4<<20)+1)} {
		if _, err := ParseAgentMarkdown(strings.NewReader(body), "test.md"); err == nil {
			t.Fatalf("accepted invalid input: %.80s", body)
		}
	}
	a, err := ParseAgentMarkdown(strings.NewReader("\ufeff---\r\nname: Writer\r\n---\r\nWrite docs.\r\n"), "test.md")
	if err != nil || a.ID != "writer" {
		t.Fatalf("BOM/CRLF: %#v %v", a, err)
	}
}

func TestMarkdownAgentDatabaseAndPackRoundTrip(t *testing.T) {
	withTempConfigDir(t)
	ctx := context.Background()
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "review.md")
	if err := os.WriteFile(path, []byte("---\nmodel: local/qwen\npermission:\n  bash: deny\n---\nReview files."), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := c.ImportAgentAuto(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list {
		if item.ID == a.ID {
			found = item.Foreign["model"] == "local/qwen"
		}
	}
	if !found {
		t.Fatal("list lost foreign metadata")
	}
	yamlPath := filepath.Join(t.TempDir(), "review.yaml")
	if err = c.ExportAgent(ctx, a.ID, yamlPath); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(t.TempDir(), "review.praimate-agent")
	if err = c.ExportAgentPack(ctx, a.ID, pack); err != nil {
		t.Fatal(err)
	}
	other, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := other.ImportAgentPack(ctx, pack)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Foreign, b.Foreign) {
		t.Fatalf("pack lost metadata: %#v", b.Foreign)
	}
	mdPath := filepath.Join(t.TempDir(), "review.md")
	if err = other.ExportAgent(ctx, b.ID, mdPath); err != nil {
		t.Fatal(err)
	}
	exported, err := LoadAgentFile(mdPath)
	if err != nil || !reflect.DeepEqual(a.Foreign, exported.Foreign) {
		t.Fatalf("export: %#v %v", exported, err)
	}
}

func TestManagedNativeKnowledgeQueryWithoutGraphify(t *testing.T) {
	withTempConfigDir(t)
	ctx := context.Background()
	agent := &Agent{ID: "local-rag", Knowledge: "rag"}
	dir, _ := AgentKnowledgeDir(agent.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.md"), []byte("# Authentication\nJWT refresh tokens expire after 900 seconds."), 0o600); err != nil {
		t.Fatal(err)
	}
	broker, err := newManagedToolBroker(ctx, agent, AgentCapabilities{}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	if err = validateManagedKnowledge(agent); err != nil {
		t.Fatal(err)
	}
	out, err := broker.ExecuteTool(ctx, "knowledge.query", []byte(`{"question":"JWT refresh","budget":200}`))
	if err != nil || !strings.Contains(out, "auth.md:1") || !strings.Contains(out, "900") {
		t.Fatalf("query: %s %v", out, err)
	}
	files, err := ListAgentKnowledge(agent.ID)
	if err != nil || len(files) != 1 || files[0] != "auth.md" {
		t.Fatalf("index exposed as document: %v %v", files, err)
	}
	if !strings.Contains(AgentSystemPrompt(agent), "praimate knowledge query") {
		t.Fatal("native CLI fallback missing")
	}
}

func TestMarkdownReviewDeletedOptionsStayDeleted(t *testing.T) {
	a, err := ParseAgentMarkdown(strings.NewReader("---\nmodel: local/test\npermission:\n  bash: deny\n---\nReview."), "review.md")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalAgentMarkdown(a)
	if err != nil {
		t.Fatal(err)
	}
	// Delete the public frontmatter setting without editing the native block.
	raw = bytes.Replace(raw, []byte("model: local/test\n"), nil, 1)
	b, err := ParseAgentMarkdown(bytes.NewReader(raw), "review.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := b.Foreign["model"]; exists {
		t.Fatalf("deleted option resurrected: %#v", b.Foreign)
	}
}

func TestMarkdownForeignNamespaceAndDatesSurviveConversion(t *testing.T) {
	source := "---\nmode: all\nreleased: 2026-10-02\npraimate:\n  custom: enabled\n---\nReview files."
	a, err := ParseAgentMarkdown(strings.NewReader(source), "review.md")
	if err != nil {
		t.Fatal(err)
	}
	yml, err := MarshalAgentYAML(a)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseAgentYAML(bytes.NewReader(yml))
	if err != nil {
		t.Fatal(err)
	}
	md, err := MarshalAgentMarkdown(b)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseAgentMarkdown(bytes.NewReader(md), "renamed.md")
	if err != nil || !reflect.DeepEqual(a.Foreign, c.Foreign) || c.Foreign["released"] != "2026-10-02" {
		t.Fatalf("foreign metadata changed: %#v %v", c, err)
	}
}

func TestMarkdownRejectsMetadataThatCannotBeStored(t *testing.T) {
	for _, source := range []string{"---\ncustom:\n  true: yes\n---\nReview.", "---\ntemperature: .inf\n---\nReview."} {
		if _, err := ParseAgentMarkdown(strings.NewReader(source), "review.md"); err == nil || !strings.Contains(err.Error(), "JSON-compatible") {
			t.Fatalf("invalid metadata was accepted: %v", err)
		}
	}
}

func TestMarkdownFrontmatterDelimiterInsideBlockScalar(t *testing.T) {
	source := "---\ndescription: |\n  A divider:\n  ---\nmode: subagent\n---\nReview files."
	a, err := ParseAgentMarkdown(strings.NewReader(source), "review.md")
	if err != nil || a.Instructions != "Review files." || !strings.Contains(a.Description, "\n---\n") {
		t.Fatalf("indented divider ended frontmatter: %#v %v", a, err)
	}
}

func TestManagedKnowledgeFallsBackWhenGraphifyQueryFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	withTempConfigDir(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "graphify"), []byte("#!/bin/sh\nprintf 'broken graph index' >&2\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	agent := &Agent{ID: "fallback-rag", Knowledge: "rag"}
	dir, _ := AgentKnowledgeDir(agent.ID)
	if err := os.MkdirAll(filepath.Join(dir, "graphify-out"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "graphify-out", "graph.json"), []byte(`{"nodes":[],"links":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "guide.md"), []byte("JWT refresh interval is 900 seconds."), 0o600); err != nil {
		t.Fatal(err)
	}
	broker, err := newManagedToolBroker(context.Background(), agent, AgentCapabilities{}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	out, err := broker.ExecuteTool(context.Background(), "knowledge.query", []byte(`{"question":"JWT refresh","budget":200}`))
	if err != nil || !strings.Contains(out, "900 seconds") || !strings.Contains(out, "Graphify query failed") || len(out) > 800 {
		t.Fatalf("fallback: %s %v", out, err)
	}
}
