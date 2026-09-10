package skills

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func runtimeFixture(t *testing.T, bindings []SkillBinding, files map[string][]PackageFile, budget SkillBudget) (*Runtime, []ResolvedSkillBinding) {
	t.Helper()
	resolved := make([]ResolvedSkillBinding, 0, len(bindings))
	for i, binding := range bindings {
		resolved = append(resolved, ResolvedSkillBinding{Binding: binding, Version: SkillVersion{Ref: binding.Ref, Digest: "sha256:" + string([]byte("0000000000000000000000000000000000000000000000000000000000000000")), SourceID: "source-" + string(rune('a'+i))}})
	}
	// Replace fixed test digests with valid distinct SHA-256 strings.
	for i := range resolved {
		resolved[i].Version.Digest = "sha256:000000000000000000000000000000000000000000000000000000000000000" + string(rune('0'+i))
	}
	set := ResolvedSkillSet{Config: &SkillConfig{Schema: SkillConfigSchema, Configured: true, Lockfile: "skills.lock.json", Enforcement: "controlled", Bindings: bindings, Budget: budget}, Bindings: resolved}
	reader := func(_ context.Context, ref, digest string) (SkillVersion, []PackageFile, error) {
		for _, b := range resolved {
			if b.Version.Ref == ref && b.Version.Digest == digest {
				return b.Version, files[ref], nil
			}
		}
		return SkillVersion{}, nil, context.Canceled
	}
	r, err := NewRuntime(set, ContextBudget{InputLimit: 10000, ModelWindow: 12000, ReservedOutput: 1000, SafetyMargin: 100, NonSkillInput: 100, Coverage: CoverageControlledPayload, Measurement: MeasurementEstimate}, reader)
	if err != nil {
		t.Fatal(err)
	}
	return r, resolved
}
func runtimeBudget() SkillBudget {
	return SkillBudget{CatalogTokens: 1000, BodyTokens: 4000, ResourceTokens: 2000, TotalTokens: 6000, MaxActive: 3, MaxLoadCallsPerTurn: 4, MaxResourceReadsPerTurn: 6, MaxLoadedBytesFallback: 16384}
}
func testSkill(body string) []PackageFile {
	return []PackageFile{{Path: "SKILL.md", Content: []byte("---\nname: verify\ndescription: check a result\n---\n" + body)}, {Path: "guide.md", Content: []byte("one\ntwo\nthree\nfour")}}
}

func TestRuntimeBuildLoadReadResidenceAndCompact(t *testing.T) {
	bindings := []SkillBinding{{Ref: "own/verify", Activation: "pinned"}, {Ref: "own/catalogue", Activation: "auto"}}
	r, versions := runtimeFixture(t, bindings, map[string][]PackageFile{"own/verify": testSkill("EXACT BODY"), "own/catalogue": testSkill("AUTO BODY")}, runtimeBudget())
	plan, err := r.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blocks) != 2 || plan.Receipt.Coverage != CoverageControlledPayload {
		t.Fatalf("initial plan: %+v", plan)
	}
	if plan.Blocks[0].Text == "AUTO BODY" || plan.Blocks[1].Text == "AUTO BODY" {
		t.Fatal("auto body was delivered")
	}
	again, err := r.Load(context.Background(), versions[0].Version.Ref, versions[0].Version.Digest)
	if err != nil || !again.Receipt.Existing {
		t.Fatalf("residency: %+v %v", again, err)
	}
	chunk, err := r.Read(context.Background(), versions[0].Version.Ref, versions[0].Version.Digest, "guide.md", 2, 2)
	if err != nil || chunk.Text != "two\nthree" || chunk.NextCursor != "4" {
		t.Fatalf("read: %+v %v", chunk, err)
	}
	r.Compact()
	reloaded, err := r.Load(context.Background(), versions[0].Version.Ref, versions[0].Version.Digest)
	if err != nil || reloaded.Receipt.Existing || reloaded.Receipt.ContextEpoch != 2 {
		t.Fatalf("compact reload: %+v %v", reloaded, err)
	}
}
func TestRuntimeRejectsOutsidePathsAndAtomicBudgetReservation(t *testing.T) {
	b := runtimeBudget()
	b.BodyTokens = 2
	b.TotalTokens = 2
	r, versions := runtimeFixture(t, []SkillBinding{{Ref: "own/a", Activation: "manual"}}, map[string][]PackageFile{"own/a": testSkill("a body that cannot fit")}, b)
	if _, err := r.Load(context.Background(), versions[0].Version.Ref, versions[0].Version.Digest); err == nil {
		t.Fatal("over-budget body loaded")
	}
	if len(r.Blocks()) != 0 {
		t.Fatal("failed load left partial residence")
	}
	b = runtimeBudget()
	r, versions = runtimeFixture(t, []SkillBinding{{Ref: "own/a", Activation: "manual"}}, map[string][]PackageFile{"own/a": testSkill("body")}, b)
	if _, err := r.Read(context.Background(), versions[0].Version.Ref, versions[0].Version.Digest, "../secret", 1, 1); err == nil {
		t.Fatal("traversal accepted")
	}
	var wg sync.WaitGroup
	successes := 0
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Load(context.Background(), versions[0].Version.Ref, versions[0].Version.Digest); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 2 || len(r.Blocks()) != 1 {
		t.Fatalf("concurrent residence=%d blocks=%d", successes, len(r.Blocks()))
	}
}
func TestRuntimeRequiresExactEligibilityAndReadLimit(t *testing.T) {
	b := runtimeBudget()
	b.MaxResourceReadsPerTurn = 1
	r, v := runtimeFixture(t, []SkillBinding{{Ref: "own/a", Activation: "manual"}}, map[string][]PackageFile{"own/a": testSkill("body")}, b)
	if _, err := r.Load(context.Background(), "own/a", "sha256:wrong"); err == nil {
		t.Fatal("foreign digest accepted")
	}
	if _, err := r.Read(context.Background(), v[0].Version.Ref, v[0].Version.Digest, "guide.md", 1, 1); err == nil {
		t.Fatal("read before load")
	}
	if _, err := r.Load(context.Background(), v[0].Version.Ref, v[0].Version.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(context.Background(), v[0].Version.Ref, v[0].Version.Digest, "guide.md", 1, 1); err != nil {
		t.Fatal(err)
	}
	again, err := r.Read(context.Background(), v[0].Version.Ref, v[0].Version.Digest, "guide.md", 1, 1)
	if err != nil || !again.Receipt.Existing || again.Text != "" || again.NextCursor != "2" {
		t.Fatalf("resident read must retain cursor without another reservation: %+v %v", again, err)
	}
	if _, err := r.Read(context.Background(), v[0].Version.Ref, v[0].Version.Digest, "guide.md", 2, 1); err == nil {
		t.Fatal("read limit ignored")
	}
}

func TestRuntimeRejectsUnprovidedTokenizerAndRechecksNewPayloadBudget(t *testing.T) {
	r, _ := runtimeFixture(t, []SkillBinding{{Ref: "own/a", Activation: "pinned"}}, map[string][]PackageFile{"own/a": testSkill("required body")}, runtimeBudget())
	if _, err := r.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	budget := r.budget
	budget.Measurement = MeasurementTokenizer
	if _, err := NewRuntime(r.set, budget, r.read); err == nil || !strings.Contains(err.Error(), "tokenizer_unavailable") {
		t.Fatalf("unprovided tokenizer accepted: %v", err)
	}
	budget = r.budget
	budget.NonSkillInput = budget.InputLimit - 1
	if _, err := r.Payload(budget); err == nil || !strings.Contains(err.Error(), "context_budget_exceeded") {
		t.Fatalf("new payload budget ignored: %v", err)
	}
	if r.epoch != 1 {
		t.Fatal("failed payload changed residence")
	}
}

func TestRuntimeAutoCatalogueIsBoundedAndNotBodies(t *testing.T) {
	bindings := make([]SkillBinding, 9)
	files := map[string][]PackageFile{}
	for i := range bindings {
		ref := "own/a" + string(rune('a'+i))
		bindings[i] = SkillBinding{Ref: ref, Activation: "auto"}
		files[ref] = testSkill("PRIVATE BODY " + ref)
	}
	r, _ := runtimeFixture(t, bindings, files, runtimeBudget())
	plan, err := r.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blocks) != 9 {
		t.Fatalf("catalogue blocks=%d", len(plan.Blocks))
	}
	for _, block := range plan.Blocks {
		if block.Kind != BlockCatalogue || strings.Contains(block.Text, "PRIVATE BODY") {
			t.Fatalf("auto body leaked: %+v", block)
		}
	}
}

func TestRuntimeDependencyClosureAndCycleAreAtomic(t *testing.T) {
	bindings := []SkillBinding{{Ref: "own/a", Activation: "manual"}, {Ref: "own/b", Activation: "manual"}}
	files := map[string][]PackageFile{
		"own/a": {{Path: "SKILL.md", Content: []byte("---\nname: a\ndescription: a\nmetadata:\n  praimate.dependencies: own/b\n---\nA")}},
		"own/b": {{Path: "SKILL.md", Content: []byte("---\nname: b\ndescription: b\nmetadata:\n  praimate.dependencies: own/a\n---\nB")}},
	}
	r, versions := runtimeFixture(t, bindings, files, runtimeBudget())
	if _, err := r.Load(context.Background(), versions[0].Version.Ref, versions[0].Version.Digest); err == nil || !strings.Contains(err.Error(), "dependency_cycle") {
		t.Fatalf("cycle: %v", err)
	}
	if len(r.Blocks()) != 0 {
		t.Fatal("cycle left partial blocks")
	}
	files["own/b"] = testSkill("B")
	r, versions = runtimeFixture(t, bindings, files, runtimeBudget())
	plan, err := r.Load(context.Background(), versions[0].Version.Ref, versions[0].Version.Digest)
	if err != nil || len(plan.Blocks) != 2 {
		t.Fatalf("closure: %+v %v", plan, err)
	}
}

func TestRuntimeActiveLimitCountsBodiesAndResidentLoadDoesNotRedeliver(t *testing.T) {
	r, versions := runtimeFixture(t, []SkillBinding{{Ref: "own/a", Activation: "pinned"}, {Ref: "own/b", Activation: "pinned"}}, map[string][]PackageFile{"own/a": testSkill("a long first body"), "own/b": testSkill("a long second body")}, runtimeBudget())
	plan, err := r.Build(context.Background())
	if err != nil || len(plan.Blocks) != 2 {
		t.Fatalf("two bodies within max_active=3: %+v %v", plan, err)
	}
	again, err := r.Load(context.Background(), versions[0].Version.Ref, versions[0].Version.Digest)
	if err != nil || !again.Receipt.Existing || len(again.Blocks) != 0 || len(again.Receipt.Delivered) != 1 {
		t.Fatalf("resident load must reference, not repeat, body: %+v %v", again, err)
	}
}

func TestRuntimeBuildFailureRollsBackWholePlan(t *testing.T) {
	budget := runtimeBudget()
	budget.BodyTokens = 1
	r, _ := runtimeFixture(t, []SkillBinding{{Ref: "own/a", Activation: "auto"}, {Ref: "own/b", Activation: "pinned"}}, map[string][]PackageFile{"own/a": testSkill("a"), "own/b": testSkill("too large a body")}, budget)
	if _, err := r.Build(context.Background()); err == nil || len(r.Blocks()) != 0 {
		t.Fatalf("failed Build retained a partial catalogue: blocks=%+v err=%v", r.Blocks(), err)
	}
}

func TestRuntimeResourceRangesAccumulateAndCannotOverwriteReservations(t *testing.T) {
	budget := runtimeBudget()
	budget.ResourceTokens = 2
	r, v := runtimeFixture(t, []SkillBinding{{Ref: "own/a", Activation: "pinned"}}, map[string][]PackageFile{"own/a": testSkill("body")}, budget)
	if _, err := r.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, line := range []int{1, 2} {
		if _, err := r.Read(context.Background(), v[0].Version.Ref, v[0].Version.Digest, "guide.md", line, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Read(context.Background(), v[0].Version.Ref, v[0].Version.Digest, "guide.md", 4, 1); err == nil {
		t.Fatal("successive ranges bypassed resource budget")
	}
	if len(r.Blocks()) != 3 {
		t.Fatalf("resource ranges lost from payload: %+v", r.Blocks())
	}
}

func TestRuntimeConflictIsSymmetricAndIncludesDependencies(t *testing.T) {
	files := map[string][]PackageFile{
		"own/a": {{Path: "SKILL.md", Content: []byte("---\nname: a\ndescription: a\nmetadata:\n  praimate.conflicts: own/b\n---\nA")}},
		"own/b": testSkill("B"),
	}
	r, v := runtimeFixture(t, []SkillBinding{{Ref: "own/a", Activation: "manual"}, {Ref: "own/b", Activation: "manual"}}, files, runtimeBudget())
	if _, err := r.Load(context.Background(), v[0].Version.Ref, v[0].Version.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Load(context.Background(), v[1].Version.Ref, v[1].Version.Digest); err == nil || !strings.Contains(err.Error(), "skill_conflict") {
		t.Fatalf("one-sided conflict ignored when conflicting skill loaded second: %v", err)
	}
}
