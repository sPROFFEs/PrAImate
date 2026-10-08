package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type DependencyConflict struct {
	Path  string
	Files []string
}

func (e *DependencyConflict) Error() string {
	return fmt.Sprintf("dependency conflict in %s (%s). Resolve with the assigned worker, stage a manual resolution and continue, or explicitly start over. Existing changes are retained.", e.Path, strings.Join(e.Files, ", "))
}

func conflictFiles(ctx context.Context, path string) ([]string, error) {
	raw, err := gitCommand(ctx, path, "diff", "--name-only", "--diff-filter=U", "-z", "--")
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimRight(raw, "\x00"), "\x00"), nil
}

func (w WorktreeManager) continueDependency(ctx context.Context, tree *Worktree, commits []string) error {
	pending, err := gitCommand(ctx, tree.Path, "rev-parse", "--verify", "-q", "CHERRY_PICK_HEAD")
	if err != nil {
		files, e := conflictFiles(ctx, tree.Path)
		if e != nil {
			return e
		}
		if len(files) > 0 {
			return &DependencyConflict{tree.Path, files}
		}
		if tree.PendingCommit != "" {
			// A cancelled pick can be applied again. External completion is accepted
			// only when Git records this dependency's source in the new commit.
			head, e := gitCommand(ctx, tree.Path, "rev-parse", "HEAD")
			if e != nil {
				return e
			}
			if head != tree.PendingBase {
				message, e := gitCommand(ctx, tree.Path, "log", "-1", "--format=%B")
				if e != nil {
					return e
				}
				if !strings.Contains(message, "(cherry picked from commit "+tree.PendingCommit+")") {
					return errors.New("dependency worktree changed outside PrAImate; inspect its pending integration before continuing")
				}
				tree.IntegratedCommits = append(tree.IntegratedCommits, tree.PendingCommit)
				tree.BaseRef = head
			}
			tree.PendingCommit = ""
			tree.PendingBase = ""
		}
		return nil
	}
	owned := false
	for _, commit := range commits {
		if commit == pending {
			owned = true
			break
		}
	}
	if !owned || (tree.PendingCommit != "" && pending != tree.PendingCommit) {
		return errors.New("worktree has an unrelated cherry-pick; inspect it before continuing")
	}
	tree.PendingCommit = pending
	if tree.PendingBase == "" {
		tree.PendingBase, err = gitCommand(ctx, tree.Path, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
	}
	files, err := conflictFiles(ctx, tree.Path)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		if _, err := gitCommand(ctx, tree.Path, "rerere"); err != nil {
			return err
		}
		return &DependencyConflict{tree.Path, files}
	}
	if _, err = gitCommand(ctx, tree.Path, "rerere"); err != nil {
		return err
	}
	check, _ := gitCommand(ctx, tree.Path, "diff", "--cached", "--check")
	if strings.Contains(check, "leftover conflict marker") {
		return errors.New("staged dependency resolution still contains conflict markers; remove them before continuing")
	}
	if _, err = gitCommand(ctx, tree.Path, "diff", "--cached", "--quiet"); err == nil {
		_, err = gitCommand(ctx, tree.Path, "cherry-pick", "--skip")
	} else {
		_, err = gitCommand(ctx, tree.Path, "cherry-pick", "--continue")
	}
	if err != nil {
		return fmt.Errorf("continue dependency integration in %s: %w", tree.Path, err)
	}
	tree.IntegratedCommits = append(tree.IntegratedCommits, pending)
	tree.PendingCommit = ""
	tree.PendingBase = ""
	tree.BaseRef, err = gitCommand(ctx, tree.Path, "rev-parse", "HEAD")
	return err
}

// StageResolvedDependency never resolves by choosing one side automatically.
// A worker must remove the conflict markers; Git retains all other staged files.
func (w WorktreeManager) StageResolvedDependency(ctx context.Context, tree *Worktree, files []string) error {
	if err := w.verify(ctx, tree); err != nil {
		return err
	}
	for _, name := range files {
		if filepath.IsAbs(name) || filepath.Clean(name) == ".." || strings.HasPrefix(filepath.Clean(name), ".."+string(filepath.Separator)) {
			return errors.New("invalid conflict path")
		}
		path := filepath.Join(tree.Path, name)
		info, err := os.Lstat(path)
		if err == nil && info.Mode().IsRegular() {
			if info.Size() > 4<<20 {
				return errors.New("conflict file exceeds 4 MiB; resolve and stage it manually")
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(line, "<<<<<<< ") || strings.HasPrefix(line, ">>>>>>> ") {
					return fmt.Errorf("unresolved conflict markers in %s", name)
				}
			}
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if _, err := gitCommand(ctx, tree.Path, "add", "-A", "--", name); err != nil {
			return err
		}
	}
	_, err := gitCommand(ctx, tree.Path, "rerere")
	return err
}
