// Package skills fetches "online skills" — Claude-Code-style skill bundles
// hosted on a remote — into a target directory the host CLI can load.
//
// Two transports are supported:
//
//   - **git** (default): any URL git itself accepts as a remote
//     (https, ssh, git@host:path, file://, local path).
//   - **zip**: any http(s) URL ending in .zip. Downloaded with the
//     stdlib http client, extracted with archive/zip. GitHub-style
//     archives where the whole tree lives under a single top-level
//     directory get flattened so the skill files land at the root
//     of the target dir.
//
// The transport is picked automatically from the URL — the user just
// pastes whichever link a skill author handed them.
package skills

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/gitutil"
)

// FetchResult records what happened to one skill URL.
type FetchResult struct {
	URL  string
	Path string // local dir the skill landed in (empty on failure)
	Err  error
}

// FetchAll clones every URL into targetRoot/<derived-name>/. Skips URLs
// whose target dir already exists (idempotent across launches). Returns
// one result per input; never errors as a whole.
func FetchAll(ctx context.Context, urls []string, targetRoot string) []FetchResult {
	results := make([]FetchResult, 0, len(urls))
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		results = append(results, fetchOne(ctx, u, targetRoot))
	}
	return results
}

func fetchOne(ctx context.Context, raw, targetRoot string) FetchResult {
	name, err := DeriveName(raw)
	if err != nil {
		return FetchResult{URL: raw, Err: err}
	}
	dst := filepath.Join(targetRoot, name)
	if _, err := os.Stat(dst); err == nil {
		// Already fetched — treat as success without re-downloading.
		return FetchResult{URL: raw, Path: dst}
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return FetchResult{URL: raw, Err: err}
	}

	if isZipURL(raw) {
		if err := fetchZip(ctx, raw, dst); err != nil {
			_ = os.RemoveAll(dst)
			return FetchResult{URL: raw, Err: err}
		}
		return FetchResult{URL: raw, Path: dst}
	}

	// Default transport: git clone.
	gitArgs := gitutil.DisableSSLVerifyForInternalHost("clone", "--depth=1", raw, dst)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Clean up partial clone if any so a retry doesn't get stuck on
		// "already exists".
		_ = os.RemoveAll(dst)
		return FetchResult{URL: raw, Err: fmt.Errorf("git clone failed: %v: %s", err, strings.TrimSpace(string(out)))}
	}
	return FetchResult{URL: raw, Path: dst}
}

// isZipURL returns true when raw looks like an http(s) URL ending in
// .zip (case-insensitive). Query strings + fragments are tolerated so
// e.g. `?token=...` after the path still picks zip as the transport.
func isZipURL(raw string) bool {
	if !hasURLScheme(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	p := strings.ToLower(u.Path)
	return strings.HasSuffix(p, ".zip")
}

// fetchZip downloads raw and unpacks it into dst. Caller has already
// guaranteed dst doesn't exist. On any error the function leaves dst
// in whatever state it managed to reach — callers nuke it.
func fetchZip(ctx context.Context, raw, dst string) error {
	// We need a Reader-at-offset to feed archive/zip, so download to a
	// tempfile rather than buffering the whole thing in memory.
	tmp, err := os.CreateTemp("", "praimate-skill-*.zip")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return fmt.Errorf("zip request: %w", err)
	}
	// Friendly UA so GitHub etc. don't deny us. Default Go UA is fine
	// but a named one helps the operator spot us in logs.
	req.Header.Set("User-Agent", "praimate-skills/1.0")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("zip download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("zip download: HTTP %d %s", resp.StatusCode, resp.Status)
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return fmt.Errorf("zip download body: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("zip close: %w", err)
	}

	zr, err := zip.OpenReader(tmpPath)
	if err != nil {
		return fmt.Errorf("zip open: %w", err)
	}
	defer zr.Close()

	// GitHub archive downloads put every file under a single top-level
	// directory (e.g. `myrepo-main/SKILL.md`). Detect that case so we
	// flatten — the result should look like a plain skill folder.
	prefix := commonTopDir(zr.File)

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}

	for _, f := range zr.File {
		// Validate the raw entry name FIRST — before prefix stripping
		// can launder a "../" out of view. Otherwise an attacker could
		// craft a zip whose only top-level dir is ".." and trick our
		// flatten-the-prefix logic into producing innocuous-looking
		// post-strip names.
		rawNorm := strings.ReplaceAll(f.Name, "\\", "/")
		for _, part := range strings.Split(rawNorm, "/") {
			if part == ".." {
				return fmt.Errorf("zip entry escapes target dir: %s", f.Name)
			}
		}
		name := f.Name
		if prefix != "" {
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "" {
			continue
		}
		if err := extractZipEntry(f, dst, name); err != nil {
			return err
		}
	}
	return nil
}

// commonTopDir returns the single top-level directory shared by all
// entries in the archive, or "" if there isn't one. Used to flatten
// GitHub archive downloads.
func commonTopDir(files []*zip.File) string {
	if len(files) == 0 {
		return ""
	}
	var top string
	for i, f := range files {
		// Normalize path separators — zips can carry either.
		name := strings.ReplaceAll(f.Name, "\\", "/")
		slash := strings.Index(name, "/")
		if slash < 0 {
			// Entry at the root → no common prefix.
			return ""
		}
		cand := name[:slash+1] // include trailing slash so TrimPrefix works
		if i == 0 {
			top = cand
			continue
		}
		if cand != top {
			return ""
		}
	}
	return top
}

// extractZipEntry writes one zip entry into dstRoot/relName. Refuses
// to follow ".." path components so a malicious archive can't write
// outside dstRoot (the "Zip Slip" CVE).
func extractZipEntry(f *zip.File, dstRoot, relName string) error {
	// Sanitise: reject absolute paths and any segment that's ".."
	relName = strings.TrimPrefix(relName, "/")
	if filepath.IsAbs(relName) {
		return fmt.Errorf("zip entry has absolute path: %s", f.Name)
	}
	for _, part := range strings.Split(filepath.ToSlash(relName), "/") {
		if part == ".." {
			return fmt.Errorf("zip entry escapes target dir: %s", f.Name)
		}
	}

	out := filepath.Join(dstRoot, relName)
	// Belt-and-braces: confirm the resolved path is still under root
	// after Join (catches edge cases with weird unicode separators).
	absRoot, _ := filepath.Abs(dstRoot)
	absOut, _ := filepath.Abs(out)
	if !strings.HasPrefix(absOut, absRoot) {
		return fmt.Errorf("zip entry escapes target dir: %s", f.Name)
	}

	if f.FileInfo().IsDir() {
		return os.MkdirAll(out, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	// Preserve the executable bit if the archive carries one; 0644
	// otherwise. archive/zip exposes Unix permissions via Mode().
	mode := f.Mode().Perm() | 0o600
	if mode == 0 {
		mode = 0o644
	}
	dst, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, rc); err != nil {
		return err
	}
	return nil
}

// DeriveName turns a git URL into a sensible directory name. Mostly the
// repo's last path segment with `.git` stripped.
//
// Examples:
//
//	https://github.com/juliusbrussee/caveman.git → caveman
//	git@github.com:org/Some-Repo.git            → some-repo
//	file:///tmp/myrepo                          → myrepo
//	C:\Users\me\repos\myrepo                    → myrepo
func DeriveName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	// Handle scp-style git@host:path
	if strings.HasPrefix(raw, "git@") && strings.Contains(raw, ":") {
		parts := strings.SplitN(raw, ":", 2)
		raw = parts[1]
	} else if hasURLScheme(raw) {
		if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
			raw = u.Path
		}
	}
	// Normalize separator so filepath.Base works on both flavors.
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = strings.TrimRight(raw, "/")
	base := raw
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		base = raw[i+1:]
	}
	base = strings.TrimSuffix(base, ".git")
	base = strings.TrimSuffix(base, ".zip")
	base = strings.TrimSuffix(base, ".ZIP")
	base = strings.ToLower(base)
	base = nonSlug.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" || base == "." {
		return "", fmt.Errorf("cannot derive name from %q", raw)
	}
	return base, nil
}

// hasURLScheme returns true only for the schemes we actually expect for
// git remotes, so a Windows path like "C:\foo" isn't misread as a URL
// (Go's url.Parse will happily treat "C" as a scheme).
func hasURLScheme(s string) bool {
	for _, p := range []string{"http://", "https://", "ssh://", "git://", "file://"} {
		if strings.HasPrefix(strings.ToLower(s), p) {
			return true
		}
	}
	return false
}

var nonSlug = regexp.MustCompile(`[^a-z0-9_-]+`)
