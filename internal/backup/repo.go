package backup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Status describes the current state of a backup repo at a glance.
// Returned by Repo.Status; rendered in the Backup screen's header.
type Status struct {
	// Initialized = the workspaces root is a git repo.
	Initialized bool
	// HasRemote = "origin" remote is configured.
	HasRemote bool
	// RemoteURL is whatever origin points at, when HasRemote is true.
	RemoteURL string
	// Clean = working tree has no uncommitted changes.
	Clean bool
	// Ahead / Behind are the commit counts vs origin/<DefaultBranch>.
	// Both 0 → in sync. Both >0 → diverged.
	Ahead, Behind int
	// DefaultBranch (usually "main"). Empty when not yet initialized.
	DefaultBranch string
	// LastCommit holds the local HEAD's short hash + subject for the
	// Backup screen's "last sync at <time>" line. Empty when no
	// commits exist yet.
	LastCommit     string
	LastCommitTime time.Time
}

// Diverged returns true when both sides have commits the other side
// lacks. The Backup screen uses this to decide whether to open the
// merge/rebase/force-push/reset popup or perform a clean fast-forward.
func (s Status) Diverged() bool { return s.Ahead > 0 && s.Behind > 0 }

// LocalAheadOnly = clean local-side push.
func (s Status) LocalAheadOnly() bool { return s.Ahead > 0 && s.Behind == 0 }

// RemoteAheadOnly = clean remote-side pull.
func (s Status) RemoteAheadOnly() bool { return s.Ahead == 0 && s.Behind > 0 }

// InSync reports both clean working tree AND no divergence vs remote.
func (s Status) InSync() bool { return s.Clean && s.Ahead == 0 && s.Behind == 0 }

// -----------------------------------------------------------------------------
// High-level operations
// -----------------------------------------------------------------------------

// IsGitRepo reports whether dir/.git exists. Cheap pre-check before
// running any other repo op.
func IsGitRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	// .git is usually a dir but in worktrees it's a file pointing
	// elsewhere — either form counts as "this is a git checkout."
	return info != nil
}

// Init initialises dir as a git repo for backup use:
//   - `git init -b main` (or fallback for older git)
//   - write managed .gitignore + .gitattributes
//   - register the MEMORY.md merge driver in local config
//   - stage + commit the initial layout under the standard message
//
// Idempotent: if dir is already a git repo, the function still
// writes the managed files + driver registration so first-time link
// scenarios are clean. Returns the Result of the LAST git operation
// when something fails, so the caller can render git's own error.
func Init(ctx context.Context, dir string) (Status, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return Status{}, errors.New("git init: backup directory is empty")
	}
	// Run executes git with dir as cmd.Dir. Unlike `git init <dir>`, that
	// requires the directory to exist before the process can start. The
	// configured workspaces root can legitimately disappear between runs
	// (for example when it was placed under a cleaned temporary directory),
	// so recreate the path before invoking Git.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Status{}, fmt.Errorf("git init: create backup directory %s: %w", dir, err)
	}
	if !IsGitRepo(dir) {
		// `-b main` is the modern default; falls back gracefully on
		// older git versions that don't know -b (they default to
		// "master", which we then rename below).
		r := Run(ctx, dir, "init", "-b", "main")
		if r.Failed() {
			// Retry without -b for older git.
			r = Run(ctx, dir, "init")
			if r.Failed() {
				return Status{}, fmt.Errorf("git init failed: %s", UserError(r))
			}
			_ = Run(ctx, dir, "branch", "-M", "main")
		}
	}
	if err := WriteManagedGitignore(dir); err != nil && !errors.Is(err, ErrUserEdited) {
		return Status{}, fmt.Errorf("write .gitignore: %w", err)
	}
	if err := WriteManagedGitattributes(dir); err != nil && !errors.Is(err, ErrUserEdited) {
		return Status{}, fmt.Errorf("write .gitattributes: %w", err)
	}
	if err := registerMemoryMergeDriver(ctx, dir); err != nil {
		return Status{}, err
	}
	// Make a baseline commit if there's nothing yet.
	st := Run(ctx, dir, "rev-parse", "HEAD")
	if st.Failed() {
		if err := stageAndCommit(ctx, dir, ""); err != nil {
			return Status{}, err
		}
	}
	return mustStatus(ctx, dir), nil
}

// Clone runs `git clone <url> <dir>`. dir must NOT already exist (or
// must be empty) — git refuses otherwise. The managed .gitignore /
// .gitattributes are not written here because they should come from
// the remote already. If the remote DOESN'T have them, the next
// Sync() call will add them.
func Clone(ctx context.Context, url, dir string) error {
	if err := ValidateRemoteURL(url); err != nil {
		return err
	}
	r := Run(ctx, "", "clone", url, dir)
	if r.Failed() {
		return fmt.Errorf("git clone failed: %s", UserError(r))
	}
	// Register the merge driver locally — the clone gets a fresh
	// .git/config that won't have our driver registered.
	if err := registerMemoryMergeDriver(ctx, dir); err != nil {
		return err
	}
	// First-clone onboarding: merge the remote's state snapshot into
	// this machine's live DB so chats/agents/settings show up immediately.
	if err := importState(ctx, dir); err != nil {
		return fmt.Errorf("restore cloned PrAImate state: %w", err)
	}
	return nil
}

// LsRemote is the "test connection" probe. Lightweight: just lists
// refs from the remote without cloning. Returns the default branch
// name on success (typically "main" or "master") + nil. Caller treats
// nil as "connection works."
//
// We do NOT call any HTTP API — `git ls-remote` uses the git smart
// protocol over the URL's transport (https / ssh / git://), which is
// exactly what `git clone` would use.
func LsRemote(ctx context.Context, url string) (defaultBranch string, err error) {
	if err := ValidateRemoteURL(url); err != nil {
		return "", err
	}
	// Short timeout — network hangs shouldn't lock the wizard.
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	r := Run(cctx, "", "ls-remote", "--symref", url, "HEAD")
	if r.Failed() {
		return "", classifyLsRemoteError(r)
	}
	// Output:
	//   ref: refs/heads/main	HEAD
	//   <hash>\trefs/heads/main
	for _, line := range strings.Split(r.Stdout, "\n") {
		if strings.HasPrefix(line, "ref: ") {
			rest := strings.TrimPrefix(line, "ref: ")
			ref := strings.SplitN(rest, "\t", 2)[0]
			defaultBranch = strings.TrimPrefix(ref, "refs/heads/")
			return defaultBranch, nil
		}
	}
	return "main", nil
}

// classifyLsRemoteError turns git's stderr into a user-friendly message.
func classifyLsRemoteError(r Result) error {
	msg := strings.ToLower(r.Stderr)
	switch {
	case strings.Contains(msg, "could not resolve host"),
		strings.Contains(msg, "name or service not known"):
		return errors.New("can't reach the remote host (DNS failed — check the URL and your network)")
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no route to host"):
		return errors.New("can't connect to the remote host (network unreachable or port closed)")
	case strings.Contains(msg, "authentication failed"),
		strings.Contains(msg, "permission denied"),
		strings.Contains(msg, "could not read username"),
		strings.Contains(msg, "fatal: authentication"),
		strings.Contains(msg, "publickey"):
		return errors.New("authentication failed — check your git credentials (https credential helper / ssh key)")
	case strings.Contains(msg, "repository not found"),
		strings.Contains(msg, "not found"):
		return errors.New("repository not found (URL wrong, or it's private and your credentials aren't configured)")
	default:
		return errors.New(UserError(r))
	}
}

// AddRemote sets origin to url. Replaces any existing origin so a
// user can re-link a different remote without manual git ops.
func AddRemote(ctx context.Context, dir, url string) error {
	if err := ValidateRemoteURL(url); err != nil {
		return err
	}
	// Remove any existing origin first (don't care if it didn't exist).
	_ = Run(ctx, dir, "remote", "remove", "origin")
	r := Run(ctx, dir, "remote", "add", "origin", url)
	if r.Failed() {
		return fmt.Errorf("git remote add origin: %s", UserError(r))
	}
	return nil
}

// ValidateRemoteURL rejects HTTP(S) userinfo because Git persists remote URLs
// verbatim in .git/config and PrAImate also remembers the URL in config.json.
// Authentication must come from a credential helper, SSH agent, or key file.
func ValidateRemoteURL(raw string) error {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid git remote URL: %w", err)
	}
	if u.User != nil {
		return errors.New("git remote URLs must not contain a username, password, or token — use Git Credential Manager, a credential helper, or SSH instead")
	}
	return nil
}

// RemoveRemote unlinks the local repo from its remote. The repo
// itself (commits, working tree) is untouched.
func RemoveRemote(ctx context.Context, dir string) error {
	r := Run(ctx, dir, "remote", "remove", "origin")
	if r.Failed() && !strings.Contains(strings.ToLower(r.Stderr), "no such remote") {
		return fmt.Errorf("git remote remove: %s", UserError(r))
	}
	return nil
}

// Fetch pulls remote refs into local refs/remotes/ without modifying
// the working tree. Required before Status can report ahead/behind.
func Fetch(ctx context.Context, dir string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r := Run(cctx, dir, "fetch", "origin", "--prune")
	if r.Failed() {
		return fmt.Errorf("git fetch failed: %s", UserError(r))
	}
	return nil
}

// CurrentStatus inspects the repo state. Always tries to fetch first
// so ahead/behind numbers are fresh. Returns a usable (zero-value)
// Status even when init/fetch fails so the screen can render
// gracefully.
func CurrentStatus(ctx context.Context, dir string) (Status, error) {
	if !IsGitRepo(dir) {
		return Status{}, nil
	}
	st := Status{Initialized: true}
	// Remote URL.
	if r := Run(ctx, dir, "config", "--get", "remote.origin.url"); !r.Failed() {
		st.HasRemote = true
		st.RemoveTrailing(&r.Stdout)
		st.RemoteURL = strings.TrimSpace(r.Stdout)
	}
	// Current branch (fall back to "main" if detached).
	if r := Run(ctx, dir, "symbolic-ref", "--short", "HEAD"); !r.Failed() {
		st.DefaultBranch = strings.TrimSpace(r.Stdout)
	} else {
		st.DefaultBranch = "main"
	}
	// Clean working tree?
	if r := Run(ctx, dir, "status", "--porcelain"); !r.Failed() {
		st.Clean = strings.TrimSpace(r.Stdout) == ""
	}
	// Last local commit.
	if r := Run(ctx, dir, "log", "-1", "--format=%h %ad %s", "--date=iso-strict"); !r.Failed() {
		parts := strings.SplitN(strings.TrimSpace(r.Stdout), " ", 3)
		if len(parts) >= 3 {
			st.LastCommit = parts[0] + " " + parts[2]
			if t, err := time.Parse(time.RFC3339, parts[1]); err == nil {
				st.LastCommitTime = t
			}
		}
	}
	// Ahead/behind vs origin/<branch>. Requires a successful fetch
	// first; we attempt it but tolerate failure (offline mode).
	if st.HasRemote {
		_ = Fetch(ctx, dir) // best-effort
		spec := "origin/" + st.DefaultBranch
		if r := Run(ctx, dir, "rev-list", "--left-right", "--count", "HEAD..."+spec); !r.Failed() {
			fields := strings.Fields(strings.TrimSpace(r.Stdout))
			if len(fields) == 2 {
				st.Ahead, _ = strconv.Atoi(fields[0])
				st.Behind, _ = strconv.Atoi(fields[1])
			}
		}
	}
	return st, nil
}

// RemoveTrailing trims a trailing newline from s (in-place via
// pointer because tests often pass struct pointers around).
func (Status) RemoveTrailing(s *string) {
	*s = strings.TrimRight(*s, "\n\r")
}

// Sync is the auto-decide flow: fetch, then choose ff-push, ff-pull,
// already-in-sync, or surface the divergence to the caller. Returns
// the resulting Status (after any successful fast-forward) and a
// `recommended` action when divergence is detected. Caller's job to
// drive the popup; this function makes no mutating decisions on
// divergence.
type SyncAction string

const (
	SyncActionInSync          SyncAction = "in_sync"
	SyncActionPushed          SyncAction = "pushed"
	SyncActionPulled          SyncAction = "pulled"
	SyncActionNeedsResolution SyncAction = "diverged"
	SyncActionNoRemote        SyncAction = "no_remote"
)

// Sync performs the safe (fast-forward only) sync flow. On any
// non-trivial state (diverged, working tree dirty, no remote) it
// returns SyncAction* describing what the caller should do next.
//
// Workflow:
//   - Stage + commit any local changes first (so "ahead" reflects
//     reality after the user touched MEMORY.md / chats etc.).
//   - Fetch to refresh ahead/behind counters.
//   - If both 0 → SyncActionInSync.
//   - If LocalAheadOnly → push, return SyncActionPushed.
//   - If RemoteAheadOnly → pull --ff-only, return SyncActionPulled.
//   - If diverged → return SyncActionNeedsResolution with the
//     Status; caller opens the merge/rebase/push/reset popup.
func Sync(ctx context.Context, dir string) (SyncAction, Status, error) {
	if !IsGitRepo(dir) {
		return SyncActionNoRemote, Status{}, errors.New("workspaces root isn't a git repo — initialise it in the Backup tab first")
	}
	st, _ := CurrentStatus(ctx, dir)
	if !st.HasRemote {
		return SyncActionNoRemote, st, nil
	}
	// Commit any local changes first. Always run the stage step — even
	// on a clean working tree the DB may have changed since the last
	// snapshot, and stageAndCommit re-exports state before staging
	// (it's a no-op commit-wise when nothing actually changed).
	if err := stageAndCommit(ctx, dir, ""); err != nil {
		return "", st, err
	}
	st, _ = CurrentStatus(ctx, dir)
	// Detect "remote branch doesn't exist yet" (initial push case).
	// `git rev-list HEAD...origin/<branch>` fails with unknown ref →
	// CurrentStatus reports Ahead/Behind both 0, which would make us
	// think we're in sync. Push -u to bootstrap.
	if remoteBranchAbsent(ctx, dir, st.DefaultBranch) {
		if err := pushWithUpstream(ctx, dir, st.DefaultBranch); err != nil {
			return "", st, err
		}
		st, _ = CurrentStatus(ctx, dir)
		return SyncActionPushed, st, nil
	}

	switch {
	case st.InSync():
		return SyncActionInSync, st, nil
	case st.LocalAheadOnly():
		if err := Push(ctx, dir, false); err != nil {
			return "", st, err
		}
		st, _ = CurrentStatus(ctx, dir)
		return SyncActionPushed, st, nil
	case st.RemoteAheadOnly():
		if err := Pull(ctx, dir, true); err != nil {
			return "", st, err
		}
		st, _ = CurrentStatus(ctx, dir)
		return SyncActionPulled, st, nil
	default:
		return SyncActionNeedsResolution, st, nil
	}
}

// remoteBranchAbsent reports whether origin/<branch> doesn't yet
// exist (an empty bare remote, or a remote that's never seen this
// branch). The check is intentionally local — no network round-trip.
func remoteBranchAbsent(ctx context.Context, dir, branch string) bool {
	if branch == "" {
		branch = "main"
	}
	r := Run(ctx, dir, "rev-parse", "--verify", "origin/"+branch)
	return r.Failed()
}

// pushWithUpstream is the first-push bootstrap: `git push -u origin <branch>`.
func pushWithUpstream(ctx context.Context, dir, branch string) error {
	if branch == "" {
		branch = "main"
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	r := Run(cctx, dir, "push", "-u", "origin", branch)
	if r.Failed() {
		return fmt.Errorf("git push -u origin %s failed: %s", branch, UserError(r))
	}
	return nil
}

// Push runs `git push origin <branch>` (or `--force` when force).
func Push(ctx context.Context, dir string, force bool) error {
	st, _ := CurrentStatus(ctx, dir)
	branch := st.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	args := []string{"push", "origin", branch}
	if force {
		args = append(args, "--force-with-lease")
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	r := Run(cctx, dir, args...)
	if r.Failed() {
		return fmt.Errorf("git push failed: %s", UserError(r))
	}
	return nil
}

// Pull runs `git pull origin <branch>` (with `--ff-only` when ffOnly).
func Pull(ctx context.Context, dir string, ffOnly bool) error {
	st, _ := CurrentStatus(ctx, dir)
	branch := st.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	args := []string{"pull", "origin", branch}
	if ffOnly {
		args = append(args, "--ff-only")
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	r := Run(cctx, dir, args...)
	if r.Failed() {
		return fmt.Errorf("git pull failed: %s", UserError(r))
	}
	// Remote content just landed — merge its state snapshot into the
	// live DB / config.
	if err := importState(ctx, dir); err != nil {
		return fmt.Errorf("restore pulled PrAImate state: %w", err)
	}
	return nil
}

// MergeFromRemote runs `git merge origin/<branch>` (no fast-forward
// arg — git's default). Histories may be unrelated when Settings attaches a
// populated local workspace to an existing backup for the first time, so the
// explicit user-selected merge permits that initial join. Returns nil on clean merge; an error
// containing "conflict" when there are merge conflicts.
func MergeFromRemote(ctx context.Context, dir string) error {
	st, _ := CurrentStatus(ctx, dir)
	branch := st.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	r := Run(ctx, dir, backupIdentityArgs("merge", "origin/"+branch, "--no-edit", "--allow-unrelated-histories")...)
	if r.Failed() {
		if strings.Contains(strings.ToLower(r.CombinedOutput()), "conflict") {
			return fmt.Errorf("merge conflict: %s", r.CombinedOutput())
		}
		return fmt.Errorf("git merge failed: %s", UserError(r))
	}
	if err := importState(ctx, dir); err != nil {
		return fmt.Errorf("restore merged PrAImate state: %w", err)
	}
	return nil
}

// RebaseOntoRemote runs `git rebase origin/<branch>`.
func RebaseOntoRemote(ctx context.Context, dir string) error {
	st, _ := CurrentStatus(ctx, dir)
	branch := st.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	r := Run(ctx, dir, backupIdentityArgs("rebase", "origin/"+branch)...)
	if r.Failed() {
		if strings.Contains(strings.ToLower(r.CombinedOutput()), "conflict") {
			return fmt.Errorf("rebase conflict: %s", r.CombinedOutput())
		}
		return fmt.Errorf("git rebase failed: %s", UserError(r))
	}
	if err := importState(ctx, dir); err != nil {
		return fmt.Errorf("restore rebased PrAImate state: %w", err)
	}
	return nil
}

// AbortMerge runs `git merge --abort`, useful when the user wants out
// of a conflicted merge state. Idempotent — if no merge is in flight
// the command exits non-zero but we treat that as success.
func AbortMerge(ctx context.Context, dir string) {
	_ = Run(ctx, dir, "merge", "--abort")
	_ = Run(ctx, dir, "rebase", "--abort")
}

// ResetHardToRemote runs `git fetch && git reset --hard origin/<branch>`.
// This DELETES local commits + uncommitted changes. Caller has
// already done the double-confirm dance.
func ResetHardToRemote(ctx context.Context, dir string) error {
	if err := Fetch(ctx, dir); err != nil {
		return err
	}
	st, _ := CurrentStatus(ctx, dir)
	branch := st.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	r := Run(ctx, dir, "reset", "--hard", "origin/"+branch)
	if r.Failed() {
		return fmt.Errorf("git reset --hard failed: %s", UserError(r))
	}
	// The git history was reset to remote, but the live DB still holds
	// this machine's rows — the import row-merges the remote snapshot
	// on top, so "reset" loses git history without losing local chats.
	if err := importState(ctx, dir); err != nil {
		return fmt.Errorf("restore reset PrAImate state: %w", err)
	}
	return nil
}

// CommitLocalChanges stages chats/ + templates/ and commits with the
// timestamped message. Returns nil when nothing was staged (clean
// tree). Used by Sync + the explicit "force push" path.
func CommitLocalChanges(ctx context.Context, dir, extraSummary string) error {
	return stageAndCommit(ctx, dir, extraSummary)
}

// LastCommitMachineID reads the trailer "Machine-ID: …" from the
// remote's most-recent commit. Used by the auto-sync safeguard to
// refuse force-push when another machine pushed within the last 24h.
// Returns ("", "", nil) when no Machine-ID is present.
func LastCommitMachineID(ctx context.Context, dir string) (id string, when time.Time, err error) {
	if err := Fetch(ctx, dir); err != nil {
		return "", time.Time{}, err
	}
	st, _ := CurrentStatus(ctx, dir)
	branch := st.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	r := Run(ctx, dir, "log", "-1", "--format=%(trailers:key=Machine-ID,valueonly,separator=)%n%aI", "origin/"+branch)
	if r.Failed() {
		return "", time.Time{}, fmt.Errorf("read trailers from origin/%s: %s", branch, UserError(r))
	}
	lines := strings.SplitN(strings.TrimSpace(r.Stdout), "\n", 2)
	if len(lines) < 2 {
		return "", time.Time{}, nil
	}
	id = strings.TrimSpace(lines[0])
	when, _ = time.Parse(time.RFC3339, strings.TrimSpace(lines[1]))
	return id, when, nil
}

// -----------------------------------------------------------------------------
// internal helpers
// -----------------------------------------------------------------------------

func mustStatus(ctx context.Context, dir string) Status {
	st, _ := CurrentStatus(ctx, dir)
	return st
}

// stageAndCommit runs `git add chats/ templates/` then commits with
// the standard timestamped message. extraSummary, when non-empty,
// becomes the commit body (under the subject line).
//
// Returns nil when there's nothing to stage. Sets a machine-id
// trailer when PRAIMATE_BACKUP_MACHINE_ID is set in the env (the
// auto-sync code sets it).
func stageAndCommit(ctx context.Context, dir, extraSummary string) error {
	// Always (re-)write the managed metadata first — picks up the
	// case where the user manually deleted .gitignore.
	_ = WriteManagedGitignore(dir)
	_ = WriteManagedGitattributes(dir)
	// Make sure the tracked subdirs exist so git add doesn't
	// error on first-init repos that haven't created anything yet.
	for _, sub := range []string{"chats", "templates", ".praimate-state"} {
		_ = os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}
	// Snapshot the live DB + shareable config into .praimate-state/ so
	// the commit carries them. Never commit a stale snapshot.
	if err := exportState(ctx, dir); err != nil {
		return fmt.Errorf("export PrAImate state: %w", err)
	}
	// Stage. `--all` flag picks up deletions too.
	r := Run(ctx, dir, "add", "--all", ".gitignore", ".gitattributes", "chats", "templates", ".praimate-state")
	if r.Failed() {
		return fmt.Errorf("git add: %s", UserError(r))
	}
	// Anything to commit?
	r = Run(ctx, dir, "diff", "--cached", "--quiet")
	if r.ExitCode == 0 {
		return nil // clean
	}
	if r.Err == nil { // ExitCode != 0 with no err: git diff --quiet's "things differ" signal
		// fall through to commit
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	msg := "praimate backup " + ts
	if extraSummary != "" {
		msg += "\n\n" + extraSummary
	}
	if mid := strings.TrimSpace(os.Getenv("PRAIMATE_BACKUP_MACHINE_ID")); mid != "" {
		msg += "\n\nMachine-ID: " + mid
	}
	// Supply a repository-independent committer identity for PrAImate-owned
	// backup commits. --author only covers the author; Git still refuses to
	// commit when the user's global committer identity is unset.
	args := backupIdentityArgs("commit", "-m", msg, "--author=PrAImate <praimate@local>")
	if r := Run(ctx, dir, args...); r.Failed() {
		return fmt.Errorf("git commit: %s", UserError(r))
	}
	return nil
}

func backupIdentityArgs(args ...string) []string {
	return append([]string{
		"-c", "user.name=PrAImate",
		"-c", "user.email=praimate@local",
	}, args...)
}

// registerMemoryMergeDriver wires the MEMORY.md custom merge driver
// into the repo's local config so .gitattributes' `merge=clade-memory`
// hint resolves to a working program. The driver shells back to
// `praimate --merge-memory %O %A %B` which we expose as a top-level flag.
//
// Best-effort: a missing `praimate` on PATH doesn't fail the init; git
// just falls back to standard textual merge (which produces conflict
// markers), and the conflict popup in the Backup screen takes over.
func registerMemoryMergeDriver(ctx context.Context, dir string) error {
	// The state-snapshot driver needs no external binary — register it
	// unconditionally. "Theirs" (remote wins in the working tree) is
	// correct for .praimate-state/: the remote snapshot gets row-merged
	// into the live DB by importState, so local rows survive in the DB
	// and the next export re-commits the union. Without this driver a
	// binary db.sqlite conflict would wedge every two-host merge.
	for k, v := range map[string]string{
		"merge.praimate-theirs.name":   "PrAImate state snapshot (remote wins; DB row-merge follows)",
		"merge.praimate-theirs.driver": "cp %B %A",
	} {
		if r := Run(ctx, dir, "config", "--local", k, v); r.Failed() {
			return fmt.Errorf("git config %s: %s", k, UserError(r))
		}
	}
	cladePath, err := exec.LookPath("praimate")
	if err != nil {
		// Best-effort. Skip the memory merge driver but don't fail init.
		return nil
	}
	for k, v := range map[string]string{
		"merge.clade-memory.name":   "PrAImate MEMORY.md concatenation merge", // id kept for existing repos
		"merge.clade-memory.driver": cladePath + " --merge-memory %O %A %B",
	} {
		if r := Run(ctx, dir, "config", "--local", k, v); r.Failed() {
			return fmt.Errorf("git config %s: %s", k, UserError(r))
		}
	}
	return nil
}
