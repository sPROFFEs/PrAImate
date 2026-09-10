package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestRetentionLimitCannotPublishUnreadableState(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	c, err := NewVersionCatalogue(root, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	v, err := hostFixtureDraft(t).Publish(context.Background(), c, 1)
	if err != nil {
		t.Fatal(err)
	}
	s := NewLocalSkillState()
	for i := 0; i < maxRegistryVersions; i++ {
		s.holds[fmt.Sprint(i)] = []SkillVersion{v}
	}
	if err := s.Hold(context.Background(), c, "overflow", []SkillVersion{v}); err == nil {
		t.Fatal("hold limit can publish an unreadable checkpoint")
	}
	if len(s.holds) != maxRegistryVersions {
		t.Fatal("failed hold changed state")
	}
}

func TestLocalStatePinsAndApprovalAreSeparateFromCatalogue(t *testing.T) {
	ctx := context.Background()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	c, err := NewVersionCatalogue(root, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := NewSkillDraft("own", "local/test", []PackageFile{{Path: "SKILL.md", Content: []byte("---\nname: test\ndescription: Test\n---\nFIRST")}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := draft.Publish(ctx, c, 1)
	if err != nil {
		t.Fatal(err)
	}
	s := NewLocalSkillState()
	if err := s.SetApproval(ctx, c, a.Ref, a.Digest, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Hold(ctx, c, "interrupted-run", []SkillVersion{a}); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetVersion(c, a.Ref, a.Digest); err == nil {
		t.Fatal("retained version removed")
	}
	revision, files := draft.Snapshot()
	files[0].Content = append(files[0].Content, '\n')
	revision, err = draft.ReplaceFiles(revision, files)
	if err != nil {
		t.Fatal(err)
	}
	b, err := draft.Publish(ctx, c, revision)
	if err != nil {
		t.Fatal(err)
	}
	if s.Approved(b) {
		t.Fatal("approval inherited by new digest")
	}
	name, err := c.SaveSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "approved") || strings.Contains(string(body), "interrupted-run") {
		t.Fatal("host authority leaked into catalogue")
	}
	stateID, err := s.SaveCheckpoint(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	stateBody, err := root.ReadFile(stateID)
	if err != nil {
		t.Fatal(err)
	}
	var record localSkillRecord
	if err := json.Unmarshal(stateBody, &record); err != nil || len(record.Holds["interrupted-run"]) != 1 {
		t.Fatal("hold not persisted")
	}
	restored, err := LoadLocalSkillState(ctx, root, stateID, c)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Approved(a) || restored.Approved(b) {
		t.Fatal("host approvals changed after restart")
	}
	if err := restored.ForgetVersion(c, a.Ref, a.Digest); err == nil {
		t.Fatal("restart lost retention hold")
	}
	s.Release("interrupted-run")
	if err := s.ForgetVersion(c, a.Ref, a.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPackageInstallation(ctx, root, a.Generation, PackageLimits{}); err != nil {
		t.Fatal("forget removed rollback content")
	}
}
