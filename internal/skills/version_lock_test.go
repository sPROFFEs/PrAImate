package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPortableLockCannotImportAuthorityAndRetainsInterruptedRuns(t *testing.T) {
	ctx := context.Background()
	s, err := OpenHostSkillStore(t.TempDir(), PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var pinned SkillVersion
	var body []byte
	first, err := s.Update(ctx, 0, func(tx *SkillHostTransaction) error {
		pinned, err = tx.Publish(ctx, hostFixtureDraft(t), 1)
		if err != nil {
			return err
		}
		body, err = tx.ExportLock(ctx, []SkillVersion{pinned})
		if err != nil {
			return err
		}
		if err := tx.HoldLock(ctx, "chat:interrupted", body); err != nil {
			return err
		}
		return tx.HoldLock(ctx, "terminal:native", body)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"approved", "trusted", "Generation", "generation", "local-state", "chat:interrupted"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("nonportable state in lock", secret)
		}
	}
	for _, mutation := range []func(map[string]any){
		func(m map[string]any) { m["trusted"] = true },
		func(m map[string]any) { m["permissions"] = []string{"exec"} },
		func(m map[string]any) { m["entries"].([]any)[0].(map[string]any)["trusted"] = true },
		func(m map[string]any) {
			m["entries"].([]any)[0].(map[string]any)["source"].(map[string]any)["permissions"] = "full"
		},
	} {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
		mutation(m)
		poisoned, _ := json.Marshal(m)
		if _, err := s.Update(ctx, 1, func(tx *SkillHostTransaction) error { return tx.HoldLock(ctx, "poisoned", poisoned) }); err == nil {
			t.Fatal("lock authority accepted")
		}
	}
	if _, err := DecodeSkillVersionLock(bytes.Replace(body, []byte(`"schema":`), []byte(`"schema":"ignored","schema":`), 1)); err == nil {
		t.Fatal("duplicate fields accepted")
	}
	_, err = s.Update(ctx, 1, func(tx *SkillHostTransaction) error {
		if tx.local.Approved(pinned) {
			return errors.New("imported lock granted trust")
		}
		return tx.Release("chat:interrupted")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, 2, func(tx *SkillHostTransaction) error { return tx.Forget(pinned.Ref, pinned.Digest) }); err == nil {
		t.Fatal("native lease not retained")
	}
	gc, err := s.Collect(ctx, 2, false)
	if err != nil || len(gc.Removed) != 0 {
		t.Fatal("GC removed native lease", err)
	}
	second, err := s.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Update(ctx, 2, func(tx *SkillHostTransaction) error {
		if err := tx.Release("terminal:native"); err != nil {
			return err
		}
		return tx.Forget(pinned.Ref, pinned.Digest)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseCheckpoint(ctx, 3, first); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseCheckpoint(ctx, 4, second.Checkpoint); err != nil {
		t.Fatal(err)
	}
	gc, err = s.Collect(ctx, 5, false)
	if err != nil || len(gc.Removed) != 1 {
		t.Fatal("released content not collected", err)
	}
}
