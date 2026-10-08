package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Worktree struct {
	ID                string   `json:"id"`
	TaskID            string   `json:"taskID"`
	Path              string   `json:"path"`
	Branch            string   `json:"branch"`
	BaseRef           string   `json:"baseRef"`
	IntegratedCommits []string `json:"integratedCommits,omitempty"`
	PendingCommit     string   `json:"pendingCommit,omitempty"`
	PendingBase       string   `json:"pendingBase,omitempty"`
}

type WorktreeManager struct {
	Workspace string
	RunID     string
}

type gitBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *gitBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func gitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	argv := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "commit.gpgsign=false", "-c", "core.quotePath=false", "-c", "rerere.enabled=true", "-c", "rerere.autoupdate=true"}
	cmd := exec.CommandContext(ctx, "git", append(argv, args...)...)
	cmd.Dir = dir
	// Host-created commits do not invoke hooks or require the user's Git identity.
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=PrAImate Workers", "GIT_AUTHOR_EMAIL=workers@praimate.local", "GIT_COMMITTER_NAME=PrAImate Workers", "GIT_COMMITTER_EMAIL=workers@praimate.local", "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out := &gitBuffer{limit: 4 << 20}
	stderr := &gitBuffer{limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %s: %w", args[0], strings.TrimSpace(stderr.String()), err)
	}
	if out.overflow {
		return out.String(), errors.New("Git output exceeds 4 MiB; inspect the worktree directly")
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}

func (w WorktreeManager) root(ctx context.Context) (string, error) {
	if !taskIDPattern.MatchString(w.RunID) {
		return "", errors.New("invalid worker run ID")
	}
	common, err := gitCommand(ctx, w.Workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return w.rootForRepository(common)
}

func (w WorktreeManager) rootForRepository(common string) (string, error) {
	common, err := filepath.EvalSymlinks(common)
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(cache) {
		return "", errors.New("worker cache directory must be absolute")
	}
	digest := sha256.Sum256([]byte(filepath.Clean(common)))
	return filepath.Join(cache, "praimate-worker-worktrees", hex.EncodeToString(digest[:16]), w.RunID), nil
}

func (w WorktreeManager) Repository(ctx context.Context) (base, branch string, err error) {
	root, err := gitCommand(ctx, w.Workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	actual, err := filepath.EvalSymlinks(w.Workspace)
	if err != nil {
		return "", "", err
	}
	expected, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(actual) != filepath.Clean(expected) {
		return "", "", errors.New("parallel workers require the repository root as workspace")
	}
	branch, err = gitCommand(ctx, w.Workspace, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", "", errors.New("parallel workers require a checked-out target branch")
	}
	status, err := gitCommand(ctx, w.Workspace, "status", "--porcelain")
	if err != nil {
		return "", "", err
	}
	if status != "" {
		return "", "", errors.New("commit or stash workspace changes before using parallel workers")
	}
	base, err = gitCommand(ctx, w.Workspace, "rev-parse", "HEAD")
	return
}

func (w WorktreeManager) Create(ctx context.Context, taskID, base string) (*Worktree, error) {
	if !taskIDPattern.MatchString(taskID) {
		return nil, errors.New("invalid task ID")
	}
	root, err := w.root(ctx)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	tree := &Worktree{ID: taskID, TaskID: taskID, Path: filepath.Join(root, "task-"+taskID), Branch: "praimate/" + w.RunID + "/task-" + taskID, BaseRef: base}
	if _, err = gitCommand(ctx, w.Workspace, "worktree", "add", "-b", tree.Branch, tree.Path, base); err != nil {
		return tree, err
	}
	return tree, nil
}

func (w WorktreeManager) verify(ctx context.Context, tree *Worktree) error {
	if tree == nil || !taskIDPattern.MatchString(tree.ID) {
		return errors.New("unknown worker worktree")
	}
	root, err := w.root(ctx)
	if err != nil {
		return err
	}
	if tree.Path != filepath.Join(root, "task-"+tree.ID) || tree.Branch != "praimate/"+w.RunID+"/task-"+tree.ID {
		return errors.New("worktree ownership does not match this run")
	}
	common, err := gitCommand(ctx, tree.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	actualRoot, err := w.rootForRepository(common)
	if err != nil {
		return err
	}
	if actualRoot != root {
		return errors.New("worker worktree belongs to another repository")
	}
	branch, err := gitCommand(ctx, tree.Path, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if branch != tree.Branch {
		return errors.New("worker changed its checked-out branch; inspect its worktree")
	}
	return nil
}

func (w WorktreeManager) IntegrateDependencies(ctx context.Context, tree *Worktree, commits []string) error {
	if err := w.verify(ctx, tree); err != nil {
		return err
	}
	if err := w.continueDependency(ctx, tree, commits); err != nil {
		return err
	}
	for _, commit := range commits {
		already := false
		for _, saved := range tree.IntegratedCommits {
			if saved == commit {
				already = true
				break
			}
		}
		if already {
			continue
		}
		head, err := gitCommand(ctx, tree.Path, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		tree.PendingCommit = commit
		tree.PendingBase = head
		if _, err := gitCommand(ctx, tree.Path, "cherry-pick", "-x", commit); err != nil {
			// rerere may already have staged a previously reviewed resolution.
			if _, stateErr := gitCommand(ctx, tree.Path, "rev-parse", "--verify", "-q", "CHERRY_PICK_HEAD"); stateErr != nil {
				return fmt.Errorf("dependency integration in %s: %w", tree.Path, err)
			}
			if resumeErr := w.continueDependency(ctx, tree, commits); resumeErr != nil {
				return resumeErr
			}
			if tree.PendingCommit != "" {
				return fmt.Errorf("dependency integration in %s: %w", tree.Path, err)
			}
			continue
		}
		tree.PendingCommit = ""
		tree.PendingBase = ""
		tree.IntegratedCommits = append(tree.IntegratedCommits, commit)
		if head, err := gitCommand(ctx, tree.Path, "rev-parse", "HEAD"); err == nil {
			tree.BaseRef = head
		} else {
			return err
		}
	}
	return nil
}

func (w WorktreeManager) Complete(ctx context.Context, tree *Worktree, worker WorkerConfig) (*TaskResult, error) {
	if err := w.verify(ctx, tree); err != nil {
		return nil, err
	}
	head, err := gitCommand(ctx, tree.Path, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if _, err = gitCommand(ctx, tree.Path, "merge-base", "--is-ancestor", tree.BaseRef, head); err != nil {
		return nil, errors.New("worker rewrote the dependency base; inspect its worktree")
	}
	if _, err = gitCommand(ctx, tree.Path, "add", "-A", "--", "."); err != nil {
		return nil, err
	}
	treeID, err := gitCommand(ctx, tree.Path, "write-tree")
	if err != nil {
		return nil, err
	}
	baseTree, err := gitCommand(ctx, tree.Path, "rev-parse", tree.BaseRef+"^{tree}")
	if err != nil {
		return nil, err
	}
	result := &TaskResult{TaskID: tree.TaskID, WorkerConfig: worker, BaseCommit: tree.BaseRef, ResultCommit: tree.BaseRef, ChangedFiles: []string{}}
	if treeID != baseTree {
		// Squash any model-created commits into one authoritative task patch.
		result.ResultCommit, err = gitCommand(ctx, tree.Path, "commit-tree", treeID, "-p", tree.BaseRef, "-m", "PrAImate worker task "+tree.TaskID)
		if err != nil {
			return nil, err
		}
		if _, err = gitCommand(ctx, tree.Path, "update-ref", "refs/heads/"+tree.Branch, result.ResultCommit, head); err != nil {
			return nil, err
		}
	}
	diff, diffErr := gitCommand(ctx, tree.Path, "diff", "--no-ext-diff", "--no-textconv", tree.BaseRef, result.ResultCommit, "--")
	if diffErr != nil && len(diff) < 4<<20 {
		return nil, diffErr
	}
	if len(diff) > 256<<10 {
		diff = diff[:256<<10] + "\n[Diff preview truncated. Inspect the worktree for the complete patch.]"
		result.DiffTruncated = true
	}
	result.Diff = diff
	names, err := gitCommand(ctx, tree.Path, "diff", "--name-only", "-z", tree.BaseRef, result.ResultCommit, "--")
	if err != nil {
		return nil, err
	}
	if names != "" {
		result.ChangedFiles = strings.Split(strings.TrimRight(names, "\x00"), "\x00")
	}
	result.ResultRef = "refs/praimate-workers/" + w.RunID + "/task-" + tree.TaskID
	if _, err = gitCommand(ctx, tree.Path, "update-ref", result.ResultRef, result.ResultCommit); err != nil {
		return nil, err
	}
	return result, nil
}

func (w WorktreeManager) RemoveResultRef(ctx context.Context, taskID string) error {
	if _, err := w.root(ctx); err != nil {
		return err
	}
	if !taskIDPattern.MatchString(taskID) {
		return errors.New("invalid task ID")
	}
	_, err := gitCommand(ctx, w.Workspace, "update-ref", "-d", "refs/praimate-workers/"+w.RunID+"/task-"+taskID)
	return err
}

func (w WorktreeManager) Remove(ctx context.Context, tree *Worktree) error {
	root, err := w.root(ctx)
	if err != nil {
		return err
	}
	if tree == nil || !taskIDPattern.MatchString(tree.ID) || tree.Path != filepath.Join(root, "task-"+tree.ID) || tree.Branch != "praimate/"+w.RunID+"/task-"+tree.ID {
		return errors.New("cannot remove an unowned worktree")
	}
	// Git may still register a worktree whose cache directory disappeared.
	// Inspect the registration instead of relying on directory existence, and
	// never prune registrations belonging to another run.
	registered, err := gitCommand(ctx, w.Workspace, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	found := false
	for _, record := range strings.Split(registered, "\x00\x00") {
		var path, branch string
		for _, field := range strings.Split(record, "\x00") {
			if strings.HasPrefix(field, "worktree ") {
				path = strings.TrimPrefix(field, "worktree ")
			} else if strings.HasPrefix(field, "branch ") {
				branch = strings.TrimPrefix(field, "branch ")
			}
		}
		if filepath.Clean(path) == filepath.Clean(tree.Path) {
			if branch != "refs/heads/"+tree.Branch {
				return errors.New("worktree registration no longer matches this run; inspect it before removal")
			}
			found = true
			break
		}
	}
	// Removal is requested only after review or explicit discard of failed work.
	if found {
		if _, err = gitCommand(ctx, w.Workspace, "worktree", "remove", "--force", tree.Path); err != nil {
			return err
		}
	} else if _, err = os.Stat(tree.Path); err == nil {
		return errors.New("unregistered worktree directory still exists; inspect it before removal")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = gitCommand(ctx, w.Workspace, "show-ref", "--verify", "--quiet", "refs/heads/"+tree.Branch); err == nil {
		_, err = gitCommand(ctx, w.Workspace, "branch", "-D", tree.Branch)
		if err != nil {
			return err
		}
	}
	// Remove only empty directories for this run/repository. Other runs may
	// still be using the shared cache namespace.
	_ = os.Remove(root)
	_ = os.Remove(filepath.Dir(root))
	return nil
}

func (w WorktreeManager) Merge(ctx context.Context, branch string, commits []string, checkpoint ...func(*Worktree, bool) error) (*Worktree, error) {
	base, current, err := w.Repository(ctx)
	if err != nil {
		return nil, err
	}
	if current != branch {
		return nil, errors.New("return to the run's target branch before merging")
	}
	tree, err := w.Create(ctx, "review-merge", base)
	if err != nil {
		return tree, err
	}
	if len(checkpoint) > 0 {
		if err = checkpoint[0](tree, false); err != nil {
			return tree, err
		}
	}
	if err = w.IntegrateDependencies(ctx, tree, commits); err != nil {
		return tree, err
	}
	if len(checkpoint) > 0 {
		if err = checkpoint[0](tree, true); err != nil {
			return tree, err
		}
	}
	if _, err = gitCommand(ctx, w.Workspace, "merge", "--ff-only", tree.Branch); err != nil {
		return tree, err
	}
	return tree, nil
}
