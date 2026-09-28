package main

// Build-from-source — for platforms where we don't ship a prebuilt
// bundled binary (praimate-code on Windows, graphify off
// linux/amd64), the user can build it locally from our repo. The flow,
// streamed live over "praimate:install":
//
//  1. clone the PrAImate repo (shallow) into a temp dir,
//  2. run the matching build script (build-praimate-code.sh /
//     build-graphify.sh) with OUT pointed at a scratch dir,
//  3. move the produced binary into <config>/praimate/bin where the
//     resolver looks for it,
//  4. delete the temp clone so it doesn't waste disk.
//
// The build scripts are bash; on Windows they run under the bash that
// ships with Git for Windows. BuildRequirements() tells the UI which
// tools are needed and whether they're present BEFORE the user starts.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/appdata"
	"github.com/sPROFFEs/PrAImate/internal/gitutil"
	"github.com/sPROFFEs/PrAImate/internal/installer"
)

const praimateRepoURL = "https://github.com/sPROFFEs/praimate.git"

// BuildRequirement is one external tool a from-source build needs.
type BuildRequirement struct {
	Name   string `json:"name"`   // "git", "bun", "uv", "bash"
	Detail string `json:"detail"` // where to get it
	Found  bool   `json:"found"`
	Path   string `json:"path,omitempty"`
}

// BuildToolInfo is the precheck the UI shows before offering the build.
type BuildToolInfo struct {
	Tool         string             `json:"tool"`
	Label        string             `json:"label"`
	Requirements []BuildRequirement `json:"requirements"`
	Ready        bool               `json:"ready"` // every requirement found
	Note         string             `json:"note"`
}

func lookReq(name, detail string) BuildRequirement {
	r := BuildRequirement{Name: name, Detail: detail}
	if p, err := exec.LookPath(name); err == nil {
		r.Found = true
		r.Path = p
	}
	return r
}

// buildRequirementsFor returns the tool list a given target needs on
// this OS.
func buildRequirementsFor(tool string) ([]BuildRequirement, string, string, error) {
	git := lookReq("git", "https://git-scm.com/downloads")
	// bash is the system shell on Linux; on Windows it comes with
	// Git for Windows (git-bash) and must be on PATH.
	reqs := []BuildRequirement{git}
	if runtime.GOOS == "windows" {
		reqs = append(reqs, lookReq("bash", "ships with Git for Windows — reinstall Git with 'Git Bash'"))
	}
	switch tool {
	case "praimate-code":
		reqs = append(reqs, lookReq("bun", "https://bun.sh — needed to compile the OpenCode fork"))
		return reqs, "PrAImate Code", "scripts/build-praimate-code.sh", nil
	case "graphify":
		reqs = append(reqs, lookReq("uv", "https://astral.sh/uv — needed to freeze the graphify standalone"))
		return reqs, "Graphify (RAG)", "scripts/build-graphify.sh", nil
	default:
		return nil, "", "", fmt.Errorf("unknown build target %q", tool)
	}
}

// BuildRequirements reports the tools a from-source build of the given
// target needs and whether each is currently installed.
func (a *App) BuildRequirements(tool string) (*BuildToolInfo, error) {
	reqs, label, _, err := buildRequirementsFor(tool)
	if err != nil {
		return nil, err
	}
	ready := true
	for _, r := range reqs {
		if !r.Found {
			ready = false
		}
	}
	note := "This clones our repo, compiles the binary locally (this can take several minutes), installs it, and deletes the temporary checkout."
	if tool == "praimate-code" {
		note = "Compiles our OpenCode fork with Bun (~2 GB download, several minutes). The temporary checkout is deleted afterwards."
	}
	return &BuildToolInfo{Tool: tool, Label: label, Requirements: reqs, Ready: ready, Note: note}, nil
}

// BuildToolFromSource clones the repo, runs the build script, installs
// the resulting binary into <config>/praimate/bin, and cleans up.
// Output streams over "praimate:install" with cli="build:<tool>".
func (a *App) BuildToolFromSource(tool string) error {
	reqs, label, script, err := buildRequirementsFor(tool)
	if err != nil {
		return err
	}
	for _, r := range reqs {
		if !r.Found {
			return fmt.Errorf("%s is required but not found on PATH — %s", r.Name, r.Detail)
		}
	}
	binDir, err := installer.PraimateBinDir()
	if err != nil {
		return fmt.Errorf("resolve install dir: %w", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}

	w := installLogWriter{ctx: a.ctx, cli: "build:" + tool}
	emit := func(format string, args ...any) {
		_, _ = fmt.Fprintf(w, format+"\n", args...)
	}

	// /tmp is tmpfs on many Linux distros (RAM-backed, often 1–2 GB).
	// PrAImate Code's Bun toolchain alone downloads ~2 GB, so default
	// to a disk-backed cache dir per OS and let the user override with
	// PRAIMATE_BUILD_DIR for non-standard layouts.
	parent, parentNote := resolveBuildParent()
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create build cache %s: %w", parent, err)
	}
	work, err := os.MkdirTemp(parent, "praimate-build-*")
	if err != nil {
		return fmt.Errorf("scratch dir under %s: %w", parent, err)
	}
	emit("· scratch dir: %s (%s)", work, parentNote)
	defer func() {
		emit("· cleaning up temporary checkout")
		_ = os.RemoveAll(work)
	}()

	ctx, cancel := context.WithTimeout(a.ctx, 40*time.Minute)
	defer cancel()

	repo := filepath.Join(work, "PrAImate")
	emit("→ cloning %s (shallow)…", praimateRepoURL)
	gitArgs := gitutil.DisableSSLVerifyForInternalHost("clone", "--depth", "1", praimateRepoURL, repo)
	if err := runStreamed(ctx, w, work, nil, "git", gitArgs...); err != nil {
		return fmt.Errorf("clone failed: %w", err)
	}

	out := filepath.Join(work, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	emit("→ building %s from source (this can take several minutes)…", label)
	env := append(os.Environ(),
		"OUT="+out,
		"PRAIMATE_BUILD_DIR="+parent,
		"TMPDIR="+parent,
	)
	if err := runStreamed(ctx, w, repo, env, "bash", script); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	// Move the produced binary (and any sidecar license/notice) into the
	// install dir where the resolver looks.
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	var produced, dest string
	switch tool {
	case "praimate-code":
		produced = filepath.Join(out, "praimate-code"+ext)
		dest = filepath.Join(binDir, "praimate-code"+ext)
	case "graphify":
		produced = filepath.Join(out, "praimate-graphify"+ext)
		dest = filepath.Join(binDir, "graphify"+ext)
	}
	if _, err := os.Stat(produced); err != nil {
		return fmt.Errorf("build finished but %s was not produced", filepath.Base(produced))
	}
	emit("→ installing into %s", dest)
	if err := moveFile(produced, dest); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	_ = os.Chmod(dest, 0o755)
	// Carry license/notice sidecars when present (praimate-code).
	for _, side := range []string{"PRAIMATE-CODE-LICENSE", "PRAIMATE-CODE-NOTICE", "PRAIMATE-GRAPHIFY-NOTICE"} {
		src := filepath.Join(out, side)
		if _, err := os.Stat(src); err == nil {
			_ = copyFileSimple(src, filepath.Join(binDir, side))
		}
	}

	refreshManagedPaths()
	emit("✓ %s built and installed", label)
	return nil
}

// runStreamed runs a command with cwd/env, streaming stdout+stderr to w.
func runStreamed(ctx context.Context, w installLogWriter, dir string, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// moveFile renames, falling back to copy+remove across filesystems
// (temp dir and config dir are often on different mounts).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFileSimple(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

func copyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if fi, err := os.Stat(src); err == nil {
		_ = os.Chmod(tmp, fi.Mode().Perm())
	}
	return os.Rename(tmp, dst)
}

// resolveBuildParent picks a disk-backed scratch parent for the
// from-source builds. The default sits inside PrAImate's single data
// root so Settings can accurately enumerate and delete all app state:
//
//	Linux:   $XDG_CONFIG_HOME/praimate/build (else ~/.config/praimate/build)
//	Windows: %APPDATA%\praimate\build
//
// Users with non-standard layouts (encrypted home, slow disk, etc.)
// override with PRAIMATE_BUILD_DIR. We also accept TMPDIR as a fallback
// signal — if it's set and not the OS default, the user already pointed
// the world at a custom temp dir and we respect it.
//
// Returns (parentDir, oneLineDescription) for the install-log banner.
func resolveBuildParent() (string, string) {
	if v := strings.TrimSpace(os.Getenv("PRAIMATE_BUILD_DIR")); v != "" {
		return v, "PRAIMATE_BUILD_DIR override"
	}
	if root, err := appdata.Root(); err == nil {
		return filepath.Join(root, "build"), filepath.Join(root, "build")
	}
	// Last resort — the OS default temp dir. Caller will likely run out
	// of space on tmpfs-backed /tmp, but at least we won't panic on a
	// system without a home dir.
	if t := strings.TrimSpace(os.Getenv("TMPDIR")); t != "" {
		return t, "$TMPDIR"
	}
	return os.TempDir(), "os.TempDir() — may be tmpfs, expect failures on large builds"
}
