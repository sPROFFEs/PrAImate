package core

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"git.jtsec.local/lab/PrAImate/internal/skills"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func v2AgentFixture(t *testing.T) (*Core, *Agent, skills.SkillVersion) {
	t.Helper()
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	ctx := context.Background()
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSkillsV2RolloutState(ctx, true); err != nil {
		t.Fatal(err)
	}
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	draft, err := skills.NewSkillDraft("own-v2", "local/example", []skills.PackageFile{{Path: "SKILL.md", Content: []byte("---\nname: example\ndescription: Example\n---\nEXACT INSTRUCTIONS")}, {Path: "LICENSE", Content: []byte("TEST LICENSE")}, {Path: "scripts/check.sh", Content: []byte("echo never executed"), Executable: true}}, skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	var version skills.SkillVersion
	view, err := host.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error { version, err = tx.Publish(ctx, draft, 1); return err })
	if err != nil {
		t.Fatal(err)
	}
	wire, err := host.ExportLock(ctx, []skills.SkillVersion{version})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := skills.DecodeSkillVersionLock(wire)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &skills.SkillConfig{Schema: skills.SkillConfigSchema, Configured: true, Lockfile: "skills.lock.json", Enforcement: "controlled", Bindings: []skills.SkillBinding{{Ref: version.Ref, Activation: "pinned"}}, Budget: skills.SkillBudget{CatalogTokens: 100, BodyTokens: 200, ResourceTokens: 100, TotalTokens: 300, MaxActive: 2, MaxLoadCallsPerTurn: 2, MaxResourceReadsPerTurn: 2, MaxLoadedBytesFallback: 4096}}
	a := &Agent{Schema: AgentSchemaV2, ID: "v2-agent", Name: "V2", Instructions: "Test", Supports: []string{"claude"}, Skills: cfg, SkillsLock: &lock, Workflows: []Workflow{{Name: "run", Steps: []WorkflowStep{{Kind: StepUserMessage, Template: "hello"}}}}}
	return c, a, version
}

func TestAgentV2YAMLDBAndChatSnapshots(t *testing.T) {
	c, a, v := v2AgentFixture(t)
	ctx := context.Background()
	body, err := MarshalAgentYAML(a)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := c.ImportAgentYAML(ctx, body, "")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Schema != AgentSchemaV2 || !reflect.DeepEqual(stored.Skills, a.Skills) || !reflect.DeepEqual(stored.SkillsLock, a.SkillsLock) {
		t.Fatal("v2 YAML/DB lost bindings")
	}
	first, err := c.StartInteractiveChat(ctx, a.ID, "claude", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	empty := *a.Skills
	empty.Bindings = []skills.SkillBinding{}
	emptyLock := skills.SkillVersionLock{Schema: "praimate.skills-lock/v1", DigestAlgorithm: "sha256-tree-v1", Entries: []skills.SkillVersionLockEntry{}}
	a.Skills = &empty
	a.SkillsLock = &emptyLock
	if _, err := c.upsertAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	reopened, err := c.GetChat(ctx, first.ID)
	if err != nil || reopened.Settings.SkillsLock.Entries[0].Digest != v.Digest {
		t.Fatal("agent edit changed existing chat", err)
	}
	second, err := c.CreateChat(ctx, CreateChatRequest{ID: "second-v2", AgentID: a.ID, CLIAgent: "claude", WorkspacePath: t.TempDir()})
	if err != nil || second.Settings.SkillsV2 == nil || len(second.Settings.SkillsV2.Bindings) != 0 {
		t.Fatal("new chat lost explicit none", err)
	}
	if err := c.UpdateChatSettings(ctx, second.ID, func(s *ChatSettings) { s.Skills = []string{"legacy"} }); err == nil {
		t.Fatal("mixed legacy/v2 accepted")
	}
	if _, err := ParseAgentYAMLForSchema(bytes.NewReader(body), AgentSchema); err == nil {
		t.Fatal("v1 reader accepted v2")
	}
	for _, bad := range [][]byte{bytes.Replace(body, []byte(AgentSchemaV2), []byte(AgentSchema), 1), append(append([]byte(nil), body...), []byte("trusted: true\n")...)} {
		if _, err := ParseAgentYAML(bytes.NewReader(bad)); err == nil {
			t.Fatal("v1 downgrade or critical unknown field ignored")
		}
	}
}

func TestAgentV2WorkflowLocksRoundTripWithDistinctPaths(t *testing.T) {
	c, a, _ := v2AgentFixture(t)
	ctx := context.Background()
	workflowConfig := *a.Skills
	workflowConfig.Lockfile = "skill-locks/run.json"
	a.Workflows[0].Skills = &workflowConfig
	a.Workflows[0].SkillsLock = a.SkillsLock
	body, err := MarshalAgentYAML(a)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAgentYAML(bytes.NewReader(body))
	if err != nil || parsed.Workflows[0].Skills == nil || parsed.Workflows[0].Skills.Lockfile != "skill-locks/run.json" {
		t.Fatalf("workflow lock round-trip: %v", err)
	}
	if _, err := c.upsertAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(t.TempDir(), "workflow"+AgentPackExt)
	if err := c.ExportAgentPack(ctx, a.ID, pack); err != nil {
		t.Fatal(err)
	}
	files, _, err := readAgentPackFiles(ctx, pack)
	if err != nil {
		t.Fatal(err)
	}
	hasRoot, hasWorkflow := false, false
	for _, f := range files {
		hasRoot = hasRoot || f.Path == "skills.lock.json"
		hasWorkflow = hasWorkflow || f.Path == "skill-locks/run.json"
	}
	if !hasRoot || !hasWorkflow {
		t.Fatal("pack omitted scoped locks")
	}
}

func TestSkillDefaultsDoNotRetroactivelyChangeSavedSessions(t *testing.T) {
	c, a, _ := v2AgentFixture(t)
	ctx := context.Background()
	chat, err := c.CreateChat(ctx, CreateChatRequest{ID: "legacy-before-default", CLIAgent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSkillDefaultsV2(ctx, a.Skills, a.SkillsLock); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{CLI: "claude", Surface: SurfaceChat}); err == nil || !strings.Contains(err.Error(), "untrusted_source") {
		t.Fatalf("new execution did not apply defaults: %v", err)
	}
	// A saved legacy run must not inherit defaults introduced after creation.
	_, err = c.ResolveExecutionConfig(ctx, ExecutionRequest{CLI: "claude", Surface: SurfaceChat, SkillSettings: &ChatSettings{}, skillSnapshot: true})
	if err != nil && strings.Contains(err.Error(), "untrusted_source") {
		t.Fatalf("saved run inherited current defaults: %v", err)
	}
	_, err = c.ResolveExecutionConfig(ctx, ExecutionRequest{CLI: "claude", Surface: SurfaceChat, ChatID: chat.ID})
	if err != nil && strings.Contains(err.Error(), "untrusted_source") {
		t.Fatalf("saved chat inherited current defaults: %v", err)
	}
	newChat, err := c.CreateChat(ctx, CreateChatRequest{ID: "v2-after-default", CLIAgent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(newChat.Settings.SkillsV2, a.Skills) {
		t.Fatal("new chat did not snapshot defaults")
	}
	if err := c.SetSkillDefaultsV2(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	saved, err := c.GetChat(ctx, newChat.ID)
	if err != nil || !reflect.DeepEqual(saved.Settings.SkillsV2, a.Skills) {
		t.Fatalf("saved selection changed: %v", err)
	}
}

func TestAgentV2PackRoundTripPreservesBundlesWithoutAuthority(t *testing.T) {
	c, a, v := v2AgentFixture(t)
	ctx := context.Background()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := host.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error { return tx.Approve(ctx, v.Ref, v.Digest, true) })
	host.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.upsertAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(t.TempDir(), "agent.praimate-agent")
	if err := c.ExportAgentPack(ctx, a.ID, pack); err != nil {
		t.Fatal(err)
	}
	files, _, err := readAgentPackFiles(ctx, pack)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.Contains(f.Path, "local-state") || strings.Contains(f.Path, "host-head") {
			t.Fatal("host authority exported")
		}
		if f.Path == "skill-bundles.json" && (bytes.Contains(f.Content, []byte("approved")) || bytes.Contains(f.Content, []byte("generation"))) {
			t.Fatal("private metadata exported")
		}
	}
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	other, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetSkillsV2RolloutState(ctx, true); err != nil {
		t.Fatal(err)
	}
	imported, err := other.ImportAgentPack(ctx, pack)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(imported.Skills, a.Skills) || !reflect.DeepEqual(imported.SkillsLock, a.SkillsLock) {
		t.Fatal("pack lost selection")
	}
	target, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	_, resources, err := target.ReadVersion(ctx, v.Ref, v.Digest)
	if err != nil {
		t.Fatal(err)
	}
	license, script := false, false
	for _, f := range resources {
		license = license || (f.Path == "LICENSE" && string(f.Content) == "TEST LICENSE")
		script = script || (f.Path == "scripts/check.sh" && f.Executable)
	}
	if !license || !script {
		t.Fatal("license/resources changed")
	}
	_, err = other.PreviewBoundSkills(ctx, imported, nil, ChatSettings{})
	var failure *skills.SkillResolutionError
	if !errors.As(err, &failure) || failure.Diagnostics[0].Code != "untrusted_source" {
		t.Fatal("pack granted trust", err)
	}
	// A poisoned inventory is rejected before replacing the existing agent.
	var poisoned bytes.Buffer
	w := zip.NewWriter(&poisoned)
	for _, f := range files {
		content := f.Content
		if f.Path == "skill-bundles.json" {
			var obj map[string]any
			_ = json.Unmarshal(content, &obj)
			obj["trusted"] = true
			content, _ = json.Marshal(obj)
		}
		entry, err := w.Create(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.zip")
	if err := os.WriteFile(bad, poisoned.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ImportAgentPack(ctx, bad); err == nil {
		t.Fatal("poisoned pack accepted")
	}
	after, err := other.GetAgent(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(after.SkillsLock, imported.SkillsLock) {
		t.Fatal("failed import changed existing agent", err)
	}
	// A valid-looking outer pack missing the exact locked object also leaves the
	// installed agent untouched before any folder or DB swap.
	var missing bytes.Buffer
	missingZip := zip.NewWriter(&missing)
	for _, file := range files {
		if strings.HasPrefix(file.Path, "skills/") {
			continue
		}
		entry, createErr := missingZip.Create(file.Path)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, createErr = entry.Write(file.Content); createErr != nil {
			t.Fatal(createErr)
		}
	}
	if err := missingZip.Close(); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(t.TempDir(), "missing.zip")
	if err := os.WriteFile(missingPath, missing.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ImportAgentPack(ctx, missingPath); err == nil {
		t.Fatal("missing locked bundle accepted")
	}
	after, err = other.GetAgent(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(after.SkillsLock, imported.SkillsLock) {
		t.Fatalf("missing-bundle import changed agent: %v", err)
	}
}

func TestReviewedAgentPackImportApprovesExactBundledSkills(t *testing.T) {
	c, a, version := v2AgentFixture(t)
	ctx := context.Background()
	if _, err := c.upsertAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(t.TempDir(), "reviewed.praimate-agent")
	if err := c.ExportAgentPack(ctx, a.ID, pack); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	target, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	review, err := target.InspectAgentPack(ctx, pack)
	if err != nil || review.ReviewDigest == "" || len(review.Skills) != 1 || review.Skills[0].Digest != version.Digest {
		t.Fatalf("inspect reviewed pack: %#v %v", review, err)
	}
	if _, err := target.ImportReviewedAgentPack(ctx, pack, "sha256:wrong"); err == nil || !strings.Contains(err.Error(), "review_changed") {
		t.Fatalf("changed review accepted: %v", err)
	}
	imported, err := target.ImportReviewedAgentPack(ctx, pack, review.ReviewDigest)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := target.SkillsV2RolloutState(ctx); err != nil || !state.Enabled {
		t.Fatalf("reviewed v2 agent import did not enable skills: %+v %v", state, err)
	}
	preview, err := target.PreviewBoundSkills(ctx, imported, nil, ChatSettings{})
	if err != nil || len(preview.Bindings) != 1 {
		t.Fatalf("reviewed import did not activate locked skill: %#v %v", preview, err)
	}
}

func TestBoundSkillsBlockActualEntryPointsBeforeCLI(t *testing.T) {
	c, a, _ := v2AgentFixture(t)
	ctx := context.Background()
	mock := &mockAdapter{name: "claude"}
	withMockAdapter(t, mock)
	if _, err := c.upsertAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	chat, err := c.StartInteractiveChat(ctx, a.ID, "claude", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContinueChat(ctx, chat.ID, "hello", chat.WorkspacePath, ""); err == nil {
		t.Fatal("chat bypassed skill gate")
	}
	messages, err := c.ListMessages(ctx, chat.ID, 0)
	if err != nil || len(messages) != 0 {
		t.Fatal("blocked turn persisted message", err)
	}
	for _, surface := range []ExecutionSurface{SurfaceChat, SurfaceStudio, SurfaceTerminal, SurfaceWorkflow} {
		_, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{Surface: surface, Agent: a, CLI: "claude", Cwd: t.TempDir()})
		var failure *skills.SkillResolutionError
		if !errors.As(err, &failure) || failure.Diagnostics[0].Code != "untrusted_source" {
			t.Fatalf("%s bypassed common resolver: %v", surface, err)
		}
	}
	result := c.RunWorkflow(ctx, RunOptions{Agent: a, WorkflowName: "run", CLI: "claude", Cwd: t.TempDir()})
	if result.Err == nil {
		t.Fatal("workflow bypassed skill gate")
	}
	if _, err := c.RunManagedAgent(ctx, ManagedRunRequest{Agent: a, CLI: "claude", Cwd: t.TempDir(), Task: "hello"}); err == nil {
		t.Fatal("managed run bypassed skill gate")
	}
	if len(mock.shots) != 0 || len(mock.resumes) != 0 {
		t.Fatal("CLI called before skill resolution")
	}
}
