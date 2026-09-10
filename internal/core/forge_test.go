package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

var forgeSkillNames = []string{"forge-context", "forge-simplicity", "forge-tdd", "forge-debugging", "forge-verification", "forge-review", "forge-security", "forge-release", "forge-handoff"}

func writeForgeKit(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	items := make([]forgeKitSkill, 0, len(forgeSkillNames))
	for _, name := range forgeSkillNames {
		dir := filepath.Join(root, "skills", "own", name)
		if err := os.MkdirAll(filepath.Join(dir, "references"), 0700); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: reviewed FORGE skill\n---\n" + strings.ToUpper(name) + " BODY"
		if name == "forge-review" {
			body += "\nNo subagents are claimed."
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "references", "evidence.md"), []byte("resource"), 0600); err != nil {
			t.Fatal(err)
		}
		items = append(items, forgeKitSkill{Name: name, Path: name + "/SKILL.md", Ref: "local/" + name})
	}
	index, err := json.Marshal(forgeKitIndex{Status: "original_local_skills_for_testing", Skills: items})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, forgeIndexFile), index, 0600); err != nil {
		t.Fatal(err)
	}
	workflows := []string{"Analizar", "Desarrollar", "Depurar", "Revisar", "Auditar seguridad", "Preparar deploy", "Guardar contexto"}
	var yaml strings.Builder
	yaml.WriteString("schema: praimate.agent/v1\nid: forge-dev\nname: FORGE baseline\ndescription: baseline\ninstructions: Preserve user work.\nsupports: [claude]\nsurfaces: [chat, editor]\nworkflows:\n")
	for _, name := range workflows {
		yaml.WriteString("  - name: " + name + "\n    steps:\n      - kind: user_message\n        template: original checklist that must not be copied\n")
	}
	if err := os.MkdirAll(filepath.Join(root, "forge", "current"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "forge", "current", "agent.yaml"), []byte(yaml.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestForgeOwnInstallRequiresExplicitReviewedTrust(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	if _, err := InstallForgeOwnSkills(context.Background(), writeForgeKit(t), false); err == nil || !strings.Contains(err.Error(), "trust_review_required") {
		t.Fatalf("unreviewed FORGE install = %v", err)
	}
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	view, err := host.View(context.Background())
	if err != nil || len(view.Versions) != 0 {
		t.Fatalf("unreviewed install mutated host: %+v, %v", view, err)
	}
}

func TestForgeOwnInstallBuildsPinnedReviewedLockWithoutExternalService(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	kit := writeForgeKit(t)
	result, err := InstallForgeOwnSkills(context.Background(), kit, true, reviewedForgeDigest(t, kit))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Versions) != len(forgeSkillNames) || len(result.Lock.Entries) != len(forgeSkillNames) {
		t.Fatalf("FORGE identities = %d versions, %d lock entries", len(result.Versions), len(result.Lock.Entries))
	}
	for _, version := range result.Versions {
		if version.SourceKind != "own" || version.SourceID != "own:"+strings.TrimPrefix(version.Ref, "local/") || !strings.HasPrefix(version.Digest, "sha256:") {
			t.Fatalf("FORGE provenance = %+v", version)
		}
	}
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	set, err := host.ResolveSkills(context.Background(), skills.SkillScopes{Session: skills.SkillScope{Config: &skills.SkillConfig{Schema: skills.SkillConfigSchema, Configured: true, Lockfile: "skills.lock.json", Enforcement: "controlled", Bindings: []skills.SkillBinding{{Ref: "local/forge-review", Activation: "auto"}}, Budget: forgeBudget()}, Lock: mustForgeLock(t, result.Lock)}}, skills.SkillResolutionPolicy{Budget: forgeBudget(), Transport: "controlled", RequireControlled: true})
	if err != nil || len(set.Bindings) != 1 || set.Bindings[0].Version.Ref != "local/forge-review" {
		t.Fatalf("review workflow selection = %+v, %v", set, err)
	}
}

func mustForgeLock(t *testing.T, lock *skills.SkillVersionLock) []byte {
	t.Helper()
	body, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestForgeAgentUsesOneWorkflowSkillAndDoesNotCopyChecklist(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSkillsV2RolloutState(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	kit := writeForgeKit(t)
	result, err := c.InstallForgeAgent(context.Background(), kit, true, reviewedForgeDigest(t, kit))
	if err != nil {
		t.Fatal(err)
	}
	if result.Agent == nil || result.Agent.Schema != AgentSchemaV2 || result.Agent.ID != "forge-dev-v2" {
		t.Fatalf("FORGE agent = %+v", result.Agent)
	}
	if result.Agent.Skills == nil || len(result.Agent.Skills.Bindings) != 3 || result.Agent.SkillsLock == nil || len(result.Agent.SkillsLock.Entries) != 3 {
		t.Fatalf("FORGE common skills = %+v, lock = %+v", result.Agent.Skills, result.Agent.SkillsLock)
	}
	for _, workflow := range result.Agent.Workflows {
		if workflow.Skills == nil || len(workflow.Skills.Bindings) != 1 || workflow.SkillsLock == nil || len(workflow.SkillsLock.Entries) != 1 {
			t.Fatalf("workflow %q selection = %+v", workflow.Name, workflow)
		}
		if strings.Contains(workflow.Steps[0].Template, "original checklist") {
			t.Fatalf("workflow %q duplicated baseline checklist", workflow.Name)
		}
	}
	if got, err := c.GetAgent(context.Background(), "forge-dev-v2"); err != nil || got.ID != result.Agent.ID {
		t.Fatalf("FORGE agent was not persisted: %+v, %v", got, err)
	}
	// FOR-01: a review delivers only its selected review body through the
	// controlled adapter path. The fake has no write/script capability.
	mock := &mockAdapter{name: "claude", replies: []string{"review complete"}}
	withMockAdapter(t, mock)
	workspace := t.TempDir()
	run := c.RunWorkflow(context.Background(), RunOptions{Agent: result.Agent, WorkflowName: "Revisar", CLI: "claude", Cwd: workspace})
	if run.Err != nil || len(mock.shots) != 1 || !strings.Contains(mock.shots[0].SystemPrompt, "FORGE-REVIEW BODY") {
		t.Fatalf("FORGE review delivery = %+v, shots=%+v", run, mock.shots)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("review workflow changed workspace: entries=%d err=%v", len(entries), err)
	}
}

func reviewedForgeDigest(t *testing.T, root string) string {
	t.Helper()
	preview, err := InspectForgeKit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return preview.ReviewDigest
}

func TestForgeChangedReviewDoesNotAuthorizeNewContent(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	kit := writeForgeKit(t)
	digest := reviewedForgeDigest(t, kit)
	path := filepath.Join(kit, "skills", "own", "forge-review", "references", "evidence.md")
	if err := os.WriteFile(path, []byte("changed after review"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallForgeOwnSkills(context.Background(), kit, true, digest); err == nil || !strings.Contains(err.Error(), "forge_review_changed") {
		t.Fatalf("changed review accepted: %v", err)
	}
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	view, err := host.View(context.Background())
	if err != nil || len(view.Versions) != 0 {
		t.Fatalf("changed review mutated host: %+v %v", view, err)
	}
}

func TestForgeAgentPackCarriesAndActivatesReviewedSkills(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	ctx := context.Background()
	source, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	kit := writeForgeKit(t)
	installed, err := source.InstallForgeAgent(ctx, kit, true, reviewedForgeDigest(t, kit))
	if err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(t.TempDir(), "forge.praimate-agent")
	if err := source.ExportAgentPack(ctx, installed.Agent.ID, pack); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	target, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	review, err := target.InspectAgentPack(ctx, pack)
	if err != nil || len(review.Skills) != len(installed.Versions) {
		t.Fatalf("FORGE pack inventory = %#v, %v", review, err)
	}
	agent, err := target.ImportReviewedAgentPack(ctx, pack, review.ReviewDigest)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := target.PreviewBoundSkills(ctx, agent, nil, ChatSettings{})
	if err != nil || len(resolved.Bindings) != 3 {
		t.Fatalf("plain FORGE selection = %#v, %v", resolved, err)
	}
	payload, materialized, configured, err := target.PrepareTerminalSkillFallback(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	defer materialized.Cleanup()
	if !configured || !strings.Contains(payload, "FORGE-CONTEXT BODY") || !strings.Contains(payload, "FORGE-SIMPLICITY BODY") || !strings.Contains(payload, "FORGE-VERIFICATION BODY") {
		t.Fatalf("FORGE terminal context was not automatic: %q", payload)
	}
}

func TestForgeRejectsSymlinkAncestor(t *testing.T) {
	kit := writeForgeKit(t)
	old := filepath.Join(kit, "skills")
	moved := filepath.Join(t.TempDir(), "skills")
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, old); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := InspectForgeKit(context.Background(), kit); err == nil {
		t.Fatal("symlinked kit ancestor accepted")
	}
}

// Opt-in local-kit test: real source bytes, isolated stores, fake transport.
// It does not execute a CLI, contact a model or approve the user's live store.
func TestForgeKitLocalAcceptance(t *testing.T) {
	kit := os.Getenv("PRAIMATE_FORGE_KIT")
	if kit == "" {
		t.Skip("not_run: set PRAIMATE_FORGE_KIT to test the local kit")
	}
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.SetSkillsV2RolloutState(ctx, true); err != nil {
		t.Fatal(err)
	}
	result, err := c.InstallForgeAgent(ctx, kit, true, reviewedForgeDigest(t, kit))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Versions) != 9 || len(result.Agent.Workflows) != 7 {
		t.Fatalf("unexpected kit: %+v", result)
	}
	mock := &mockAdapter{name: "claude", replies: []string{"synthetic review"}}
	withMockAdapter(t, mock)
	workflow := result.Agent.FindWorkflow("Revisar")
	inputs := map[string]string{}
	for _, input := range workflow.Inputs {
		inputs[input.Name] = "Synthetic local acceptance input"
	}
	run := c.RunWorkflow(ctx, RunOptions{Agent: result.Agent, WorkflowName: "Revisar", Inputs: inputs, CLI: "claude", Cwd: t.TempDir()})
	if run.Err != nil {
		t.Fatal(run.Err)
	}
	if len(mock.shots) != 1 || !strings.Contains(mock.shots[0].SystemPrompt, workflow.SkillsLock.Entries[0].Digest) {
		t.Fatal("actual kit selected digest not delivered")
	}
	if output := os.Getenv("PRAIMATE_FORGE_PACK_OUT"); output != "" {
		if !filepath.IsAbs(output) {
			t.Fatal("PRAIMATE_FORGE_PACK_OUT must be an absolute path")
		}
		if err := c.ExportAgentPack(ctx, result.Agent.ID, output); err != nil {
			t.Fatalf("export external FORGE agent pack: %v", err)
		}
		// The generated acceptance artifact must pass the public, generic
		// importer. This is the same route the Agents page invokes.
		t.Setenv("PRAIMATE_HOME", t.TempDir())
		target, err := New(Options{Store: openTempStore(t)})
		if err != nil {
			t.Fatal(err)
		}
		review, err := target.InspectAgentPack(ctx, output)
		if err != nil || len(review.Skills) != 9 {
			t.Fatalf("inspect external FORGE pack: %#v %v", review, err)
		}
		if _, err := target.ImportReviewedAgentPack(ctx, output, review.ReviewDigest); err != nil {
			t.Fatalf("generic reviewed import of FORGE pack: %v", err)
		}
	}
	t.Logf("real local kit: %d versions, %d workflows; exact review digest %s captured in fake payload", len(result.Versions), len(result.Agent.Workflows), workflow.SkillsLock.Entries[0].Digest)
}

func TestForgeMalformedBaselineDoesNotApproveSkills(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSkillsV2RolloutState(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	kit := writeForgeKit(t)
	path := filepath.Join(kit, "forge", "current", "agent.yaml")
	if err := os.WriteFile(path, []byte("schema: praimate.agent/v1\nid: forge\nname: forge\ndescription: fixture\ninstructions: preserve work\nsupports: [claude]\nsurfaces: [chat]\nworkflows:\n  - name: Unreviewed\n    steps:\n      - kind: user_message\n        template: fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstallForgeAgent(context.Background(), kit, true); err == nil || !strings.Contains(err.Error(), "no reviewed skill mapping") {
		t.Fatalf("malformed FORGE baseline = %v", err)
	}
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	view, err := host.View(context.Background())
	if err != nil || len(view.Versions) != len(BuiltinSkillCatalogue()) {
		t.Fatalf("malformed baseline mutated host: %+v, %v", view, err)
	}
	for _, version := range view.Versions {
		if version.SourceKind != "builtin" {
			t.Fatalf("malformed baseline installed FORGE skill: %+v", version)
		}
	}
}

func TestForgeExternalOriginalForkAndDelegationReviewRemainDistinct(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	ctx := context.Background()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	files := []skills.PackageFile{{Path: "SKILL.md", Content: []byte("---\nname: upstream-review\ndescription: review\nlicense: MIT\n---\nDelegate to a subagent.")}, {Path: "LICENSE", Content: []byte("MIT License")}}
	digest, err := skills.PackageDigest(files)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := skills.SelectPackages(ctx, mustCandidates(t, files), []skills.PackageSelection{{Candidate: 0, ExpectedDigest: digest}}, skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := host.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var original, fork skills.SkillVersion
	if _, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
		var err error
		original, err = tx.RegisterSource(ctx, "external:fixture", "external/review", selected[0], skills.SourceProvenance{Kind: "external", Origin: "https://example.com/upstream", ResolvedRevision: strings.Repeat("a", 40)})
		if err != nil {
			return err
		}
		d, err := tx.Fork(ctx, original.Ref, original.Digest, "local/forge-review-sequential")
		if err != nil {
			return err
		}
		revision, _ := d.Snapshot()
		fork, err = tx.Publish(ctx, d, revision)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if original.SourceID == fork.SourceID || fork.DerivedSource != original.SourceID || fork.DerivedDigest != original.Digest {
		t.Fatalf("original/fork provenance collapsed: original=%+v fork=%+v", original, fork)
	}
	_, exported, err := host.ReadVersion(ctx, fork.Ref, fork.Digest)
	if err != nil || !strings.Contains(string(exported[1].Content), "MIT") {
		t.Fatalf("fork did not preserve license: %v", err)
	}
	review := ReviewExternalSkill(files)
	if !review.RequiresExplicitFork || !strings.Contains(strings.Join(review.Diagnostics, " "), "delegation_not_supported") {
		t.Fatalf("delegating external review = %+v", review)
	}
}

func mustCandidates(t *testing.T, files []skills.PackageFile) []skills.PackageCandidate {
	t.Helper()
	root := t.TempDir()
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	candidates, _, err := skills.InspectPackageDirectory(context.Background(), root, skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return candidates
}
