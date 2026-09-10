package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

type nativeSkillFakeAdapter struct {
	mockAdapter
	caps NativeSkillCapabilities
}

func (a *nativeSkillFakeAdapter) NativeSkillCapabilities() NativeSkillCapabilities { return a.caps }

func nativeSkillFixture(t *testing.T) (skills.ResolvedSkillSet, skills.SkillVersion) {
	t.Helper()
	_, agent, version := v2AgentFixture(t)
	ctx := context.Background()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := host.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
		return tx.Approve(ctx, version.Ref, version.Digest, true)
	}); err != nil {
		t.Fatal(err)
	}
	_ = host.Close()
	scope, err := skills.SelectionScope(agent.Skills, agent.SkillsLock)
	if err != nil {
		t.Fatal(err)
	}
	// Native delivery is a compatible transport, unlike P4's controlled
	// payload.  Keep the same pinned lock and only change the explicit policy.
	scope.Config.Enforcement = "compatible"
	set, err := ResolveNativeSkillSet(ctx, skills.SkillScopes{Agent: scope})
	if err != nil {
		t.Fatal(err)
	}
	return set, version
}

func TestNativeMaterializationExposesOnlySelectedPinnedSkillToFakeCLI(t *testing.T) {
	set, version := nativeSkillFixture(t)
	adapter := &nativeSkillFakeAdapter{mockAdapter: mockAdapter{name: "fake-native"}, caps: NativeSkillCapabilities{NativeSkillDiscovery: true, ScopedSkillRoot: true, StrictSelectedSet: true}}
	m, err := MaterializeNativeSkills(context.Background(), adapter, set, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Cleanup() }()
	manifest, err := os.ReadFile(m.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), version.Ref) || !strings.Contains(string(manifest), version.Digest) {
		t.Fatalf("manifest does not prove selected pinned skill: %s", manifest)
	}
	entries, err := os.ReadDir(filepath.Join(m.Root, "skills"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("native root entries = %d, err=%v", len(entries), err)
	}
	if got := m.Env["PRAIMATE_SKILL_ROOT"]; got != m.Root || m.ReadState != "unknown" {
		t.Fatalf("native launch evidence = %#v, read=%q", m.Env, m.ReadState)
	}
	if !m.RequiresNewSession || m.HotUpdate {
		t.Fatalf("non-hot adapter update state = %+v", m)
	}
	if len(m.Receipt.Delivered) != 1 || m.Receipt.Delivered[0].Ref != version.Ref {
		t.Fatalf("materialization receipt = %+v", m.Receipt)
	}
}

func TestNativeMaterializationDoesNotUseInlinePayloadOrClaimReads(t *testing.T) {
	set, _ := nativeSkillFixture(t)
	adapter := &nativeSkillFakeAdapter{mockAdapter: mockAdapter{name: "fake-native"}, caps: NativeSkillCapabilities{NativeSkillDiscovery: true, ScopedSkillRoot: true, StrictSelectedSet: true}}
	m, err := MaterializeNativeSkills(context.Background(), adapter, set, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Cleanup() }()
	if m.Env["PRAIMATE_SKILL_ROOT"] == "" || m.Env["PRAIMATE_SKILL_MANIFEST"] == "" {
		t.Fatal("missing native transport environment")
	}
	if _, inline := m.Env["PRAIMATE_SKILL_PAYLOAD"]; inline {
		t.Fatal("native delivery exposed an inline payload transport")
	}
	if m.Receipt.Coverage != skills.CoverageUnknown || m.ReadState != "unknown" {
		t.Fatalf("unobservable native state claimed evidence: %+v %q", m.Receipt, m.ReadState)
	}
}

func TestNativeMaterializationFailsClosedForGlobalDiscoveryAndControlledConfig(t *testing.T) {
	set, _ := nativeSkillFixture(t)
	adapter := &nativeSkillFakeAdapter{mockAdapter: mockAdapter{name: "fake-native"}, caps: NativeSkillCapabilities{NativeSkillDiscovery: true, ScopedSkillRoot: true}}
	if _, err := MaterializeNativeSkills(context.Background(), adapter, set, true); err == nil || !strings.Contains(err.Error(), "strict") {
		t.Fatalf("strict global discovery = %v, want block", err)
	}
	set.Config.Enforcement = "controlled"
	adapter.caps.StrictSelectedSet = true
	if _, err := MaterializeNativeSkills(context.Background(), adapter, set, false); err == nil || !strings.Contains(err.Error(), "compatible") {
		t.Fatalf("controlled config over native = %v, want incompatible", err)
	}
}

func TestNativeMaterializationPreservesModifiedOwnedFileOnCleanup(t *testing.T) {
	set, _ := nativeSkillFixture(t)
	adapter := &nativeSkillFakeAdapter{mockAdapter: mockAdapter{name: "fake-native"}, caps: NativeSkillCapabilities{NativeSkillDiscovery: true, ScopedSkillRoot: true}}
	m, err := MaterializeNativeSkills(context.Background(), adapter, set, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Manifest, []byte("user change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(); err == nil || !strings.Contains(err.Error(), "preserved") {
		t.Fatalf("cleanup = %v, want preservation", err)
	}
	if _, err := os.Stat(m.Root); err != nil {
		t.Fatalf("modified root was deleted: %v", err)
	}
	_ = os.RemoveAll(m.Root)
}

func TestNativeMaterializationPreservesAddedFileOnCleanup(t *testing.T) {
	set, _ := nativeSkillFixture(t)
	adapter := &nativeSkillFakeAdapter{mockAdapter: mockAdapter{name: "fake-native"}, caps: NativeSkillCapabilities{NativeSkillDiscovery: true, ScopedSkillRoot: true}}
	m, err := MaterializeNativeSkills(context.Background(), adapter, set, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Root, "user-note.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(); err == nil || !strings.Contains(err.Error(), "was added") {
		t.Fatalf("cleanup = %v, want preservation", err)
	}
	_ = os.RemoveAll(m.Root)
}

func TestNativeMaterializationCleanupIsIdempotent(t *testing.T) {
	set, _ := nativeSkillFixture(t)
	adapter := &nativeSkillFakeAdapter{mockAdapter: mockAdapter{name: "fake-native"}, caps: NativeSkillCapabilities{NativeSkillDiscovery: true, ScopedSkillRoot: true}}
	m, err := MaterializeNativeSkills(context.Background(), adapter, set, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(); err != nil {
		t.Fatalf("second cleanup = %v", err)
	}
}

func TestNativeSkillLeasePreventsDifferentSelectionInSameProject(t *testing.T) {
	cwd := t.TempDir()
	release, err := acquireNativeSkillLease(cwd, "selection-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := acquireNativeSkillLease(cwd, "selection-b"); err == nil || !strings.Contains(err.Error(), "lease_conflict") {
		t.Fatalf("parallel selection = %v, want collision", err)
	}
	release()
	if _, err := acquireNativeSkillLease(cwd, "selection-b"); err != nil {
		t.Fatalf("lease was not released: %v", err)
	}
}

func TestNativeSkillCapabilityMustBeExplicit(t *testing.T) {
	set, _ := nativeSkillFixture(t)
	_, err := MaterializeNativeSkills(context.Background(), &mockAdapter{name: "name-is-not-capability"}, set, false)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unmarked adapter error = %v", err)
	}
}

func TestPrepareTerminalSkillFallbackLoadsReviewedPinnedAgentSkills(t *testing.T) {
	c, agent, version := v2AgentFixture(t)
	ctx := context.Background()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := host.View(ctx)
	if err == nil {
		_, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
			return tx.Approve(ctx, version.Ref, version.Digest, true)
		})
	}
	host.Close()
	if err != nil {
		t.Fatal(err)
	}
	payload, materialized, configured, err := c.PrepareTerminalSkillFallback(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if !configured || materialized == nil || !strings.Contains(payload, "EXACT INSTRUCTIONS") || !strings.Contains(payload, materialized.Manifest) {
		t.Fatalf("terminal fallback missing reviewed content: configured=%v materialized=%#v payload=%q", configured, materialized, payload)
	}
	if _, err := os.Stat(materialized.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := materialized.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareTerminalSkillFallbackForSettingsUsesFrozenChatSelection(t *testing.T) {
	c, agent, version := v2AgentFixture(t)
	ctx := context.Background()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := host.View(ctx)
	if err == nil {
		_, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
			return tx.Approve(ctx, version.Ref, version.Digest, true)
		})
	}
	host.Close()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := skills.SelectionScope(agent.Skills, agent.SkillsLock)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := skills.DecodeSkillVersionLock(scope.Lock)
	if err != nil {
		t.Fatal(err)
	}
	settings := ChatSettings{SkillsV2: scope.Config, SkillsLock: &lock}
	payload, materialized, configured, err := c.PrepareTerminalSkillFallbackForSettings(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if !configured || materialized == nil || !strings.Contains(payload, "EXACT INSTRUCTIONS") {
		t.Fatalf("frozen terminal settings were not delivered: configured=%v materialized=%#v payload=%q", configured, materialized, payload)
	}
	if err := materialized.Cleanup(); err != nil {
		t.Fatal(err)
	}
}
