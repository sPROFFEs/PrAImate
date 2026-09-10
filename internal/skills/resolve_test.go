package skills

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func resolutionFixture(t *testing.T) (*HostSkillStore, SkillVersion, SkillScope, SkillResolutionPolicy) {
	t.Helper()
	ctx := context.Background()
	s, err := OpenHostSkillStore(t.TempDir(), PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var v SkillVersion
	_, err = s.Update(ctx, 0, func(tx *SkillHostTransaction) error {
		v, err = tx.Publish(ctx, hostFixtureDraft(t), 1)
		if err != nil {
			return err
		}
		return tx.Approve(ctx, v.Ref, v.Digest, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.ExportLock(ctx, []SkillVersion{v})
	if err != nil {
		t.Fatal(err)
	}
	scope := SkillScope{Config: testSkillConfig(SkillBinding{Ref: v.Ref, Activation: "pinned"}), Lock: lock}
	return s, v, scope, SkillResolutionPolicy{Budget: testSkillBudget(), Transport: "controlled"}
}

func TestSkillResolutionScopePrecedenceAndHostIntersection(t *testing.T) {
	s, _, scope, policy := resolutionFixture(t)
	ctx := context.Background()
	project := SkillScope{Config: testSkillConfig(), Lock: scope.Lock}
	workflow := SkillScope{Config: cloneSkillConfig(scope.Config), Lock: scope.Lock}
	workflow.Config.Bindings[0].Activation = "manual"
	session := SkillScope{Config: cloneSkillConfig(scope.Config), Lock: scope.Lock}
	session.Config.Bindings[0].Activation = "auto"
	scopes := SkillScopes{Application: scope, Project: project, ProjectTrusted: true, Agent: scope, Workflow: workflow, Session: session}
	policy.Budget.CatalogTokens = 0
	policy.Budget.BodyTokens = 10
	policy.Budget.ResourceTokens = 0
	policy.Budget.MaxActive = 1
	result, err := s.ResolveSkills(ctx, scopes, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scope != "session" || len(result.Bindings) != 1 || result.Bindings[0].Binding.Activation != "auto" || result.Config.Budget.CatalogTokens != 0 || result.Config.Budget.ResourceTokens != 0 || result.Config.Budget.BodyTokens != 10 || result.Config.Budget.MaxActive != 1 {
		t.Fatalf("precedence/host limits failed: %+v", result)
	}
	if result.Legacy {
		t.Fatal("explicit config became legacy")
	}
	scopes.Session = SkillScope{}
	result, err = s.ResolveSkills(ctx, scopes, policy)
	if err != nil || result.Scope != "workflow" {
		t.Fatal("workflow precedence", err)
	}
	scopes.Workflow = SkillScope{}
	result, err = s.ResolveSkills(ctx, scopes, policy)
	if err != nil || result.Scope != "agent" {
		t.Fatal("agent precedence", err)
	}
	scopes.Agent = SkillScope{}
	result, err = s.ResolveSkills(ctx, scopes, policy)
	if err != nil || result.Scope != "project" || len(result.Bindings) != 0 {
		t.Fatal("trusted project empty lost", err)
	}
	scopes.ProjectTrusted = false
	result, err = s.ResolveSkills(ctx, scopes, policy)
	if err != nil || result.Scope != "application" || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "untrusted_source" {
		t.Fatal("untrusted project applied", err)
	}
	result, err = s.ResolveSkills(ctx, SkillScopes{Application: scope, Session: SkillScope{Config: testSkillConfig()}}, policy)
	if err != nil || len(result.Bindings) != 0 || result.Config.Bindings == nil {
		t.Fatal("explicit empty inherited defaults", err)
	}
	result, err = s.ResolveSkills(ctx, SkillScopes{}, SkillResolutionPolicy{})
	if err != nil || !result.Legacy {
		t.Fatal("legacy became implicitly configured", err)
	}
}

func TestSkillResolutionRequiredFailureIsAtomicAndOptionalDiagnosed(t *testing.T) {
	for _, scenario := range []string{"missing", "untrusted", "mismatch", "transport"} {
		t.Run(scenario, func(t *testing.T) {
			s, v, scope, policy := resolutionFixture(t)
			ctx := context.Background()
			revision := uint64(1)
			expectedCode := ""
			switch scenario {
			case "missing":
				lock, err := DecodeSkillVersionLock(scope.Lock)
				if err != nil {
					t.Fatal(err)
				}
				lock.Entries[0].Digest = "sha256:" + strings.Repeat("a", 64)
				scope.Lock, _ = json.Marshal(lock)
				expectedCode = "skill_not_found"
			case "untrusted":
				if _, err := s.Update(ctx, 1, func(tx *SkillHostTransaction) error { return tx.Approve(ctx, v.Ref, v.Digest, false) }); err != nil {
					t.Fatal(err)
				}
				revision = 2
				expectedCode = "untrusted_source"
			case "mismatch":
				lock, err := DecodeSkillVersionLock(scope.Lock)
				if err != nil {
					t.Fatal(err)
				}
				lock.Entries[0].SourceID = "other"
				lock.Entries[0].Source.Locator = "other"
				scope.Lock, _ = json.Marshal(lock)
				expectedCode = "integrity_mismatch"
			case "transport":
				policy.Transport = "compatible"
				expectedCode = "incompatible_transport"
			}
			result, err := s.ResolveAndHold(ctx, revision, "run", SkillScopes{Session: scope}, policy)
			var failure *SkillResolutionError
			if !errors.As(err, &failure) || len(failure.Diagnostics) != 1 || failure.Diagnostics[0].Code != expectedCode || len(result.Bindings) != 0 {
				t.Fatalf("required failure: %+v %v", result, err)
			}
			view, err := s.View(ctx)
			if err != nil || view.Revision != revision {
				t.Fatal("failed resolution committed state", err)
			}
			scope.Config.Bindings[0].Optional = true
			scope.Config.Bindings[0].Activation = "auto"
			result, err = s.ResolveSkills(ctx, SkillScopes{Session: scope}, policy)
			if err != nil || len(result.Bindings) != 0 || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != expectedCode {
				t.Fatalf("optional failure hidden: %+v %v", result, err)
			}
		})
	}
}

func TestSkillResolutionOffAndControlledPolicyCannotBeBypassed(t *testing.T) {
	s, v, scope, policy := resolutionFixture(t)
	ctx := context.Background()
	scope.Config.Enforcement = "compatible"
	policy.Transport = "compatible"
	policy.RequireControlled = true
	if _, err := s.ResolveSkills(ctx, SkillScopes{Session: scope}, policy); err == nil {
		t.Fatal("config weakened controlled host policy")
	}
	policy.RequireControlled = false
	policy.DeniedSources = map[string]bool{v.SourceID: true}
	if _, err := s.ResolveSkills(ctx, SkillScopes{Session: scope}, policy); err == nil {
		t.Fatal("approved version bypassed host deny")
	}
	scope.Config.Bindings[0].Activation = "off"
	scope.Lock = nil
	result, err := s.ResolveSkills(ctx, SkillScopes{Session: scope}, policy)
	if err != nil || len(result.Bindings) != 0 {
		t.Fatal("off binding resolved or loaded", err)
	}
}

func TestSkillResolutionUsesStableSnapshotsAndRetainsResolvedVersions(t *testing.T) {
	s, v, scope, policy := resolutionFixture(t)
	ctx := context.Background()
	first, err := s.ResolveSkills(ctx, SkillScopes{Session: scope}, policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ResolveSkills(ctx, SkillScopes{Session: scope}, policy)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("equivalent scopes disagree", err)
	}
	first.Config.Bindings[0].Activation = "off"
	first.Bindings[0].Version.SourceID = "mutated"
	third, err := s.ResolveSkills(ctx, SkillScopes{Session: scope}, policy)
	if err != nil || !reflect.DeepEqual(second, third) {
		t.Fatal("result aliases internal state", err)
	}
	result, err := s.ResolveAndHold(ctx, 1, "chat:one", SkillScopes{Session: scope}, policy)
	if err != nil || result.HostRevision != 2 {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, 2, func(tx *SkillHostTransaction) error { return tx.Forget(v.Ref, v.Digest) }); err == nil {
		t.Fatal("resolved pin not retained")
	}
	if _, err := s.ResolveAndHold(ctx, 1, "chat:two", SkillScopes{Session: scope}, policy); err == nil {
		t.Fatal("stale preview committed")
	}
}
