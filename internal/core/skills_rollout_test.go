package core

import (
	"context"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

func TestSkillsV2RolloutDefaultsOffAndDoesNotModifyLegacyChat(t *testing.T) {
	ctx := context.Background()
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	state, err := c.SkillsV2RolloutState(ctx)
	if err != nil || state.Enabled {
		t.Fatalf("initial rollout = %+v, %v", state, err)
	}
	mock := &mockAdapter{name: "claude", replies: []string{"legacy reply"}}
	withMockAdapter(t, mock)
	chat, err := c.CreateChat(ctx, CreateChatRequest{ID: "legacy-rollout", CLIAgent: "claude", WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContinueChat(ctx, chat.ID, "legacy request", chat.WorkspacePath, "legacy system"); err != nil {
		t.Fatal(err)
	}
	if len(mock.shots) != 1 || strings.Contains(mock.shots[0].SystemPrompt, "praimate-skills") {
		t.Fatalf("legacy transport changed while v2 off: %+v", mock.shots)
	}
}

func TestSkillsV2EnableSeedsApprovedBuiltinsIdempotently(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	ctx := context.Background()
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSkillsV2RolloutState(ctx, true); err != nil {
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
	view, err := host.View(ctx)
	if err != nil || len(view.Versions) != len(BuiltinSkillCatalogue()) {
		t.Fatalf("built-in registry = %#v, %v", view, err)
	}
	_, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
		for _, version := range view.Versions {
			resolved, approved, err := tx.ResolveVersion(ctx, version.Ref, version.Digest)
			if err != nil || !approved || resolved.SourceKind != "builtin" {
				t.Fatalf("built-in was not approved with builtin provenance: %#v %v", resolved, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSkillsV2RolloutRejectsWithoutFallbackAndReenablesPinnedChat(t *testing.T) {
	c, agent, version := v2AgentFixture(t)
	ctx := context.Background()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	view, err := host.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
		return tx.Approve(ctx, version.Ref, version.Digest, true)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.upsertAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	mock := &mockAdapter{name: "claude", replies: []string{"v2 reply"}}
	withMockAdapter(t, mock)
	chat, err := c.StartInteractiveChat(ctx, agent.ID, "claude", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSkillsV2RolloutState(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContinueChat(ctx, chat.ID, "must not fall back", chat.WorkspacePath, "agent system"); err == nil || !strings.Contains(err.Error(), "skills_v2_disabled") {
		t.Fatalf("disabled v2 turn = %v", err)
	}
	if len(mock.shots) != 0 {
		t.Fatalf("disabled v2 reached adapter: %+v", mock.shots)
	}
	persisted, err := c.GetChat(ctx, chat.ID)
	if err != nil || persisted.Settings.SkillsLock == nil || persisted.Settings.SkillsLock.Entries[0].Digest != version.Digest {
		t.Fatalf("rollback changed pinned chat: %+v, %v", persisted, err)
	}
	if err := c.SetSkillsV2RolloutState(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContinueChat(ctx, chat.ID, "run after rollback", chat.WorkspacePath, "agent system"); err != nil {
		t.Fatal(err)
	}
	if len(mock.shots) != 1 || !strings.Contains(mock.shots[0].SystemPrompt, version.Digest) {
		t.Fatalf("reenabled v2 did not send exact pinned payload: %+v", mock.shots)
	}
}

func TestSkillsV2DisabledAlsoBlocksInheritedDefaultPreflight(t *testing.T) {
	c, agent, v := v2AgentFixture(t)
	approveRuntimeFixture(t, v)
	ctx := context.Background()
	if err := c.SetSkillDefaultsV2(ctx, agent.Skills, agent.SkillsLock); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSkillsV2RolloutState(ctx, false); err != nil {
		t.Fatal(err)
	}
	_, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{CLI: "claude", Cwd: t.TempDir(), Surface: SurfaceStudio})
	if err == nil || !strings.Contains(err.Error(), "skills_v2_disabled") {
		t.Fatalf("inherited defaults bypassed rollout: %v", err)
	}
}
