package skills

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func hostFixtureDraft(t *testing.T) *SkillDraft {
	t.Helper()
	d, err := NewSkillDraft("own-id", "local/test", []PackageFile{{Path: "SKILL.md", Content: []byte("---\nname: test\ndescription: Test\n---\nBODY")}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestHostStoreAtomicRestartRetentionAndGC(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	s, err := OpenHostSkillStore(directory, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := hostFixtureDraft(t)
	var version SkillVersion
	first, err := s.Update(ctx, 0, func(tx *SkillHostTransaction) error {
		var err error
		version, err = tx.Publish(ctx, d, 1)
		if err != nil {
			return err
		}
		if err := tx.Hold(ctx, "run-1", []SkillVersion{version}); err != nil {
			return err
		}
		return tx.SaveDraft(ctx, "editor", d)
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := OpenHostSkillStore(directory, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	view, err := other.View(ctx)
	if err != nil || view.Checkpoint != first || view.Revision != 1 || len(view.Versions) != 1 {
		t.Fatalf("restart: %+v %v", view, err)
	}
	if _, err := other.Update(ctx, 1, func(tx *SkillHostTransaction) error { return tx.Forget(version.Ref, version.Digest) }); err == nil {
		t.Fatal("active hold lost on restart")
	}
	if _, err := s.Update(ctx, 0, func(tx *SkillHostTransaction) error { return nil }); err == nil {
		t.Fatal("stale update accepted")
	}
	if _, err := s.Update(ctx, 1, func(tx *SkillHostTransaction) error {
		if err := tx.Release("run-1"); err != nil {
			return err
		}
		return tx.Forget(version.Ref, version.Digest)
	}); err != nil {
		t.Fatal(err)
	}
	gc, err := s.Collect(ctx, 2, false)
	if err != nil || len(gc.Removed) != 0 {
		t.Fatalf("retained checkpoint collected: %+v %v", gc, err)
	}
	if err := s.ReleaseCheckpoint(ctx, 2, first); err != nil {
		t.Fatal(err)
	}
	gc, err = s.Collect(ctx, 3, false)
	if err != nil || len(gc.Removed) != 1 || gc.Removed[0] != version.Generation {
		t.Fatalf("unreferenced generation not collected: %+v %v", gc, err)
	}
	if _, err := s.Update(ctx, 3, func(tx *SkillHostTransaction) error { return tx.Restore(ctx, first) }); err == nil {
		t.Fatal("released checkpoint restored")
	}
	if _, err := s.View(ctx); err != nil {
		t.Fatal("GC broke active host", err)
	}
}

func TestHostStoreRollbackAndCallbackFailure(t *testing.T) {
	ctx := context.Background()
	s, err := OpenHostSkillStore(t.TempDir(), PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	initial, err := s.Update(ctx, 0, func(tx *SkillHostTransaction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	d := hostFixtureDraft(t)
	if _, err := s.Update(ctx, 1, func(tx *SkillHostTransaction) error {
		if _, err := tx.Publish(ctx, d, 1); err != nil {
			return err
		}
		return errors.New("injected failure")
	}); err == nil {
		t.Fatal("callback failure ignored")
	}
	view, err := s.View(ctx)
	if err != nil || view.Revision != 1 || len(view.Versions) != 0 {
		t.Fatal("failed transaction changed active catalogue")
	}
	gc, err := s.Collect(ctx, 1, false)
	if err != nil || len(gc.Removed) != 1 {
		t.Fatal("failed transaction orphan not collected", err)
	}
	if _, err := s.Update(ctx, 1, func(tx *SkillHostTransaction) error { _, err := tx.Publish(ctx, d, 1); return err }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, 2, func(tx *SkillHostTransaction) error { return tx.Restore(ctx, initial) }); err != nil {
		t.Fatal(err)
	}
	view, err = s.View(ctx)
	if err != nil || len(view.Versions) != 0 {
		t.Fatal("rollback failed")
	}
}

func TestHostLockCrossProcess(t *testing.T) {
	if directory := os.Getenv("PRAIMATE_HOST_LOCK_FIXTURE"); directory != "" {
		s, err := OpenHostSkillStore(directory, PackageLimits{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, err := s.View(context.Background()); !errors.Is(err, ErrSkillHostBusy) {
			t.Fatalf("other process bypassed lock: %v", err)
		}
		return
	}
	directory := t.TempDir()
	s, err := OpenHostSkillStore(directory, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Update(context.Background(), 0, func(tx *SkillHostTransaction) error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHostLockCrossProcess$")
		cmd.Env = append(os.Environ(), "PRAIMATE_HOST_LOCK_FIXTURE="+directory)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("lock child failed: %v %s", err, output)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHostTransactionProcessCrash(t *testing.T) {
	if directory := os.Getenv("PRAIMATE_HOST_CRASH_FIXTURE"); directory != "" {
		s, err := OpenHostSkillStore(directory, PackageLimits{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Update(context.Background(), 0, func(tx *SkillHostTransaction) error {
			if _, err := tx.Publish(context.Background(), hostFixtureDraft(t), 1); err != nil {
				return err
			}
			os.Exit(39)
			return nil
		})
		t.Fatal("crash not reached", err)
	}
	directory := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostTransactionProcessCrash$")
	cmd.Env = append(os.Environ(), "PRAIMATE_HOST_CRASH_FIXTURE="+directory)
	if err := cmd.Run(); err == nil {
		t.Fatal("child did not crash")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 39 {
		t.Fatal(err)
	}
	s, err := OpenHostSkillStore(directory, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	view, err := s.View(context.Background())
	if err != nil || view.Revision != 0 || len(view.Versions) != 0 {
		t.Fatal("crash published incomplete host state")
	}
	gc, err := s.Collect(context.Background(), 0, false)
	if err != nil || len(gc.Removed) != 1 {
		t.Fatal("crash recovery failed", err)
	}
}
