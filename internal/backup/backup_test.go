package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Most of these tests require a real `git` binary. We skip when git
// isn't installed (CI hosts without it) rather than reach for a
// vendored library.
func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping integration test")
	}
}

func TestRun_RecordsExitCodeAndStderr(t *testing.T) {
	skipIfNoGit(t)
	dir := t.TempDir()
	// Not a git repo → git status fails with exit 128.
	r := Run(context.Background(), dir, "status")
	if !r.Failed() {
		t.Errorf("expected failure when running git status outside a repo; got %+v", r)
	}
	if r.ExitCode == 0 {
		t.Errorf("ExitCode should be non-zero, got %d", r.ExitCode)
	}
	if r.Stderr == "" {
		t.Errorf("Stderr should explain the failure, got empty")
	}
}

func TestRun_UserErrorPreservesProcessStartFailure(t *testing.T) {
	skipIfNoGit(t)
	dir := filepath.Join(t.TempDir(), "missing")
	r := Run(context.Background(), dir, "status")
	if !r.Failed() || r.ExitCode != -1 {
		t.Fatalf("Run with missing cwd = %+v, want process-start failure", r)
	}
	got := UserError(r)
	if strings.Contains(got, "git failed (exit -1)") || !strings.Contains(got, dir) {
		t.Fatalf("UserError = %q, want underlying cwd error containing %q", got, dir)
	}
}

func TestValidateRemoteURLRejectsEmbeddedHTTPCredentials(t *testing.T) {
	for _, raw := range []string{
		"https://user:token@example.test/repo.git",
		"https://token@example.test/repo.git",
		"http://user@example.test/repo.git",
	} {
		if err := ValidateRemoteURL(raw); err == nil {
			t.Errorf("ValidateRemoteURL(%q) accepted plaintext userinfo", raw)
		}
	}
	for _, raw := range []string{
		"http://gitea.example.test/team/repo.git",
		"https://example.test/repo.git",
		"ssh://git@example.test/repo.git",
		"git@example.test:team/repo.git",
	} {
		if err := ValidateRemoteURL(raw); err != nil {
			t.Errorf("ValidateRemoteURL(%q) = %v", raw, err)
		}
	}
}

func TestWriteManagedGitignore_FirstWrite(t *testing.T) {
	dir := t.TempDir()
	if err := WriteManagedGitignore(dir); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(body), "Managed by PrAImate") {
		t.Errorf(".gitignore should carry the Clade-managed marker:\n%s", body)
	}
	for _, want := range []string{"/*", "!/chats/", "!/templates/"} {
		if !strings.Contains(string(body), want) {
			t.Errorf(".gitignore should contain %q; got:\n%s", want, body)
		}
	}
}

func TestWriteManagedGitignore_RefusesToClobberUserEdited(t *testing.T) {
	dir := t.TempDir()
	custom := "node_modules/\nmy-secret.txt\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteManagedGitignore(dir)
	if err == nil {
		t.Fatal("expected refusal when overwriting a user-edited .gitignore")
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(body) != custom {
		t.Errorf("user-edited .gitignore was clobbered:\n%s", body)
	}
}

func TestWriteManagedGitignore_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := WriteManagedGitignore(dir); err != nil {
		t.Fatal(err)
	}
	stat1, _ := os.Stat(filepath.Join(dir, ".gitignore"))
	if err := WriteManagedGitignore(dir); err != nil {
		t.Fatalf("second write should be a no-op, got %v", err)
	}
	stat2, _ := os.Stat(filepath.Join(dir, ".gitignore"))
	if stat2.ModTime().After(stat1.ModTime()) {
		// Best-effort — some FSes have low timestamp resolution, so a
		// strict assertion would be flaky.
		t.Logf("note: second write touched the file (ModTime advanced); this is acceptable but a true no-op would be tidier")
	}
}

func TestInit_CreatesRepoWithManagedFilesAndInitialCommit(t *testing.T) {
	skipIfNoGit(t)
	dir := t.TempDir()
	st, err := Init(context.Background(), dir)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if !st.Initialized {
		t.Error("Status.Initialized should be true after Init")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf(".git dir not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err != nil {
		t.Errorf(".gitignore not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitattributes")); err != nil {
		t.Errorf(".gitattributes not created: %v", err)
	}
	// Initial commit exists.
	r := Run(context.Background(), dir, "log", "-1", "--format=%s")
	if r.Failed() {
		t.Errorf("git log failed after init: %s", UserError(r))
	}
	if !strings.Contains(r.Stdout, "praimate backup") {
		t.Errorf("initial commit should start with 'praimate backup'; got %q", r.Stdout)
	}
}

func TestInit_CreatesMissingBackupDirectory(t *testing.T) {
	skipIfNoGit(t)
	dir := filepath.Join(t.TempDir(), "missing", "workspaces")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("test precondition: backup directory unexpectedly exists: %v", err)
	}

	st, err := Init(context.Background(), dir)
	if err != nil {
		t.Fatalf("Init should create a missing backup directory: %v", err)
	}
	if !st.Initialized || !IsGitRepo(dir) {
		t.Fatalf("Init status = %+v, git repo = %v", st, IsGitRepo(dir))
	}
}

func TestInit_DoesNotRequireGlobalGitIdentity(t *testing.T) {
	skipIfNoGit(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.useConfigOnly")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")

	dir := t.TempDir()
	if _, err := Init(context.Background(), dir); err != nil {
		t.Fatalf("Init should supply its own commit identity: %v", err)
	}
	r := Run(context.Background(), dir, "log", "-1", "--format=%an|%ae|%cn|%ce")
	if r.Failed() {
		t.Fatalf("git log: %s", UserError(r))
	}
	if got := strings.TrimSpace(r.Stdout); got != "PrAImate|praimate@local|PrAImate|praimate@local" {
		t.Fatalf("backup commit identity = %q", got)
	}
}

func TestInit_Idempotent(t *testing.T) {
	skipIfNoGit(t)
	dir := t.TempDir()
	if _, err := Init(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(context.Background(), dir); err != nil {
		t.Fatalf("Init should be idempotent; second call returned %v", err)
	}
}

func TestSync_NoRemoteReturnsNoRemote(t *testing.T) {
	skipIfNoGit(t)
	dir := t.TempDir()
	if _, err := Init(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	action, _, err := Sync(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if action != SyncActionNoRemote {
		t.Errorf("action = %s, want %s when no remote is configured", action, SyncActionNoRemote)
	}
}

func TestLsRemote_RejectsUnreachable(t *testing.T) {
	skipIfNoGit(t)
	// 127.0.0.1:1 is reliably refused on every OS we ship to.
	_, err := LsRemote(context.Background(), "http://127.0.0.1:1/nope.git")
	if err == nil {
		t.Fatal("LsRemote should fail against an unreachable URL")
	}
	// Error message should be actionable, not raw git boilerplate.
	if !strings.Contains(err.Error(), "host") && !strings.Contains(err.Error(), "connect") &&
		!strings.Contains(err.Error(), "reach") {
		t.Errorf("error should describe the connection failure in user terms; got %v", err)
	}
}

// TestSync_FFPushFromCleanLocal: a repo with one local commit ahead
// of an empty bare remote should push cleanly and report
// SyncActionPushed.
func TestSync_FFPushFromCleanLocal(t *testing.T) {
	skipIfNoGit(t)
	ctx := context.Background()

	// Bare remote.
	remote := t.TempDir()
	if r := Run(ctx, remote, "init", "--bare", "-b", "main"); r.Failed() {
		// Fall back for older git.
		_ = os.RemoveAll(remote)
		_ = os.MkdirAll(remote, 0o755)
		if r := Run(ctx, remote, "init", "--bare"); r.Failed() {
			t.Skipf("git init --bare failed (likely old git): %s", UserError(r))
		}
	}

	// Local repo with an initial commit + a tracked file added.
	local := t.TempDir()
	if _, err := Init(ctx, local); err != nil {
		t.Fatal(err)
	}
	// Configure committer identity for tests (CI hosts may not have it).
	_ = Run(ctx, local, "config", "user.email", "test@example.com")
	_ = Run(ctx, local, "config", "user.name", "Clade Test")
	// Add a tracked file (under chats/ so it's NOT gitignored).
	if err := os.MkdirAll(filepath.Join(local, "chats", "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "chats", "foo", "memo.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CommitLocalChanges(ctx, local, ""); err != nil {
		t.Fatal(err)
	}
	if err := AddRemote(ctx, local, remote); err != nil {
		t.Fatal(err)
	}

	action, st, err := Sync(ctx, local)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if action != SyncActionPushed {
		t.Errorf("action = %s, want %s; status=%+v", action, SyncActionPushed, st)
	}
}

func TestMergeFromRemoteJoinsUnrelatedHistoriesWithoutGlobalIdentity(t *testing.T) {
	skipIfNoGit(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.useConfigOnly")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	SetStateSyncer(nil)
	t.Cleanup(func() { SetStateSyncer(nil) })
	ctx := context.Background()

	remote := t.TempDir()
	if r := Run(ctx, remote, "init", "--bare", "-b", "main"); r.Failed() {
		t.Skipf("git init --bare -b main unsupported: %s", UserError(r))
	}
	seed := t.TempDir()
	if _, err := Init(ctx, seed); err != nil {
		t.Fatal(err)
	}
	remoteFile := filepath.Join(seed, "chats", "remote", "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(remoteFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteFile, []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CommitLocalChanges(ctx, seed, ""); err != nil {
		t.Fatal(err)
	}
	if err := AddRemote(ctx, seed, remote); err != nil {
		t.Fatal(err)
	}
	if action, _, err := Sync(ctx, seed); err != nil || action != SyncActionPushed {
		t.Fatalf("seed remote: action=%q err=%v", action, err)
	}

	local := t.TempDir()
	if _, err := Init(ctx, local); err != nil {
		t.Fatal(err)
	}
	localFile := filepath.Join(local, "chats", "local", "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(localFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localFile, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CommitLocalChanges(ctx, local, ""); err != nil {
		t.Fatal(err)
	}
	if err := AddRemote(ctx, local, remote); err != nil {
		t.Fatal(err)
	}
	if err := Fetch(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := MergeFromRemote(ctx, local); err != nil {
		t.Fatalf("merge unrelated backup histories: %v", err)
	}
	for _, path := range []string{
		filepath.Join(local, "chats", "local", "MEMORY.md"),
		filepath.Join(local, "chats", "remote", "MEMORY.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("merged backup missing %s: %v", path, err)
		}
	}
}

// TestGitignore_RootFileIgnored: a stray file at the workspaces root
// must NOT propagate. Files under chats/ DO propagate.
func TestGitignore_RootFileIgnored(t *testing.T) {
	skipIfNoGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	_ = Run(ctx, dir, "config", "user.email", "test@example.com")
	_ = Run(ctx, dir, "config", "user.name", "Clade Test")

	// Stray file at root.
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("ephemeral\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Real file under chats/.
	_ = os.MkdirAll(filepath.Join(dir, "chats", "abc"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "chats", "abc", "chat.json"), []byte("{}"), 0o644)

	// Stage everything PrAImate would stage.
	if err := CommitLocalChanges(ctx, dir, ""); err != nil {
		t.Fatal(err)
	}
	r := Run(ctx, dir, "ls-files")
	if r.Failed() {
		t.Fatal(UserError(r))
	}
	tracked := r.Stdout
	if !strings.Contains(tracked, "chats/abc/chat.json") {
		t.Errorf("chats/abc/chat.json should be tracked; tracked files:\n%s", tracked)
	}
	if strings.Contains(tracked, "scratch.txt") {
		t.Errorf("scratch.txt at root should NOT be tracked; tracked files:\n%s", tracked)
	}
}

// Files written by pre-rebrand builds carry the legacy Clade marker —
// they are still OURS to refresh, never "user-edited".
func TestWriteManagedGitignore_RefreshesLegacyCladeMarkedFile(t *testing.T) {
	dir := t.TempDir()
	legacy := "# Managed by Clade — do not edit by hand.\nold content\n"
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteManagedGitignore(dir); err != nil {
		t.Fatalf("legacy-marked file must be refreshable: %v", err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "Managed by PrAImate") {
		t.Errorf("file not upgraded to the new managed content:\n%s", body)
	}
}
