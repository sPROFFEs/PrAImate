package installer

// Native release-asset downloader. Replaces the previous PowerShell
// (Invoke-WebRequest) / bash (curl) shell-outs, which were fragile in
// exactly the ways users hit: Windows PowerShell 5.1 defaults to
// TLS 1.0 in .NET's ServicePointManager and drops GitHub's TLS 1.2-only
// CDN with "The connection was closed unexpectedly", curl isn't
// guaranteed on minimal Linuxes, and a missing asset surfaced as an
// opaque 404 mid-command. Go's net/http negotiates modern TLS natively,
// we can check the asset EXISTS in the release before downloading a
// byte, and transient network drops get retried.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/updater"
	"github.com/sPROFFEs/PrAImate/internal/version"
)

// downloadClient is the HTTP client for asset downloads. No global
// timeout — assets can be large on slow links; per-request cancellation
// comes from the caller's context instead.
var downloadClient = &http.Client{}

// downloadRetries is how many times a failed asset download is retried
// on transient errors (connection reset, truncated body) before giving
// up. API-level failures (404, missing asset) are never retried.
const downloadRetries = 2

// DownloadReleaseAsset resolves the latest GitHub release, verifies the
// named asset is actually published in it, and downloads it to destPath
// (mode 0755 — these are executables). Progress and status lines stream
// to w.
//
// Returns ErrNoPrebuiltAsset (wrapped) when the latest release does not
// ship assetName, so callers can offer "compile from source" instead of
// a retry that can never succeed.
func DownloadReleaseAsset(ctx context.Context, assetName, destPath string, w io.Writer) error {
	fmt.Fprintf(w, "→ resolving latest release of %s...\n", version.Repo)
	rel, err := updater.FetchLatest()
	if err != nil {
		return fmt.Errorf("resolve latest release: %w", err)
	}

	downloadURL, size := releaseAssetURL(rel, assetName)
	if downloadURL == "" {
		fmt.Fprintf(w, "  release %s does not publish %s\n", rel.TagName, assetName)
		return fmt.Errorf("release %s has no asset %q for %s/%s: %w",
			rel.TagName, assetName, runtime.GOOS, runtime.GOARCH, ErrNoPrebuiltAsset)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(destPath), err)
	}

	fmt.Fprintf(w, "→ downloading %s (%s, release %s)...\n", assetName, humanSize(size), rel.TagName)
	var lastErr error
	for attempt := 0; attempt <= downloadRetries; attempt++ {
		if attempt > 0 {
			fmt.Fprintf(w, "  retrying (%d/%d): %v\n", attempt, downloadRetries, lastErr)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		lastErr = downloadTo(ctx, downloadURL, destPath, size)
		if lastErr == nil {
			fmt.Fprintf(w, "✓ installed %s\n", destPath)
			return nil
		}
		if ctx.Err() != nil || !isTransient(lastErr) {
			break
		}
	}
	return fmt.Errorf("download %s: %w", assetName, lastErr)
}

// The web fallback knows the latest tag but cannot enumerate all assets.
// Try the named release URL in that case; downloadTo distinguishes a real
// 404 from an asset that merely wasn't listed by the fallback.
func releaseAssetURL(rel *updater.Release, assetName string) (string, int64) {
	for _, asset := range rel.Assets {
		if asset.Name == assetName {
			return asset.BrowserDownloadURL, asset.Size
		}
	}
	if rel.AssetsIncomplete {
		return version.RepoURL + "/releases/download/" + url.PathEscape(rel.TagName) + "/" + url.PathEscape(assetName), 0
	}
	return "", 0
}

// downloadTo streams url into destPath via a same-directory temp file +
// rename, so an interrupted download never leaves a half-written
// executable at the final path.
func downloadTo(ctx context.Context, url, destPath string, wantSize int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "praimate-installer/"+version.Current)
	resp, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("HTTP 404 for %s: %w", url, ErrNoPrebuiltAsset)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	tmp, err := os.CreateTemp(filepath.Dir(destPath), filepath.Base(destPath)+".part-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	n, err := io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	if wantSize > 0 && n != wantSize {
		os.Remove(tmpName)
		return fmt.Errorf("truncated download: got %d of %d bytes", n, wantSize)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return err
	}
	// Windows can't rename over an existing (possibly running) exe;
	// clear the destination first, best-effort.
	_ = os.Remove(destPath)
	if err := os.Rename(tmpName, destPath); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// verifyInstalledBinary runs `<DownloadDest> --version` after a
// download/sidecar install (methods with VerifyRun set). The point:
// Bun-compiled x64 builds (praimate-code) require AVX2, and on a CPU
// or VM without it the binary faults with an illegal instruction —
// on Windows `exit status 0xc000001d` — on EVERY invocation. Before
// this check the install claimed success, detection then reported the
// tool broken/"not installed", and reinstalling downloaded the same
// incompatible binary again. Now the mismatch is caught here and,
// when the method names a FallbackAsset (the "-baseline" no-AVX2
// build), fixed automatically.
func verifyInstalledBinary(ctx context.Context, m Method, w io.Writer) error {
	if !m.VerifyRun {
		return nil
	}
	base := filepath.Base(m.DownloadDest)
	fmt.Fprintf(w, "→ verifying %s runs on this machine...\n", base)
	version, err := probeToolVersion(ctx, m.DownloadDest)
	if err == nil {
		fmt.Fprintf(w, "✓ verified: %s\n", version)
		return nil
	}
	if errors.Is(err, errProbeTimeout) {
		// Slow first start (AV scan of a fresh 170MB exe) — not broken.
		fmt.Fprintf(w, "  (version probe timed out — the binary starts slowly but was installed)\n")
		return nil
	}
	if !IsIllegalInstruction(err) {
		return fmt.Errorf("installed %s but it failed to run: %w", base, err)
	}
	if m.FallbackAsset == "" || m.FallbackAsset == m.DownloadAsset {
		return fmt.Errorf("%s crashed with an illegal instruction — this CPU cannot run the build "+
			"(it requires AVX2) and no baseline variant is available for %s/%s: %w",
			base, runtime.GOOS, runtime.GOARCH, err)
	}
	fmt.Fprintf(w, "  %s crashed with an illegal instruction — this CPU lacks AVX2 support.\n", base)
	fmt.Fprintf(w, "→ falling back to the baseline (no-AVX2) build %s...\n", m.FallbackAsset)
	if err := DownloadReleaseAsset(ctx, m.FallbackAsset, m.DownloadDest, w); err != nil {
		if errors.Is(err, ErrNoPrebuiltAsset) {
			return fmt.Errorf("this CPU needs the baseline (no-AVX2) build, but the release does not "+
				"publish %s yet — please report this so the asset gets added: %w", m.FallbackAsset, err)
		}
		return err
	}
	version, err = probeToolVersion(ctx, m.DownloadDest)
	if err != nil {
		if errors.Is(err, errProbeTimeout) {
			fmt.Fprintf(w, "  (version probe timed out — the binary starts slowly but was installed)\n")
			return nil
		}
		return fmt.Errorf("baseline build %s also failed to run: %w", m.FallbackAsset, err)
	}
	fmt.Fprintf(w, "✓ baseline build verified: %s\n", version)
	return nil
}

// installFromBundledSidecar checks whether the binary a download method
// wants already ships next to the running praimate executable (the
// release archives bundle praimate-code / praimate-graphify there when
// built with the full toolchain) and copies it into DownloadDest.
// Returns (true, err) when a sidecar was found and installed (or the
// copy failed); (false, nil) when the caller should download instead.
func installFromBundledSidecar(m Method, w io.Writer) (bool, error) {
	// The bundled sidecars are the default (AVX2) x64 builds; on a host
	// that needs the baseline variant, skip straight to the release
	// download so the right asset is fetched.
	if m.VerifyRun && hostNeedsBaselineBuild() {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, nil
	}
	exeDir := filepath.Dir(exe)
	for _, name := range sidecarNames(m) {
		cand := filepath.Join(exeDir, name)
		if st, err := os.Stat(cand); err != nil || st.IsDir() {
			continue
		}
		fmt.Fprintf(w, "→ Found bundled binary at %s\n", cand)
		fmt.Fprintf(w, "→ Copying to %s\n", m.DownloadDest)
		if err := os.MkdirAll(filepath.Dir(m.DownloadDest), 0o755); err != nil {
			return true, err
		}
		if err := copyExecutable(cand, m.DownloadDest); err != nil {
			return true, err
		}
		fmt.Fprintln(w, "✓ Install finished")
		return true, nil
	}
	return false, nil
}

// sidecarNames lists the file names the method's binary may ship under
// in the install dir: the destination base name (praimate-code.exe) and
// the asset name with its -<os>-<arch> suffix stripped
// (praimate-graphify-windows-amd64.exe → praimate-graphify.exe, the
// name the release archives actually use).
func sidecarNames(m Method) []string {
	names := []string{filepath.Base(m.DownloadDest)}
	asset := m.DownloadAsset
	ext := ""
	if strings.HasSuffix(asset, ".exe") {
		ext = ".exe"
		asset = strings.TrimSuffix(asset, ".exe")
	}
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	if strings.HasSuffix(asset, suffix) {
		if base := strings.TrimSuffix(asset, suffix) + ext; base != names[0] {
			names = append(names, base)
		}
	}
	return names
}

func copyExecutable(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open bundled binary: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy binary: %w", err)
	}
	return out.Close()
}

// isTransient reports whether a download failure is worth retrying —
// network-level drops, resets, timeouts and truncation, but not HTTP
// status errors or a missing asset.
func isTransient(err error) bool {
	if err == nil || errors.Is(err, ErrNoPrebuiltAsset) || errors.Is(err, context.Canceled) {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "HTTP ") {
		return false
	}
	return true
}

func humanSize(n int64) string {
	switch {
	case n <= 0:
		return "size unknown"
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
